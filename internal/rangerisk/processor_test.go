package rangerisk

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/roadnet"
)

type fakePub struct{ got []*alertv1.AlertEvent }

func (f *fakePub) Publish(a *alertv1.AlertEvent) { f.got = append(f.got, a) }
func (f *fakePub) Flush(_ context.Context) error { return nil }

type fixedRange float64

func (fixedRange) Name() string { return "fixed" }
func (r fixedRange) RangeKm(*VehicleState, VehicleRef, *telemetryv1.TelemetryEvent) float64 {
	return float64(r)
}

const (
	tenantA = "aaaaaaaa-0000-0000-0000-000000000001"
	tenantB = "bbbbbbbb-0000-0000-0000-000000000002"
	vinA    = "1HGCM82633A004352"
)

type fixture struct {
	g       *roadnet.Graph
	eng     *Engine
	pub     *fakePub
	vehNode int32
	chNode  int32
}

// a 30x30 grid; the vehicle is 10 blocks (~1.5 km) from the only charger
func newFixture(t *testing.T) *fixture {
	g := roadnet.Generate("t", 13.0, 80.2, 30, 150, 1)
	f := &fixture{g: g, pub: &fakePub{}, vehNode: g.ID(5, 5), chNode: g.ID(5, 15)}
	f.eng = NewEngine([]*roadnet.Graph{g}, []Charger{
		{ID: "pub1", City: 0, Node: f.chNode, Available: true},
		{ID: "depotB", City: 0, Node: g.ID(5, 6), Tenant: tenantB, Available: true}, // right next to the vehicle, other tenant
	})
	return f
}

func (f *fixture) proc(rangeKm float64) *Processor {
	return NewProcessor(DefaultConfig(), f.eng, Directory{vinA: {City: 0}}, fixedRange(rangeKm), f.pub)
}

func (f *fixture) ev(t0 time.Time, i int, soc float32, cs telemetryv1.ChargeState) *telemetryv1.TelemetryEvent {
	return &telemetryv1.TelemetryEvent{TenantId: tenantA, Vin: vinA, Seq: uint64(i), Ts: timestamppb.New(t0.Add(time.Duration(i) * time.Second)),
		RecvTs: timestamppb.New(t0.Add(time.Duration(i) * time.Second)), Lat: float64(f.g.Lat[f.vehNode]), Lon: float64(f.g.Lon[f.vehNode]),
		SocPct: soc, ChargeState: cs, OdoKm: 1000 + float64(i)*0.01}
}

var t0 = time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)

func feed(p *Processor, f *fixture, n int, soc float32, cs telemetryv1.ChargeState, start int) {
	for i := 0; i < n; i++ {
		p.OnEvent(f.ev(t0, start+i, soc, cs), nil, t0.Add(time.Duration(start+i)*time.Second))
	}
}

// G6.5: another tenant's depot charger is never a candidate, even when it is the closest.
func TestTenantNeverRoutesToAnotherTenantsDepot(t *testing.T) {
	f := newFixture(t)
	dA, idA, ok := f.eng.Lookup(0, tenantA, f.vehNode)
	if !ok || idA != "pub1" {
		t.Fatalf("tenant A should reach the public charger, got %q ok=%v", idA, ok)
	}
	dB, idB, _ := f.eng.Lookup(0, tenantB, f.vehNode)
	if idB != "depotB" || dB >= dA {
		t.Fatalf("tenant B should use its own adjacent depot: %q at %v m (A: %v m)", idB, dB, dA)
	}
}

