package sim

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"voltsight/internal/seedgen"
)

type delayed struct {
	m Message // Payload is an owned copy
}

type shard struct {
	id       int
	vehicles []int32
	arena    []byte
	out      []Message
	ring     [][]delayed // delayed deliveries indexed by release tick % len
	outage   []delayed   // buffered during a connectivity outage, flushed at recovery
	s        sample
}

type engine struct {
	w      *world
	cfg    Config
	sink   Sink
	stats  *Stats
	shards []*shard
	baseMs int64
}

// Run simulates cfg.Duration seconds of the fleet, emitting messages to sink. It returns when the
// duration is reached, the context is cancelled, or a sink error occurs.
func Run(ctx context.Context, cfg Config, sink Sink, stats *Stats) error {
	cfg.defaults()
	if cfg.Shards <= 0 {
		cfg.Shards = defaultShards()
	}
	w, err := newWorld(cfg, stats)
	if err != nil {
		return err
	}
	if w.truth, err = newTruth(cfg.TruthDir); err != nil {
		return err
	}
	if err := w.writeVehicleTruth(cfg.TruthDir); err != nil {
		return err
	}
	e := &engine{w: w, cfg: cfg, sink: sink, stats: stats,
		baseMs: cfg.SimStart.UnixMilli() + int64(cfg.StartTOD)*1000}
	if !cfg.TimeBase.IsZero() {
		e.baseMs = cfg.TimeBase.UnixMilli()
	}
	ringSize := maxInt(cfg.OOOMaxDelay, 30) + 2
	for i := 0; i < cfg.Shards; i++ {
		e.shards = append(e.shards, &shard{id: i, arena: make([]byte, 0, 4<<20), ring: make([][]delayed, ringSize)})
	}
	for i := range w.vehicles {
		sh := e.shards[i%cfg.Shards]
		sh.vehicles = append(sh.vehicles, int32(i))
	}

	chargerSeq := make([]uint64, len(w.chargers))
	outageSet := func(i int) bool { return hash01(w.chargers[i].id) < cfg.ChargerOutageFraction }
	chargerEvents := func(now int, status uint8, only func(int) bool) error {
		var batch []Message
		for i := range w.chargers {
			if only != nil && !only(i) {
				continue
			}
			chargerSeq[i]++
			c := &w.chargers[i]
			kw := c.kw
			if status != 1 {
				kw = 0
			}
			batch = append(batch, Message{Kind: KindChargerStatus, Key: c.id,
				Payload: appendChargerStatus(nil, c.id, status, e.baseMs+int64(now)*1000, chargerSeq[i], kw)})
		}
		stats.ChargerEvents.Add(int64(len(batch)))
		if len(batch) == 0 {
			return nil
		}
		return sink.Emit(-1, now, batch)
	}

	wallStart := time.Now()
	for now := 0; now < cfg.Duration; now++ {
		if err := ctx.Err(); err != nil {
			break
		}
		w.updateAmbient(now)
		if now == 0 {
			if err := chargerEvents(now, 1, nil); err != nil { // initial state: everything available
				return err
			}
		}
		if cfg.ChargerOutageFraction > 0 && cfg.ChargerOutageDuration > 0 {
			switch now {
			case cfg.ChargerOutageAt:
				for i := range w.chargers {
					if outageSet(i) {
						w.chargers[i].avail.Store(0)
					}
				}
				if err := chargerEvents(now, 3, outageSet); err != nil {
					return err
				}
			case cfg.ChargerOutageAt + cfg.ChargerOutageDuration:
				for i := range w.chargers {
					if outageSet(i) {
						w.chargers[i].avail.Store(1)
					}
				}
				if err := chargerEvents(now, 1, outageSet); err != nil {
					return err
				}
			}
		}

		var wg sync.WaitGroup
		errs := make(chan error, len(e.shards))
		for _, sh := range e.shards {
			wg.Add(1)
			go func(sh *shard) {
				defer wg.Done()
				if err := e.tick(sh, now); err != nil {
					errs <- err
				}
			}(sh)
		}
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return fmt.Errorf("sink: %w", err)
		}
		stats.Ticks.Add(1)

		if cfg.RealTime {
			target := wallStart.Add(time.Duration(float64(now+1) / cfg.Speedup * float64(time.Second)))
			if d := time.Until(target); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
				}
			}
		}
	}
	return w.truth.close()
}

// ConnectorKey identifies one OEM-cloud connector: the tenant's vehicles of one OEM.
type ConnectorKey struct {
	Tenant uuid.UUID
	OEM    string
}

