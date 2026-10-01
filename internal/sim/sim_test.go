package sim

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	chargerv1 "voltsight/gen/voltsight/charger/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/seedgen"
	"voltsight/internal/vin"
)

func run(t *testing.T, cfg Config, sink Sink) *Stats {
	t.Helper()
	var st Stats
	if err := Run(context.Background(), cfg, sink, &st); err != nil {
		t.Fatal(err)
	}
	return &st
}

// G2.1 deterministic and independent of the sharding.
func TestDeterminismIndependentOfShards(t *testing.T) {
	base := Config{Seed: 5, Vehicles: 1500, Duration: 90, DupRate: 0.05, OOORate: 0.03, MalformedRate: 0.01,
		BurstAt: 10, BurstDuration: 5, BurstMult: 3, OutageAt: 30, OutageDuration: 20, OutageFraction: 0.3,
		SchemaV2At: 40, Format: FormatOEMJSON, ChargerOutageAt: 20, ChargerOutageDuration: 30, ChargerOutageFraction: 0.2}
	var digests []uint64
	var gen []int64
	for _, shards := range []int{1, 3, 8} {
		cfg := base
		cfg.Shards = shards
		sink := &NullSink{}
		st := run(t, cfg, sink)
		digests, gen = append(digests, sink.Digest()), append(gen, st.Generated.Load())
	}
	if digests[0] != digests[1] || digests[1] != digests[2] || gen[0] != gen[1] || gen[1] != gen[2] {
		t.Fatalf("output depends on the shard count: digests %x generated %v", digests, gen)
	}
	other := base
	other.Seed = 6
	other.Shards = 4
	s2 := &NullSink{}
	run(t, other, s2)
	if s2.Digest() == digests[0] {
		t.Fatal("a different seed produced identical output")
	}
	again := base
	again.Shards = 2
	s3 := &NullSink{}
	run(t, again, s3)
	if s3.Digest() != digests[0] {
		t.Fatal("the same seed did not reproduce the same output")
	}
}

func fullSample() *sample {
	return &sample{vin: "1HGCM82633A004352", oem: "AURORA", schemaVer: 1, tsMs: 1790845302120, lat: 21.1702, lon: 72.8311,
		speedKmh: 64.2, socPct: 41, odoKm: 18234.7, dtc: "P0A7F", evt: evHarshBrake, seq: 88412, heading: 91.5,
		packTempC: 33.5, ambientC: 29.5, packV: 358.2, packA: -42.5, chargeState: chargeCharging, chargerID: "PUB-CHE-0001"}
}

// G2.3 the hand-written protobuf encoder equals the reference implementation.
func TestProtoEncoderMatchesReference(t *testing.T) {
	s := fullSample()
	var got telemetryv1.TelemetryEvent
	if err := proto.Unmarshal(appendProto(nil, s), &got); err != nil {
		t.Fatal(err)
	}
	want := &telemetryv1.TelemetryEvent{
		Vin: s.vin, Lat: s.lat, Lon: s.lon, SpeedKmh: s.speedKmh, SocPct: s.socPct, OdoKm: s.odoKm, Dtc: []string{"P0A7F"},
		Evt: telemetryv1.Event_HARSH_BRAKE, Seq: s.seq, Oem: "AURORA", SchemaVer: 1, HeadingDeg: s.heading, PackTempC: s.packTempC,
		AmbientTempC: s.ambientC, PackVoltageV: s.packV, PackCurrentA: s.packA, ChargeState: telemetryv1.ChargeState_CHARGE_STATE_CHARGING,
		ChargerId: s.chargerID,
	}
	if got.Ts.AsTime().UnixMilli() != s.tsMs {
		t.Fatalf("timestamp %d != %d", got.Ts.AsTime().UnixMilli(), s.tsMs)
	}
	got.Ts = nil
	if !proto.Equal(&got, want) {
		t.Fatalf("hand-written encoding differs from the reference message:\n got  %v\n want %v", &got, want)
	}
	// zero-valued fields are omitted exactly like proto3 does (and a sample with no DTC has none)
	z := &sample{vin: "1HGCM82633A004352", tsMs: 1000, oem: "X", schemaVer: 1}
	var zg telemetryv1.TelemetryEvent
	if err := proto.Unmarshal(appendProto(nil, z), &zg); err != nil || zg.SocPct != 0 || len(zg.Dtc) != 0 {
		t.Fatalf("zero sample: %v %+v", err, &zg)
	}
	st := appendChargerStatus(nil, "c1", 3, 5000, 7, 0)
	var cs chargerv1.ChargerStatusEvent
	if err := proto.Unmarshal(st, &cs); err != nil || cs.ChargerId != "c1" || cs.Status != chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE || cs.Seq != 7 {
		t.Fatalf("charger status encoding: %v %+v", err, &cs)
	}
}

