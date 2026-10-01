package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The geographic map is fed by two read-only endpoints. Positions come from the hot per-vehicle state the stream
// worker already keeps in Redis (the same snapshot, cached for 3 s per tenant, that serves the dashboard), the
// risk state from the tenant's OPEN alerts (which the risk engine created), and chargers from PostgreSQL plus the
// live status hash. Nothing here is computed in the browser and nothing is invented: a field that is not known
// is simply absent ("not available" in the UI).

// metroCities are the metro areas of the synthetic world (same centres as seedgen.Cities); a vehicle's city is the
// nearest centre, which matches how the simulator places vehicles.
var metroCities = []struct {
	Name     string
	Lat, Lon float64
}{{"Chennai", 13.0827, 80.2707}, {"Bengaluru", 12.9716, 77.5946}, {"Surat", 21.1702, 72.8311}}

func nearestCity(lat, lon float64) string {
	best, bd := "", math.MaxFloat64
	for _, c := range metroCities {
		d := (lat-c.Lat)*(lat-c.Lat) + (lon-c.Lon)*(lon-c.Lon)
		if d < bd {
			best, bd = c.Name, d
		}
	}
	return best
}

// mapVehicle is one marker. Risk is "ok" | "low" | "critical", taken from the vehicle's open alert.
type mapVehicle struct {
	VIN         string    `json:"vin"`
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	SocPct      float32   `json:"soc_pct"`
	SpeedKmh    float32   `json:"speed_kmh"`
	ChargeState string    `json:"charge_state"`
	LastSeen    time.Time `json:"last_seen"`
	City        string    `json:"city"`
	Risk        string    `json:"risk"`
	AlertID     string    `json:"alert_id,omitempty"`
	Rule        string    `json:"rule,omitempty"`
	UsableKm    *float64  `json:"usable_range_km,omitempty"`
	ChargerID   string    `json:"nearest_charger_id,omitempty"`
	ChargerKm   *float64  `json:"charger_distance_km,omitempty"`
	MarginKm    *float64  `json:"range_margin_km,omitempty"`
	AlertAt     time.Time `json:"alert_detected_at,omitzero"`
}

type openRisk struct {
	alertID, rule, severity, charger string
	at                               time.Time
	usable, dist                     *float64
}

type riskCache struct {
	mu   sync.Mutex
	at   time.Time
	data map[string]openRisk
}

// openRiskByVIN: the most severe, most recent open alert per vehicle, with the evidence fields the map shows.
func (s *Server) openRiskByVIN(ctx context.Context, tenant uuid.UUID) (map[string]openRisk, error) {
	v, _ := s.risk.LoadOrStore(tenant, &riskCache{})
	rc := v.(*riskCache)
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if s.cfg.Now().Sub(rc.at) < 2*time.Second && rc.data != nil {
		return rc.data, nil
	}
	out := map[string]openRisk{}
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rs, err := tx.Query(ctx, `
			SELECT DISTINCT ON (vin) vin, id::text, rule, severity, detected_at,
			       (evidence->>'usable_range_km')::float8, (evidence->>'distance_to_charger_km')::float8, evidence->>'nearest_charger_id'
			FROM alert WHERE tenant_id = $1 AND status = 'open'
			ORDER BY vin, (severity = 'CRITICAL') DESC, detected_at DESC`, tenant)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var vin string
			var o openRisk
			var ch *string
			if err := rs.Scan(&vin, &o.alertID, &o.rule, &o.severity, &o.at, &o.usable, &o.dist, &ch); err != nil {
				return err
			}
			if ch != nil {
				o.charger = *ch
			}
			out[vin] = o
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}
	rc.at, rc.data = s.cfg.Now(), out
	return out, nil
}

// mapVehicles: GET /v1/map/vehicles?city=Chennai&limit=5000. Tenant-bound (the snapshot key and the alert query both
// carry the caller's tenant; the latter also under RLS). Locations are masked for roles without geo.precise.
func (s *Server) mapVehicles(w http.ResponseWriter, r *http.Request, p *Principal) {
	city := strings.TrimSpace(r.URL.Query().Get("city"))
	limit := intParam(r, "limit", 5000, 1, 20000)
	snap, err := s.fleetSnapshot(r.Context(), p.TenantID)
	if err != nil {
		serverError(w, err)
		return
	}
	risk, err := s.openRiskByVIN(r.Context(), p.TenantID)
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]mapVehicle, 0, min(len(snap), limit))
	for _, st := range snap {
		c := nearestCity(st.Lat, st.Lon)
		if city != "" && !strings.EqualFold(city, c) {
			continue
		}
		lat, lon := maskLocation(p, st.Lat, st.Lon)
		mv := mapVehicle{VIN: st.VIN, Lat: lat, Lon: lon, SocPct: st.SocPct, SpeedKmh: st.SpeedKmh, ChargeState: st.ChargeState, LastSeen: st.LastSeen, City: c, Risk: "ok"}
		if o, ok := risk[st.VIN]; ok {
			mv.Risk = "low"
			if o.severity == "CRITICAL" {
				mv.Risk = "critical"
			}
			mv.AlertID, mv.Rule, mv.AlertAt, mv.UsableKm, mv.ChargerID, mv.ChargerKm = o.alertID, o.rule, o.at, o.usable, o.charger, o.dist
			if o.usable != nil && o.dist != nil {
				m := *o.usable - *o.dist
				mv.MarginKm = &m
			}
		}
		out = append(out, mv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VIN < out[j].VIN })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, 200, map[string]any{"at": s.cfg.Now().UTC(), "count": len(out), "vehicles": out})
}

// mapCharger is one charger marker; Status is exactly what the charger.status.v1 stream last said (default AVAILABLE).
type mapCharger struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Network string    `json:"network"`
	Lat     float64   `json:"lat"`
	Lon     float64   `json:"lon"`
	PowerKW float64   `json:"power_kw"`
	Status  string    `json:"status"`
	City    string    `json:"city"`
}

// mapChargers: GET /v1/map/chargers?city=. The charger table is under RLS: public chargers and the tenant's own depot chargers.
func (s *Server) mapChargers(w http.ResponseWriter, r *http.Request, p *Principal) {
	city := strings.TrimSpace(r.URL.Query().Get("city"))
	var rows []mapCharger
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT id, name, tenant_id IS NULL, lat, lon, power_kw::float8 FROM charger ORDER BY id`)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var c mapCharger
			var public bool
			if err := rs.Scan(&c.ID, &c.Name, &public, &c.Lat, &c.Lon, &c.PowerKW); err != nil {
				return err
			}
			c.Network = "depot"
			if public {
				c.Network = "public"
			}
			rows = append(rows, c)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	status, _ := s.cfg.Redis.HGetAll(r.Context(), chargerStatusHash).Result()
	out := rows[:0]
	for _, c := range rows {
		c.City = nearestCity(c.Lat, c.Lon)
		if city != "" && !strings.EqualFold(city, c.City) {
			continue
		}
		c.Status = "AVAILABLE"
		if v, ok := status[c.ID.String()]; ok {
			c.Status = v
		}
		c.Lat, c.Lon = maskLocation(p, c.Lat, c.Lon)
		out = append(out, c)
	}
	writeJSON(w, 200, map[string]any{"count": len(out), "chargers": out})
}