// Connectors lists the connectors a run with this configuration needs (one per tenant x OEM present).
func Connectors(cfg Config) ([]ConnectorKey, error) {
	cfg.defaults()
	sw, err := seedgen.Generate(seedgen.Config{Seed: cfg.WorldSeed, Vehicles: cfg.Vehicles})
	if err != nil {
		return nil, err
	}
	seen := map[ConnectorKey]bool{}
	var out []ConnectorKey
	for _, v := range sw.Vehicles {
		k := ConnectorKey{Tenant: v.TenantID, OEM: sw.Models[v.ModelID-1].OEM}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

func (e *engine) schemaVer(v *vehicle, now int) uint32 {
	if v.dialect == 'A' && e.cfg.SchemaV2At > 0 && now >= e.cfg.SchemaV2At {
		return 2
	}
	return 1
}

func (e *engine) inOutage(v *vehicle, now int) bool {
	return e.cfg.OutageDuration > 0 && v.outageMember && now >= e.cfg.OutageAt && now < e.cfg.OutageAt+e.cfg.OutageDuration
}

func (e *engine) tick(sh *shard, now int) error {
	cfg, w := &e.cfg, e.w
	sh.out, sh.arena = sh.out[:0], sh.arena[:0]
	mult := 1
	if cfg.BurstMult > 1 && now >= cfg.BurstAt && now < cfg.BurstAt+cfg.BurstDuration {
		mult = cfg.BurstMult
	}
	maxDelay := len(sh.ring) - 2
	for _, vi := range sh.vehicles {
		v := &w.vehicles[vi]
		w.step(v, now, &sh.s)
		sh.s.schemaVer = e.schemaVer(v, now)
		dialect := byte(0)
		if cfg.Format == FormatOEMJSON {
			dialect = v.dialect
		}
		for k := 0; k < mult; k++ {
			v.seq++
			sh.s.seq = v.seq
			sh.s.tsMs = e.baseMs + int64(now)*1000 + int64(v.phaseMs) + int64(v.skewMs) + int64(k*1000/mult)
			start := len(sh.arena)
			switch {
			case dialect == 'A':
				sh.arena = appendJSONA(sh.arena, &sh.s)
			case dialect == 'B':
				sh.arena = appendJSONB(sh.arena, &sh.s)
			default:
				sh.arena = appendProto(sh.arena, &sh.s)
			}
			payload := sh.arena[start:len(sh.arena):len(sh.arena)]
			e.stats.Generated.Add(1)

			// delivery faults: all draws are made unconditionally so a vehicle's random stream does not
			// depend on which faults are enabled or on the sharding
			mal := v.ir.Float64() < cfg.MalformedRate
			ooo := v.ir.Float64() < cfg.OOORate
			oooDelay := 1 + v.ir.IntN(maxDelay)
			dup := v.ir.Float64() < cfg.DupRate
			dupLater := v.ir.Float64() < 0.5
			dupDelay := 1 + v.ir.IntN(30)
			garble := v.ir.IntN(4)

			if mal {
				payload = corrupt(payload, dialect, garble)
				e.stats.Malformed.Add(1)
			}
			outage := e.inOutage(v, now)
			route := func(p []byte, delay int) {
				m := Message{Kind: KindTelemetry, Key: v.vin, Dialect: dialect, Tenant: v.tenant, OEM: sh.s.oem, Payload: p}
				switch {
				case outage:
					m.Payload = append([]byte(nil), p...)
					sh.outage = append(sh.outage, delayed{m})
					e.stats.OutageBuffered.Add(1)
				case delay > 0:
					m.Payload = append([]byte(nil), p...)
					slot := (now + delay) % len(sh.ring)
					sh.ring[slot] = append(sh.ring[slot], delayed{m})
				default:
					sh.out = append(sh.out, m)
				}
			}
			delay := 0
			if ooo {
				delay = oooDelay
				if !outage {
					e.stats.OutOfOrder.Add(1)
				}
			}
			route(payload, delay)
			if dup {
				e.stats.Duplicates.Add(1)
				d := 0
				if dupLater {
					d = dupDelay
				}
				route(payload, d)
			}
		}
	}

	// delayed messages that fall due now, then the outage buffer at recovery
	slot := now % len(sh.ring)
	if due := sh.ring[slot]; len(due) > 0 {
		for _, d := range due {
			sh.out = append(sh.out, d.m)
		}
		sh.ring[slot] = due[:0]
	}
	if cfg.OutageDuration > 0 && now == cfg.OutageAt+cfg.OutageDuration && len(sh.outage) > 0 {
		for _, d := range sh.outage {
			sh.out = append(sh.out, d.m)
		}
		e.stats.OutageFlushed.Add(int64(len(sh.outage)))
		sh.outage = sh.outage[:0]
	}

	n := int64(len(sh.out))
	var bytes int64
	for i := range sh.out {
		bytes += int64(len(sh.out[i].Payload))
	}
	e.stats.Emitted.Add(n)
	e.stats.Bytes.Add(bytes)
	for { // track the busiest tick of any shard
		cur := e.stats.PeakTickMessages.Load()
		if n <= cur || e.stats.PeakTickMessages.CompareAndSwap(cur, n) {
			break
		}
	}
	if n == 0 {
		return nil
	}
	return e.sink.Emit(sh.id, now, sh.out)
}

// corrupt returns a damaged copy of payload: truncated protobuf with trailing garbage, or broken JSON.
func corrupt(p []byte, dialect byte, kind int) []byte {
	out := append([]byte(nil), p...)
	if dialect == 0 {
		switch kind {
		case 0:
			return append(out[:len(out)/2], 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01)
		case 1:
			return out[:len(out)/3]
		case 2:
			return append([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f}, out...)
		}
		return []byte{0x08}
	}
	switch kind {
	case 0:
		return out[:len(out)-1] // missing closing brace
	case 1:
		return out[:len(out)/2]
	case 2:
		return append([]byte("{{"), out...)
	}
	return []byte("not json at all")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
