package rangerisk

import (
	"context"
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/state"
)

// Publisher delivers alerts. Publish must not block (the worker's single goroutine calls it); Flush blocks
// until everything published so far is durable, and is called before the worker commits Kafka offsets.
type Publisher interface {
	Publish(*alertv1.AlertEvent)
	Flush(ctx context.Context) error
}

// Config holds the decision parameters.
type Config struct {
	// Safety is the share of the estimated range held back: the vehicle must reach a charger with the
	// remaining (1-Safety) of its estimated range.
	Safety float64
	// ReserveKm: below this margin (after reaching the charger) the vehicle gets a RANGE_LOW warning.
	ReserveKm float64
	// ClearKm is the extra margin required before a raised level is lowered again (hysteresis).
	ClearKm float64
	// Confirm is how many consecutive samples must agree before a level changes (1 Hz telemetry: seconds).
	Confirm uint8
	// Window is the alert window: one alert per (vehicle, rule, window). Windows are aligned to event time so
	// that replays and duplicates produce identical idempotency keys.
	Window time.Duration
}

// DefaultConfig returns the production defaults.
func DefaultConfig() Config {
	return Config{Safety: 0.10, ReserveKm: 10, ClearKm: 3, Confirm: 2, Window: 15 * time.Minute}
}

// Processor evaluates range risk for every new telemetry event. It implements rtp.Processor.
type Processor struct {
	cfg    Config
	eng    *Engine
	dir    Directory
	est    Estimator
	pub    Publisher
	states map[string]*VehicleState

	evals, noRef, noCharger *counter
	alerts                  *prometheus.CounterVec
	decideSec               prometheus.Histogram
}

type counter struct{ prometheus.Counter }

// NewProcessor creates the processor. dir and eng come from FromWorld.
func NewProcessor(cfg Config, eng *Engine, dir Directory, est Estimator, pub Publisher) *Processor {
	p := &Processor{cfg: cfg, eng: eng, dir: dir, est: est, pub: pub, states: make(map[string]*VehicleState, len(dir))}
	p.evals = &counter{prometheus.NewCounter(prometheus.CounterOpts{Name: "risk_evaluations_total", Help: "Telemetry events evaluated."})}
	p.noRef = &counter{prometheus.NewCounter(prometheus.CounterOpts{Name: "risk_unknown_vehicle_total", Help: "Events for vehicles with no reference data."})}
	p.noCharger = &counter{prometheus.NewCounter(prometheus.CounterOpts{Name: "risk_no_reachable_charger_total", Help: "Evaluations where no available charger is connected."})}
	p.alerts = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "risk_alerts_total", Help: "Alerts published."}, []string{"rule"})
	p.decideSec = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "risk_receive_to_decision_seconds", Help: "Gateway receive to alert decision.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10}})
	return p
}

// Collectors returns the Prometheus collectors to register.
func (p *Processor) Collectors() []prometheus.Collector {
	return []prometheus.Collector{p.evals, p.noRef, p.noCharger, p.alerts, p.decideSec}
}

// Flush makes published alerts durable (called by the worker before it commits offsets).
func (p *Processor) Flush(ctx context.Context) error { return p.pub.Flush(ctx) }

// OnEvent implements rtp.Processor.
func (p *Processor) OnEvent(ev *telemetryv1.TelemetryEvent, _ *state.Entry, now time.Time) {
	ref, ok := p.dir[ev.Vin]
	if !ok {
		p.noRef.Inc()
		return
	}
	vs := p.states[ev.Vin]
	if vs == nil {
		vs = &VehicleState{}
		p.states[ev.Vin] = vs
	}
	p.evals.Inc()
	vs.observe(ev)

	if ev.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_PLUGGED || ev.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_CHARGING {
		vs.level, vs.pending = 0, 0 // on a charger: nothing to warn about
		return
	}

	d := p.decide(vs, ref, ev)
	if d.level > vs.level {
		vs.pending++
		if vs.pending < p.cfg.Confirm {
			return
		}
		vs.level, vs.pending = d.level, 0
	} else if d.level < vs.level {
		// lower the level only with extra margin (hysteresis); otherwise keep it
		clear := p.cfg.ClearKm
		if vs.level == 2 && d.margin >= clear || vs.level == 1 && d.margin >= p.cfg.ReserveKm+clear {
			if vs.pending++; vs.pending >= p.cfg.Confirm {
				vs.level, vs.pending = d.level, 0
			}
		} else {
			vs.pending = 0
		}
	} else {
		vs.pending = 0
	}
	if vs.level == 0 {
		return
	}

	// at most one alert per (vehicle, rule, window)
	win := ev.Ts.AsTime().Truncate(p.cfg.Window).Unix()
	rule, sev, last := alertv1.Rule_RULE_RANGE_LOW, alertv1.Severity_SEVERITY_WARNING, &vs.lastLow
	if vs.level == 2 {
		rule, sev, last = alertv1.Rule_RULE_RANGE_CRITICAL, alertv1.Severity_SEVERITY_CRITICAL, &vs.lastCritical
	}
	if *last == win {
		return
	}
	*last = win
	p.pub.Publish(p.alert(ev, ref, d, rule, sev, win, now))
	p.alerts.WithLabelValues(rule.String()).Inc()
}

