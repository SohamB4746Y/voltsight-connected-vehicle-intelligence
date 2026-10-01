// Command vseval measures detection quality of the range-risk engine against simulator ground truth.
//
// It runs the simulator in-process (no Kafka), feeds every telemetry sample to one range-risk processor per
// estimator (and to a plain SoC-threshold rule as a third reference), and compares the CRITICAL alerts with
// the strandings the simulator actually produced. The simulator's physics contain hidden terms the estimators
// never see (true state of health, payload, driver style, wind), so this is an evaluation against a world
// that the model did not generate itself, but it is still a SIMULATED world: absolute numbers do not transfer
// to real fleets (declared limitation).
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	chargerv1 "voltsight/gen/voltsight/charger/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/rangerisk"
	"voltsight/internal/seedgen"
	"voltsight/internal/sim"
)

type capture struct {
	mu   sync.Mutex
	list []*alertv1.AlertEvent
}

func (c *capture) Publish(a *alertv1.AlertEvent) {
	c.mu.Lock()
	c.list = append(c.list, a)
	c.mu.Unlock()
}
func (c *capture) Flush(context.Context) error { return nil }

// arm is one detector under test: its own engine (charger overlay) and one processor per simulator shard.
type arm struct {
	name  string
	eng   *rangerisk.Engine
	procs []*rangerisk.Processor
	pub   *capture
}

type sink struct {
	arms   []*arm
	thr    *thresholdRule
	events int64
	mu     sync.Mutex
}

func (s *sink) Emit(shard int, _ int, batch []sim.Message) error {
	var n int64
	for i := range batch {
		m := &batch[i]
		switch m.Kind {
		case sim.KindChargerStatus:
			var cs chargerv1.ChargerStatusEvent
			if proto.Unmarshal(m.Payload, &cs) == nil {
				for _, a := range s.arms {
					a.eng.SetAvailable(cs.ChargerId, cs.Status != chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE)
				}
			}
		case sim.KindTelemetry:
			var ev telemetryv1.TelemetryEvent
			if proto.Unmarshal(m.Payload, &ev) != nil {
				continue
			}
			n++
			for _, a := range s.arms {
				a.procs[shard].OnEvent(&ev, nil, ev.Ts.AsTime())
			}
			s.thr.observe(&ev)
		}
	}
	s.mu.Lock()
	s.events += n
	s.mu.Unlock()
	return nil
}

// thresholdRule is the trivial industry baseline: warn when SoC falls below a fixed percentage.
type thresholdRule struct {
	pct   float32
	mu    sync.Mutex
	first map[string]time.Time
}

func (t *thresholdRule) observe(ev *telemetryv1.TelemetryEvent) {
	if ev.SocPct >= t.pct || ev.ChargeState != telemetryv1.ChargeState_CHARGE_STATE_NONE {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.first[ev.Vin]; !ok {
		t.first[ev.Vin] = ev.Ts.AsTime()
	}
}

type stranding struct {
	VIN string `json:"vin"`
	TS  int    `json:"t_s"`
}

func main() {
	var (
		vehicles  = flag.Int("vehicles", 3000, "fleet size")
		hours     = flag.Float64("hours", 8, "simulated hours")
		seed      = flag.Uint64("seed", 1, "behaviour seed")
		lowSoc    = flag.Float64("low-soc-fraction", 0.25, "share of vehicles that start low (stress)")
		unaware   = flag.Float64("unaware", 0.3, "share of drivers ignoring low-battery warnings (stress)")
		outage    = flag.Float64("charger-outage-fraction", 0.9, "share of chargers out of service during the outage window (a regional outage)")
		outageAt  = flag.Float64("outage-at-hours", 2, "outage start (simulated hours)")
		outageFor = flag.Float64("outage-hours", 5, "outage length (simulated hours)")
		horizon   = flag.Duration("horizon", 2*time.Hour, "an alert is correct if the vehicle strands within this horizon")
		out       = flag.String("out", "", "write the result JSON here")
		truthDir  = flag.String("truth-dir", "tmp/eval-truth", "ground-truth directory")
	)
	flag.Parse()

	cfg := sim.Config{Seed: *seed, Vehicles: *vehicles, Duration: int(*hours * 3600), StartTOD: 5*3600 + 30*60, Format: sim.FormatProto,
		LowSoCStartFraction: *lowSoc, UnawareDriverFraction: *unaware, TruthDir: *truthDir}
	if *outage > 0 {
		cfg.ChargerOutageAt, cfg.ChargerOutageDuration, cfg.ChargerOutageFraction = int(*outageAt*3600), int(*outageFor*3600), *outage
	}
	shards := 8
	cfg.Shards = shards

	sw, err := seedgen.Generate(seedgen.Config{Seed: 20260925, Vehicles: *vehicles})
	must(err)
	graphs := sim.CityGraphs(*seed)
	mk := func(name string, est rangerisk.Estimator) *arm {
		dir, chargers, err := rangerisk.FromWorld(sw, graphs)
		must(err)
		a := &arm{name: name, eng: rangerisk.NewEngine(graphs, chargers), pub: &capture{}}
		for i := 0; i < shards; i++ {
			a.procs = append(a.procs, rangerisk.NewProcessor(rangerisk.DefaultConfig(), a.eng, dir, est, a.pub))
		}
		return a
	}
	s := &sink{arms: []*arm{mk("baseline-catalogue", rangerisk.Baseline{}), mk("ewma-soc-per-km", rangerisk.EWMA{})},
		thr: &thresholdRule{pct: 15, first: map[string]time.Time{}}}

	t0 := time.Now()
	var stats sim.Stats
	must(sim.Run(context.Background(), cfg, s, &stats))
	wall := time.Since(t0)
	origin := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(cfg.StartTOD) * time.Second)

	strands := readStrandings(*truthDir)
	unawareSet := readUnaware(*truthDir)
	res := map[string]any{
		"vehicles": *vehicles, "simulated_hours": *hours, "telemetry_events": s.events, "wall_seconds": wall.Seconds(),
		"strandings_ground_truth": len(strands), "horizon_minutes": horizon.Minutes(),
		"scenario": map[string]any{"low_soc_start_fraction": *lowSoc, "unaware_driver_fraction": *unaware, "charger_outage_fraction": *outage, "outage_at_hours": *outageAt, "outage_hours": *outageFor, "seed": *seed},
	}
	arms := map[string]any{}
	for _, a := range s.arms {
		first := map[string]time.Time{}
		a.pub.mu.Lock()
		all := append([]*alertv1.AlertEvent(nil), a.pub.list...)
		a.pub.mu.Unlock()
		sort.Slice(all, func(i, j int) bool { return all[i].SourceEventTs.AsTime().Before(all[j].SourceEventTs.AsTime()) })
		total := map[string]int{}
		for _, al := range all {
			total[al.Rule.String()]++
			if al.Rule == alertv1.Rule_RULE_RANGE_CRITICAL {
				if _, ok := first[al.Vin]; !ok {
					first[al.Vin] = al.SourceEventTs.AsTime()
				}
			}
		}
		arms[a.name] = score(first, strands, unawareSet, origin, *horizon, *hours, *vehicles, total)
	}
	arms["soc-below-15pct-threshold"] = score(s.thr.first, strands, unawareSet, origin, *horizon, *hours, *vehicles, nil)
	res["detectors"] = arms
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		must(os.WriteFile(*out, append(b, '\n'), 0o644))
	}
}