// G2.3 both OEM dialects (and schema v2) are valid JSON with every field.
func TestOEMJSONDialects(t *testing.T) {
	s := fullSample()
	var a map[string]any
	if err := json.Unmarshal(appendJSONA(nil, s), &a); err != nil {
		t.Fatalf("dialect A is not valid JSON: %v", err)
	}
	spd := a["spd"].(map[string]any)
	if _, ok := spd["kmh"]; !ok || a["batt"].(map[string]any)["soc"].(float64) != 0.41 {
		t.Fatalf("dialect A v1 shape wrong: %v", a)
	}
	if a["diag"].(map[string]any)["evt"] != "HARSH_BRAKE" || a["vehicle"].(map[string]any)["vin"] != s.vin {
		t.Fatalf("dialect A fields wrong: %v", a)
	}
	s2 := *s
	s2.schemaVer = 2
	var a2 map[string]any
	if err := json.Unmarshal(appendJSONA(nil, &s2), &a2); err != nil {
		t.Fatal(err)
	}
	if _, ok := a2["spd"].(map[string]any)["kph"]; !ok {
		t.Fatalf("dialect A v2 must rename speed to kph: %v", a2)
	}
	if _, ok := a2["batt"].(map[string]any)["soh_hint"]; !ok {
		t.Fatal("dialect A v2 must add soh_hint")
	}

	var b map[string]any
	if err := json.Unmarshal(appendJSONB(nil, s), &b); err != nil {
		t.Fatalf("dialect B is not valid JSON: %v", err)
	}
	if b["socPct"].(float64) != 41 || b["dtcRaw"] != "0A7F" || b["event"] != "HB" || b["VIN"] != s.vin || b["plug"].(float64) != 2 || b["time"].(float64) != float64(s.tsMs) {
		t.Fatalf("dialect B fields wrong: %v", b)
	}
	none := *s
	none.dtc, none.evt = "", evNone
	var bn map[string]any
	if err := json.Unmarshal(appendJSONB(nil, &none), &bn); err != nil || bn["dtcRaw"] != "" || bn["event"] != "" {
		t.Fatalf("dialect B without dtc/event: %v %v", err, bn)
	}
}