type decision struct {
	rangeKm, usableKm, distKm, margin float64
	chargerID                         string
	reachable                         bool
	level                             uint8
}

func (p *Processor) decide(vs *VehicleState, ref VehicleRef, ev *telemetryv1.TelemetryEvent) decision {
	var d decision
	d.rangeKm = p.est.RangeKm(vs, ref, ev)
	d.usableKm = d.rangeKm * (1 - p.cfg.Safety)
	g := p.eng.Graph(ref.City)
	node := g.NearestNode(ev.Lat, ev.Lon)
	dist, id, ok := p.eng.Lookup(ref.City, ev.TenantId, node)
	d.chargerID, d.reachable = id, ok
	if ok {
		d.distKm = float64(dist) / 1000
	} else {
		d.distKm = math.Inf(1)
		p.noCharger.Inc()
	}
	d.margin = d.usableKm - d.distKm
	switch {
	case d.margin < 0:
		d.level = 2
	case d.margin < p.cfg.ReserveKm:
		d.level = 1
	}
	return d
}

func (p *Processor) alert(ev *telemetryv1.TelemetryEvent, ref VehicleRef, d decision, rule alertv1.Rule, sev alertv1.Severity, win int64, now time.Time) *alertv1.AlertEvent {
	recv := ev.RecvTs
	if recv != nil {
		p.decideSec.Observe(now.Sub(recv.AsTime()).Seconds())
	}
	m := map[string]any{
		"estimator": p.est.Name(), "soc_pct": float64(ev.SocPct), "estimated_range_km": round1(d.rangeKm),
		"safety_margin": p.cfg.Safety, "usable_range_km": round1(d.usableKm), "lat": ev.Lat, "lon": ev.Lon,
		"speed_kmh": float64(ev.SpeedKmh), "ambient_c": float64(ev.AmbientTempC), "odometer_km": ev.OdoKm,
		"reachable_charger": d.reachable,
	}
	if d.reachable {
		m["distance_to_charger_km"] = round1(d.distKm)
		m["nearest_charger_id"] = d.chargerID
		if path, dist, ok := p.eng.Route(ref.City, p.eng.Graph(ref.City).NearestNode(ev.Lat, ev.Lon), d.chargerID); ok {
			m["route_km"] = round1(float64(dist) / 1000)
			m["route"] = routePoints(p.eng.Graph(ref.City).Lat, p.eng.Graph(ref.City).Lon, path, 40)
		}
	}
	av, un := p.eng.CountAvailable(ref.City)
	m["city_chargers_available"], m["city_chargers_total"] = av, un
	st, _ := structpb.NewStruct(m)
	out := &alertv1.AlertEvent{
		TenantId: ev.TenantId, Vin: ev.Vin, Rule: rule, Severity: sev,
		WindowStart: timestamppb.New(time.Unix(win, 0).UTC()), DetectedAt: timestamppb.New(now),
		SourceEventTs: ev.Ts, SourceRecvTs: recv, SocPct: ev.SocPct, UsableRangeKm: float32(d.usableKm),
		MarginKm: float32(d.margin), NearestChargerId: d.chargerID, ModelVersion: p.est.Name(), Evidence: st,
	}
	if d.reachable {
		out.DistanceToChargerKm = float32(d.distKm)
	}
	return out
}

func round1(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return -1
	}
	return math.Round(v*10) / 10
}

// routePoints samples at most max [lat, lon] pairs along a node path.
func routePoints(lat, lon []float32, path []int32, max int) []any {
	if len(path) == 0 {
		return nil
	}
	step := 1
	if len(path) > max {
		step = (len(path) + max - 1) / max
	}
	var out []any
	for i := 0; i < len(path); i += step {
		out = append(out, []any{float64(lat[path[i]]), float64(lon[path[i]])})
	}
	last := path[len(path)-1]
	return append(out, []any{float64(lat[last]), float64(lon[last])})
}