// score compares the first CRITICAL alert per vehicle with the strandings.
func score(first map[string]time.Time, strands map[string]int, unaware map[string]bool, origin time.Time, horizon time.Duration,
	hours float64, vehicles int, totals map[string]int) map[string]any {
	var tp, fp, fpUnaware, tpUnaware, alertedUnaware int
	var leads []float64
	for vin, at := range first {
		ts, stranded := strands[vin]
		strandAt := origin.Add(time.Duration(ts) * time.Second)
		good := stranded && !at.After(strandAt) && strandAt.Sub(at) <= horizon
		if unaware[vin] {
			alertedUnaware++
		}
		switch {
		case good:
			tp++
			leads = append(leads, strandAt.Sub(at).Minutes())
			if unaware[vin] {
				tpUnaware++
			}
		default:
			fp++
			if unaware[vin] {
				fpUnaware++
			}
		}
	}
	caught := 0 // strandings preceded by an alert at any lead time up to the horizon
	for vin, ts := range strands {
		if at, ok := first[vin]; ok {
			strandAt := origin.Add(time.Duration(ts) * time.Second)
			if !at.After(strandAt) && strandAt.Sub(at) <= horizon {
				caught++
			}
		}
	}
	sort.Float64s(leads)
	pick := func(p float64) float64 {
		if len(leads) == 0 {
			return 0
		}
		return leads[int(p*float64(len(leads)-1))]
	}
	prec := func(tp, fp int) float64 {
		if tp+fp == 0 {
			return 0
		}
		return float64(tp) / float64(tp+fp)
	}
	rec := 0.0
	if len(strands) > 0 {
		rec = float64(caught) / float64(len(strands))
	}
	m := map[string]any{
		"vehicles_with_critical_alert": len(first), "true_positive_vehicles": tp, "false_positive_vehicles": fp,
		"precision": prec(tp, fp), "recall": rec,
		"precision_unaware_drivers":                      prec(tpUnaware, fpUnaware),
		"lead_time_minutes":                              map[string]float64{"p10": pick(0.1), "median": pick(0.5), "p90": pick(0.9)},
		"critical_alert_vehicles_per_1000_vehicle_hours": float64(len(first)) / (float64(vehicles) * hours) * 1000,
	}
	if totals != nil {
		m["alert_totals"] = totals
	}
	return m
}

func readStrandings(dir string) map[string]int {
	f, err := os.Open(dir + "/strandings.ndjson")
	must(err)
	defer f.Close()
	out := map[string]int{}
	dec := json.NewDecoder(f)
	for dec.More() {
		var s stranding
		if dec.Decode(&s) != nil {
			break
		}
		if _, ok := out[s.VIN]; !ok {
			out[s.VIN] = s.TS
		}
	}
	return out
}

func readUnaware(dir string) map[string]bool {
	f, err := os.Open(dir + "/vehicles.csv")
	must(err)
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	must(err)
	out := map[string]bool{}
	for _, r := range rows[1:] {
		out[r[0]] = r[8] == "true"
	}
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vseval:", err)
		os.Exit(1)
	}
}