// G2.4 injection rates match their flags.
func TestInjectionRates(t *testing.T) {
	const veh, dur = 4000, 100
	cfg := Config{Seed: 3, Vehicles: veh, Duration: dur, DupRate: 0.05, OOORate: 0.03, MalformedRate: 0.01, Shards: 4}
	sink := &CollectSink{}
	st := run(t, cfg, sink)
	gen := float64(st.Generated.Load())
	if gen != veh*dur {
		t.Fatalf("generated %v samples, want exactly %d", gen, veh*dur)
	}
	within := func(name string, got int64, rate float64) {
		want := gen * rate
		if math.Abs(float64(got)-want) > 0.1*want {
			t.Errorf("%s: %d injected, expected about %.0f (rate %.3f)", name, got, want, rate)
		}
	}
	within("duplicates", st.Duplicates.Load(), 0.05)
	within("out-of-order", st.OutOfOrder.Load(), 0.03)
	within("malformed", st.Malformed.Load(), 0.01)

	// delivered = generated + duplicates, minus the tail still delayed when the run ended
	delivered := float64(len(sink.Msgs))
	expect := gen + float64(st.Duplicates.Load())
	if delivered > expect || delivered < expect*0.985 {
		t.Errorf("delivered %v of an expected %v", delivered, expect)
	}

	// corrupted payloads are detectable by contract validation (decode error, missing gateway-required fields)
	valid := func(p []byte) bool {
		var e telemetryv1.TelemetryEvent
		return proto.Unmarshal(p, &e) == nil && e.Oem != "" && e.SchemaVer > 0 && vin.Valid(e.Vin) && e.Ts != nil
	}
	invalid := 0
	for _, d := range sink.Msgs {
		if d.Kind == KindTelemetry && !valid(d.Payload) {
			invalid++
		}
	}
	// malformed events can also be duplicated, so allow that slack; but nearly all must be flagged
	if float64(invalid) < 0.9*float64(st.Malformed.Load()) {
		t.Errorf("only %d of %d corrupted payloads fail validation", invalid, st.Malformed.Load())
	}
	if float64(invalid) > 1.15*float64(st.Malformed.Load())*(1+0.05) {
		t.Errorf("%d payloads fail validation but only %d were corrupted: the encoder itself produces invalid events", invalid, st.Malformed.Load())
	}

	// out-of-order: some deliveries arrive after a later sequence number of the same vehicle
	last := map[string]uint64{}
	inversions := 0
	for _, d := range sink.Msgs {
		var e telemetryv1.TelemetryEvent
		if proto.Unmarshal(d.Payload, &e) != nil || e.Oem == "" {
			continue
		}
		if e.Seq < last[e.Vin] {
			inversions++
		}
		if e.Seq > last[e.Vin] {
			last[e.Vin] = e.Seq
		}
	}
	if inversions == 0 {
		t.Error("no out-of-order deliveries observed")
	}
}

func perTick(sink *CollectSink) map[int]int {
	m := map[int]int{}
	for _, d := range sink.Msgs {
		if d.Kind == KindTelemetry {
			m[d.Tick]++
		}
	}
	return m
}

// G2.4 outage buffering ends in a flush burst; a sample-rate burst multiplies the rate.
func TestOutageFlushBurstAndRateBurst(t *testing.T) {
	const veh = 2000
	sink := &CollectSink{}
	st := run(t, Config{Seed: 9, Vehicles: veh, Duration: 80, OutageAt: 20, OutageDuration: 30, OutageFraction: 0.5,
		BurstAt: 60, BurstDuration: 10, BurstMult: 3, Shards: 3}, sink)
	pt := perTick(sink)
	if st.OutageBuffered.Load() == 0 || st.OutageBuffered.Load() != st.OutageFlushed.Load() {
		t.Fatalf("buffered %d flushed %d", st.OutageBuffered.Load(), st.OutageFlushed.Load())
	}
	if pt[10] != veh {
		t.Fatalf("steady state should deliver one event per vehicle per second, got %d", pt[10])
	}
	during := pt[30]
	if during < veh*35/100 || during > veh*65/100 {
		t.Fatalf("during the outage about half the fleet should still report, got %d of %d", during, veh)
	}
	flush := pt[50]
	if flush < veh*14/10 {
		t.Fatalf("recovery tick delivered %d, expected a burst of at least 1.4x the fleet (%d)", flush, veh)
	}
	if burst := pt[65]; burst != veh*3 {
		t.Fatalf("burst window should deliver 3 samples per vehicle per second, got %d", burst)
	}
	if st.PeakTickMessages.Load() < int64(flush)/3 {
		t.Fatal("peak tick counter not tracking")
	}
}

type checkSink struct {
	mu       sync.Mutex
	lastSeq  map[string]uint64
	lastOdo  map[string]float64
	lastSOC  map[string]float32
	chgUp    int
	chgDown  int
	driving  int
	maxSpeed float32
	bad      []string
	cities   [][2]float64
	vinSeen  map[string]bool
	events   map[telemetryv1.Event]int
	dtc      int
}