// G6.6 critical: the safety-discounted range cannot reach the nearest available charger
func TestCriticalWhenChargerOutOfReach(t *testing.T) {
	f := newFixture(t)
	d, _, _ := f.eng.Lookup(0, tenantA, f.vehNode)
	p := f.proc(float64(d) / 1000 * 1.05) // range barely above the distance: 10% safety makes it unreachable
	feed(p, f, 1, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 0 {
		t.Fatal("one sample must not raise an alert (confirmation)")
	}
	feed(p, f, 5, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 1)
	if len(f.pub.got) != 1 {
		t.Fatalf("got %d alerts, want exactly 1", len(f.pub.got))
	}
	a := f.pub.got[0]
	if a.Rule != alertv1.Rule_RULE_RANGE_CRITICAL || a.Severity != alertv1.Severity_SEVERITY_CRITICAL || a.NearestChargerId != "pub1" || a.MarginKm >= 0 {
		t.Fatalf("unexpected alert: %v", a)
	}
	if a.Evidence.Fields["route_km"] == nil || a.Evidence.Fields["estimator"].GetStringValue() != "fixed" || len(a.Evidence.Fields["route"].GetListValue().Values) < 2 {
		t.Fatalf("evidence incomplete: %v", a.Evidence)
	}
	// A* evidence agrees with the overlay distance
	if rk, dk := a.Evidence.Fields["route_km"].GetNumberValue(), float64(d)/1000; rk < dk-0.1 || rk > dk+0.1 {
		t.Fatalf("route %.2f km != overlay %.2f km", rk, dk)
	}
}

func TestWarningWhenMarginBelowReserve(t *testing.T) {
	f := newFixture(t)
	d, _, _ := f.eng.Lookup(0, tenantA, f.vehNode)
	p := f.proc(float64(d)/1000/0.9 + 5) // 5 km of margin after the charger: below the 10 km reserve
	feed(p, f, 5, 30, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 1 || f.pub.got[0].Rule != alertv1.Rule_RULE_RANGE_LOW || f.pub.got[0].Severity != alertv1.Severity_SEVERITY_WARNING {
		t.Fatalf("want one RANGE_LOW warning, got %v", f.pub.got)
	}
}

func TestSilentWhenComfortableOrCharging(t *testing.T) {
	f := newFixture(t)
	p := f.proc(200)
	feed(p, f, 50, 80, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 0 {
		t.Fatalf("comfortable vehicle alerted: %v", f.pub.got)
	}
	low := f.proc(0.1) // hopeless range, but plugged in
	feed(low, f, 20, 2, telemetryv1.ChargeState_CHARGE_STATE_CHARGING, 0)
	feed(low, f, 20, 2, telemetryv1.ChargeState_CHARGE_STATE_PLUGGED, 20)
	if len(f.pub.got) != 0 {
		t.Fatal("a vehicle on a charger must not alert")
	}
}

// one alert per (vehicle, rule, window); the next window alerts again while the vehicle is still at risk
func TestOneAlertPerWindowAndDeterministicKeys(t *testing.T) {
	f := newFixture(t)
	p := f.proc(0.5)
	feed(p, f, 300, 10, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 1 {
		t.Fatalf("%d alerts inside one window, want 1", len(f.pub.got))
	}
	for i := 900; i < 905; i++ { // a later window (t0+15 min)
		p.OnEvent(f.ev(t0, i, 10, telemetryv1.ChargeState_CHARGE_STATE_NONE), nil, t0)
	}
	if len(f.pub.got) != 2 || !f.pub.got[1].WindowStart.AsTime().After(f.pub.got[0].WindowStart.AsTime()) {
		t.Fatalf("expected a second alert in the next window, got %d", len(f.pub.got))
	}
	// a replay through a fresh processor (restart after a crash) yields the same idempotency keys
	f2 := newFixture(t)
	p2 := f2.proc(0.5)
	feed(p2, f2, 300, 10, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	k := func(a *alertv1.AlertEvent) [3]any { return [3]any{a.Vin, a.Rule, a.WindowStart.AsTime()} }
	if k(f.pub.got[0]) != k(f2.pub.got[0]) {
		t.Fatalf("replay key %v != %v", k(f2.pub.got[0]), k(f.pub.got[0]))
	}
}

// G6.7 logic: an outage of the nearest charger turns a safe vehicle critical, restoring it clears the level
func TestChargerOutageChangesTheDecision(t *testing.T) {
	g := roadnet.Generate("t", 13.0, 80.2, 120, 150, 1)
	f := &fixture{g: g, pub: &fakePub{}, vehNode: g.ID(5, 5)}
	f.eng = NewEngine([]*roadnet.Graph{g}, []Charger{
		{ID: "near", City: 0, Node: g.ID(5, 8), Available: true},  // ~0.45 km
		{ID: "far", City: 0, Node: g.ID(5, 115), Available: true}, // ~16.5 km
	})
	p := f.proc(14) // 12.6 km usable: a comfortable margin for "near", not enough for "far"
	feed(p, f, 10, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 0 {
		t.Fatalf("alerted with a charger in reach: %v", f.pub.got)
	}
	if !f.eng.SetAvailable("near", false) {
		t.Fatal("status change not applied")
	}
	if f.eng.SetAvailable("near", false) {
		t.Fatal("repeating a status must be a no-op")
	}
	feed(p, f, 10, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 10)
	if len(f.pub.got) != 1 || f.pub.got[0].Rule != alertv1.Rule_RULE_RANGE_CRITICAL || f.pub.got[0].NearestChargerId != "far" {
		t.Fatalf("outage should raise a critical alert towards the far charger, got %v", f.pub.got)
	}
	f.eng.SetAvailable("near", true)
	if d, id, _ := f.eng.Lookup(0, tenantA, f.vehNode); id != "near" || d > 1000 {
		t.Fatalf("restored charger not used: %q %v", id, d)
	}
}

// hysteresis: a raised level is lowered only with ClearKm of extra margin
func TestHysteresisHoldsLevelUntilClearMargin(t *testing.T) {
	f := newFixture(t)
	d, _, _ := f.eng.Lookup(0, tenantA, f.vehNode)
	need := float64(d) / 1000 / 0.9
	rng := need * 0.97 // slightly short: critical
	p := NewProcessor(DefaultConfig(), f.eng, Directory{vinA: {City: 0}}, &switchable{r: &rng}, f.pub)
	feed(p, f, 4, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if p.states[vinA].level != 2 {
		t.Fatalf("level %d, want 2", p.states[vinA].level)
	}
	rng = need + 1.0/0.9 // margin about +1 km: above 0 but below ClearKm (3)
	feed(p, f, 10, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 4)
	if p.states[vinA].level != 2 {
		t.Fatalf("level dropped to %d inside the hysteresis band", p.states[vinA].level)
	}
	rng = need + 40
	feed(p, f, 10, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 14)
	if p.states[vinA].level != 0 {
		t.Fatalf("level %d, want 0 once comfortably clear", p.states[vinA].level)
	}
}

type switchable struct{ r *float64 }

func (switchable) Name() string { return "switch" }
func (s switchable) RangeKm(*VehicleState, VehicleRef, *telemetryv1.TelemetryEvent) float64 {
	return *s.r
}

func TestUnknownVehicleIsCountedNotFatal(t *testing.T) {
	f := newFixture(t)
	p := NewProcessor(DefaultConfig(), f.eng, Directory{}, fixedRange(1), f.pub)
	feed(p, f, 3, 20, telemetryv1.ChargeState_CHARGE_STATE_NONE, 0)
	if len(f.pub.got) != 0 {
		t.Fatal("alert for a vehicle without reference data")
	}
}

// the EWMA estimator learns a vehicle's real consumption and beats the catalogue baseline once trained
func TestEWMALearnsConsumption(t *testing.T) {
	ref := VehicleRef{NominalKWh: 60, KWhPerKm: 0.15} // catalogue: 400 km at 100%
	vs := &VehicleState{}
	// the real vehicle burns 0.5 %SoC per km (200 km per charge)
	odo, soc := 1000.0, 90.0
	for i := 0; i < 400; i++ {
		odo += 0.1
		soc -= 0.05
		vs.observe(&telemetryv1.TelemetryEvent{OdoKm: odo, SocPct: float32(soc)})
	}
	ev := &telemetryv1.TelemetryEvent{SocPct: 50}
	base, ewma := Baseline{}.RangeKm(vs, ref, ev), EWMA{}.RangeKm(vs, ref, ev)
	if base < 190 || base > 210 {
		t.Fatalf("baseline %v (catalogue says 200 km at 50%%)", base)
	}
	if ewma < 95 || ewma > 105 {
		t.Fatalf("ewma %v km, want ~100 (50%% at 0.5%%/km)", ewma)
	}
	if got := (EWMA{}).RangeKm(&VehicleState{}, ref, ev); got != base {
		t.Fatalf("untrained vehicle must fall back to the baseline, got %v", got)
	}
}