func (s *checkSink) Emit(_ int, _ int, batch []Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range batch {
		if m.Kind != KindTelemetry {
			continue
		}
		var e telemetryv1.TelemetryEvent
		if err := proto.Unmarshal(m.Payload, &e); err != nil {
			s.bad = append(s.bad, "decode: "+err.Error())
			continue
		}
		if !vin.Valid(e.Vin) {
			s.bad = append(s.bad, "invalid vin "+e.Vin)
		}
		if e.Seq <= s.lastSeq[e.Vin] {
			s.bad = append(s.bad, "seq not monotonic for "+e.Vin)
		}
		s.lastSeq[e.Vin] = e.Seq
		if e.OdoKm < s.lastOdo[e.Vin] {
			s.bad = append(s.bad, "odometer decreased for "+e.Vin)
		}
		s.lastOdo[e.Vin] = e.OdoKm
		if e.SocPct < 0 || e.SocPct > 100 {
			s.bad = append(s.bad, "soc out of range")
		}
		if e.SpeedKmh > 95 || e.SpeedKmh < 0 {
			s.bad = append(s.bad, "implausible speed")
		}
		if e.SpeedKmh > s.maxSpeed {
			s.maxSpeed = e.SpeedKmh
		}
		inCity := false
		for _, c := range s.cities {
			if math.Abs(e.Lat-c[0]) < 0.25 && math.Abs(e.Lon-c[1]) < 0.25 {
				inCity = true
			}
		}
		if !inCity {
			s.bad = append(s.bad, "position outside every city")
		}
		prev, had := s.lastSOC[e.Vin]
		if had {
			switch {
			case e.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_CHARGING && e.SocPct > prev+0.05:
				s.chgUp++
			case e.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_CHARGING && e.SocPct < prev-1:
				s.chgDown++
			case e.SpeedKmh > 20 && e.SocPct < prev:
				s.driving++
			}
		}
		s.lastSOC[e.Vin] = e.SocPct
		s.events[e.Evt]++
		if len(e.Dtc) > 0 {
			s.dtc++
		}
	}
	return nil
}

// G2.5 / G2.6 physics sanity and ground truth under a stress scenario that must produce strandings.
func TestPhysicsAndGroundTruth(t *testing.T) {
	dir := t.TempDir()
	var cities [][2]float64
	for _, c := range seedgen.Cities() {
		cities = append(cities, [2]float64{c.Lat, c.Lon})
	}
	cs := &checkSink{lastSeq: map[string]uint64{}, lastOdo: map[string]float64{}, lastSOC: map[string]float32{},
		cities: cities, vinSeen: map[string]bool{}, events: map[telemetryv1.Event]int{}}
	st := run(t, Config{Seed: 11, Vehicles: 600, Duration: 3 * 3600, Shards: 4, TruthDir: dir,
		LowSoCStartFraction: 0.4, UnawareDriverFraction: 0.5, FaultFraction: 0.1,
		ChargerOutageAt: 600, ChargerOutageDuration: 3 * 3600, ChargerOutageFraction: 0.97}, cs)
	if len(cs.bad) > 0 {
		t.Fatalf("%d invariant violations, first: %v", len(cs.bad), cs.bad[:min(5, len(cs.bad))])
	}
	if cs.driving == 0 || cs.chgUp == 0 {
		t.Fatalf("SoC must fall while driving (%d samples) and rise while charging (%d samples)", cs.driving, cs.chgUp)
	}
	if cs.chgDown > cs.chgUp/50 {
		t.Fatalf("SoC fell while charging in %d samples (vs %d rising)", cs.chgDown, cs.chgUp)
	}
	if cs.maxSpeed < 40 {
		t.Fatalf("vehicles never reach road speed (max %.1f km/h)", cs.maxSpeed)
	}
	for _, ev := range []telemetryv1.Event{telemetryv1.Event_IGNITION_ON, telemetryv1.Event_PLUG_IN, telemetryv1.Event_HARSH_BRAKE, telemetryv1.Event_LOW_SOC} {
		if cs.events[ev] == 0 {
			t.Errorf("event %v never occurred", ev)
		}
	}
	if cs.dtc == 0 {
		t.Error("no DTC reported although 10% of vehicles were configured to develop a fault")
	}
	if st.Strandings.Load() == 0 {
		t.Fatal("the stress scenario (low batteries, unaware drivers, 97% of chargers out of service) produced no strandings")
	}
	t.Logf("trips=%d chargeSessions=%d strandings=%d chargerEvents=%d", st.Trips.Load(), st.ChargeSessions.Load(), st.Strandings.Load(), st.ChargerEvents.Load())

	lines := func(name string) int {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ground truth %s: %v", name, err)
		}
		defer f.Close()
		n := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("%s line %d is not JSON: %v", name, n, err)
			}
			n++
		}
		return n
	}
	if got := lines("strandings.ndjson"); int64(got) != st.Strandings.Load() {
		t.Fatalf("strandings file has %d lines, counter says %d", got, st.Strandings.Load())
	}
	if got := lines("trips.ndjson"); int64(got) != st.Trips.Load() || got == 0 {
		t.Fatalf("trips file has %d lines, counter says %d", got, st.Trips.Load())
	}
	if got := lines("charge_sessions.ndjson"); int64(got) != st.ChargeSessions.Load() || got == 0 {
		t.Fatalf("charge sessions file has %d lines, counter says %d", got, st.ChargeSessions.Load())
	}
	vf, err := os.ReadFile(filepath.Join(dir, "vehicles.csv"))
	if err != nil || len(vf) < 1000 {
		t.Fatalf("vehicles.csv missing or empty: %v", err)
	}
}

// Charger outage events reach the sink and switch availability off and on.
func TestChargerStatusEvents(t *testing.T) {
	sink := &statusSink{}
	run(t, Config{Seed: 2, Vehicles: 200, Duration: 40, ChargerOutageAt: 10, ChargerOutageDuration: 15, ChargerOutageFraction: 0.5, Shards: 2}, sink)
	if sink.initial == 0 || sink.out == 0 || sink.back == 0 || sink.back != sink.out {
		t.Fatalf("charger events: initial=%d out=%d restored=%d", sink.initial, sink.out, sink.back)
	}
	frac := float64(sink.out) / float64(sink.initial)
	if frac < 0.4 || frac > 0.6 {
		t.Fatalf("%.2f of chargers taken out, expected about 0.5", frac)
	}
}

type statusSink struct {
	mu                 sync.Mutex
	initial, out, back int
}

func (s *statusSink) Emit(_ int, tick int, batch []Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range batch {
		if m.Kind != KindChargerStatus {
			continue
		}
		var e chargerv1.ChargerStatusEvent
		if err := proto.Unmarshal(m.Payload, &e); err != nil {
			return err
		}
		switch {
		case tick == 0:
			s.initial++
		case e.Status == chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE:
			s.out++
		case e.Status == chargerv1.ChargerStatus_CHARGER_STATUS_AVAILABLE:
			s.back++
		}
	}
	return nil
}

// G2.7 the full 100,000-vehicle fleet runs in one process; memory is bounded.
func TestHundredThousandVehicles(t *testing.T) {
	sink := &NullSink{}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	st := run(t, Config{Seed: 1, Vehicles: 100_000, Duration: 5}, sink)
	runtime.ReadMemStats(&after)
	if st.Generated.Load() != 500_000 || sink.Telemetry.Load() != 500_000 {
		t.Fatalf("generated %d, delivered %d, want 500000", st.Generated.Load(), sink.Telemetry.Load())
	}
	heapMB := float64(after.HeapAlloc) / 1e6
	t.Logf("MEASURED: 100,000 vehicles x 5 s: heap %.0f MB (sys %.0f MB)", heapMB, float64(after.Sys)/1e6)
	if heapMB > 1024 {
		t.Fatalf("heap %.0f MB for 100K vehicles exceeds the 1 GB budget", heapMB)
	}
}
