package api

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/state"
)

// VehicleState is the latest known telemetry of a vehicle (from the real-time path's Redis state).
type VehicleState struct {
	VIN         string    `json:"vin,omitempty"`
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	SocPct      float32   `json:"soc_pct"`
	SpeedKmh    float32   `json:"speed_kmh"`
	OdoKm       float64   `json:"odo_km"`
	PackTempC   float32   `json:"pack_temp_c"`
	ChargeState string    `json:"charge_state"`
	ChargerID   string    `json:"charger_id,omitempty"`
	DTC         []string  `json:"dtc,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

func toState(ev *telemetryv1.TelemetryEvent) VehicleState {
	return VehicleState{VIN: ev.Vin, Lat: ev.Lat, Lon: ev.Lon, SocPct: ev.SocPct, SpeedKmh: ev.SpeedKmh, OdoKm: ev.OdoKm, PackTempC: ev.PackTempC,
		ChargeState: strings.TrimPrefix(ev.ChargeState.String(), "CHARGE_STATE_"), ChargerID: ev.ChargerId, DTC: ev.Dtc, LastSeen: ev.Ts.AsTime()}
}

func (s *Server) maskState(p *Principal, st *VehicleState) {
	st.Lat, st.Lon = maskLocation(p, st.Lat, st.Lon)
}

// states fetches the latest state of the given vehicles in one pipelined round trip.
func (s *Server) states(ctx context.Context, tenant uuid.UUID, vins []string) (map[string]VehicleState, error) {
	out := make(map[string]VehicleState, len(vins))
	if len(vins) == 0 {
		return out, nil
	}
	pipe := s.cfg.Redis.Pipeline()
	cmds := make([]interface{ Result() (string, error) }, len(vins))
	for i, v := range vins {
		cmds[i] = pipe.HGet(ctx, state.Key(tenant.String(), v), "ev")
	}
	_, _ = pipe.Exec(ctx)
	for i, v := range vins {
		b, err := cmds[i].Result()
		if err != nil {
			continue
		}
		var ev telemetryv1.TelemetryEvent
		if proto.Unmarshal([]byte(b), &ev) == nil {
			out[v] = toState(&ev)
		}
	}
	return out, nil
}

// snapshot is a short-lived (3 s) copy of every vehicle state of a tenant, shared by the map and the summary.
type snapshot struct {
	mu   sync.Mutex
	at   time.Time
	data []VehicleState
}

func (s *Server) fleetSnapshot(ctx context.Context, tenant uuid.UUID) ([]VehicleState, error) {
	v, _ := s.snap.LoadOrStore(tenant, &snapshot{})
	sn := v.(*snapshot)
	sn.mu.Lock()
	defer sn.mu.Unlock()
	if s.cfg.Now().Sub(sn.at) < 3*time.Second && sn.data != nil {
		return sn.data, nil
	}
	var keys []string
	var cur uint64
	for {
		ks, next, err := s.cfg.Redis.Scan(ctx, cur, "t:"+tenant.String()+":v:*", 2000).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, ks...)
		if cur = next; cur == 0 {
			break
		}
	}
	data := make([]VehicleState, 0, len(keys))
	for lo := 0; lo < len(keys); lo += 2000 {
		hi := min(lo+2000, len(keys))
		pipe := s.cfg.Redis.Pipeline()
		cmds := make([]interface{ Result() (string, error) }, hi-lo)
		for i, k := range keys[lo:hi] {
			cmds[i] = pipe.HGet(ctx, k, "ev")
		}
		_, _ = pipe.Exec(ctx)
		for _, c := range cmds {
			b, err := c.Result()
			if err != nil {
				continue
			}
			var ev telemetryv1.TelemetryEvent
			if proto.Unmarshal([]byte(b), &ev) == nil {
				data = append(data, toState(&ev))
			}
		}
	}
	sn.at, sn.data = s.cfg.Now(), data
	return data, nil
}

func (s *Server) fleetSummary(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	sum := map[string]any{}
	err := s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		var vehicles, chargers int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicle WHERE tenant_id = $1`, p.TenantID).Scan(&vehicles); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM charger WHERE tenant_id IS NULL OR tenant_id = $1`, p.TenantID).Scan(&chargers); err != nil {
			return err
		}
		sum["vehicles"], sum["chargers_visible"] = vehicles, chargers
		rows, err := tx.Query(ctx, `SELECT severity, status, count(*) FROM alert WHERE tenant_id = $1 AND status <> 'resolved' GROUP BY 1, 2`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		alerts := map[string]int{}
		for rows.Next() {
			var sev, st string
			var n int
			if err := rows.Scan(&sev, &st, &n); err != nil {
				return err
			}
			alerts[sev+"/"+st] = n
		}
		sum["alerts_unresolved"] = alerts
		return rows.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	snap, err := s.fleetSnapshot(ctx, p.TenantID)
	if err != nil {
		serverError(w, err)
		return
	}
	var moving, charging, low int
	var socSum float64
	for _, v := range snap {
		socSum += float64(v.SocPct)
		if v.SpeedKmh > 3 {
			moving++
		}
		if v.ChargeState == "CHARGING" || v.ChargeState == "PLUGGED" {
			charging++
		}
		if v.SocPct < 20 {
			low++
		}
	}
	avg := 0.0
	if len(snap) > 0 {
		avg = math.Round(socSum/float64(len(snap))*10) / 10
	}
	sum["live"] = map[string]any{"reporting": len(snap), "moving": moving, "charging": charging, "low_soc": low, "avg_soc_pct": avg}
	if h, err := s.cfg.Redis.HGetAll(r.Context(), "chargers:status").Result(); err == nil {
		out := 0
		for _, v := range h {
			if v == "OUT_OF_SERVICE" {
				out++
			}
		}
		sum["chargers_out_of_service_global"] = out
	}
	writeJSON(w, 200, sum)
}

type vehicleRow struct {
	VIN          string        `json:"vin"`
	Model        string        `json:"model"`
	OEM          string        `json:"oem"`
	Fleet        string        `json:"fleet"`
	Commissioned string        `json:"commissioned_at"`
	OpenAlerts   int           `json:"open_alerts"`
	State        *VehicleState `json:"state,omitempty"`
}

const vehicleSelect = `
SELECT v.vin, vm.name, vm.oem_code, f.name, v.commissioned_at::text, COALESCE(a.n, 0)
FROM vehicle v
JOIN vehicle_model vm ON vm.id = v.model_id
JOIN fleet f ON f.tenant_id = v.tenant_id AND f.id = v.fleet_id
LEFT JOIN LATERAL (SELECT count(*)::int AS n FROM alert al
                    WHERE al.tenant_id = v.tenant_id AND al.vin = v.vin AND al.status = 'open') a ON true`

func (s *Server) listVehicles(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	limit := intParam(r, "limit", 50, 1, 200)
	after := r.URL.Query().Get("after")
	q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > 17 || strings.ContainsAny(q, "%_\\") {
		writeProblem(w, 400, "invalid search")
		return
	}
	atRisk := r.URL.Query().Get("at_risk") == "true"
	var rows []vehicleRow
	err := s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		// keyset pagination on the primary key (O(log n) per page, stable under inserts)
		query := vehicleSelect + ` WHERE v.tenant_id = $1 AND v.vin > $2 AND v.vin LIKE $3`
		if atRisk {
			query += ` AND COALESCE(a.n, 0) > 0`
		}
		query += ` ORDER BY v.vin LIMIT $4`
		rs, err := tx.Query(ctx, query, p.TenantID, after, q+"%", limit+1)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var v vehicleRow
			if err := rs.Scan(&v.VIN, &v.Model, &v.OEM, &v.Fleet, &v.Commissioned, &v.OpenAlerts); err != nil {
				return err
			}
			rows = append(rows, v)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[limit-1].VIN
	}
	vins := make([]string, len(rows))
	for i, v := range rows {
		vins[i] = v.VIN
	}
	st, _ := s.states(ctx, p.TenantID, vins)
	for i := range rows {
		if x, ok := st[rows[i].VIN]; ok {
			s.maskState(p, &x)
			rows[i].State = &x
		}
	}
	writeJSON(w, 200, map[string]any{"items": rows, "next": next})
}

func (s *Server) getVehicle(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	vin := strings.ToUpper(r.PathValue("vin"))
	var v vehicleRow
	var alerts []alertRow
	err := s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, vehicleSelect+` WHERE v.tenant_id = $1 AND v.vin = $2`, p.TenantID, vin).
			Scan(&v.VIN, &v.Model, &v.OEM, &v.Fleet, &v.Commissioned, &v.OpenAlerts); err != nil {
			return err
		}
		var err error
		alerts, err = queryAlerts(ctx, tx, p.TenantID, `AND vin = $2`, []any{vin}, 20, nil)
		return err
	})
	if err == pgx.ErrNoRows {
		notFound(w)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	if st, _ := s.states(ctx, p.TenantID, []string{vin}); len(st) == 1 {
		x := st[vin]
		s.maskState(p, &x)
		v.State = &x
	}
	writeJSON(w, 200, map[string]any{"vehicle": v, "alerts": alerts})
}

type telemetryPoint struct {
	TS       time.Time `json:"ts"`
	SocPct   float64   `json:"soc_pct"`
	SpeedKmh float64   `json:"speed_kmh"`
	TempC    float64   `json:"pack_temp_c"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
}

// vehicleTelemetry serves history from ClickHouse, always bound to the caller's tenant.
func (s *Server) vehicleTelemetry(w http.ResponseWriter, r *http.Request, p *Principal) {
	ctx := r.Context()
	vin := strings.ToUpper(r.PathValue("vin"))
	// the vehicle must belong to the tenant (404 otherwise: no cross-tenant probing)
	var ok bool
	if err := s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vehicle WHERE tenant_id = $1 AND vin = $2)`, p.TenantID, vin).Scan(&ok)
	}); err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		notFound(w)
		return
	}
	if s.cfg.CH == nil {
		writeProblem(w, 503, "history store not configured")
		return
	}
	to := s.cfg.Now().UTC()
	from := to.Add(-30 * time.Minute)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, 400, "from must be RFC 3339")
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, 400, "to must be RFC 3339")
			return
		}
		to = t
	}
	if !to.After(from) || to.Sub(from) > 48*time.Hour {
		writeProblem(w, 400, "range must be positive and at most 48 h")
		return
	}
	step := intParam(r, "step", 10, 1, 3600)
	rows, err := s.cfg.CH.Query(ctx, `SELECT toStartOfInterval(ts, INTERVAL @step SECOND) AS b, avg(soc_pct), avg(speed_kmh), avg(pack_temp_c), avg(lat), avg(lon)
		FROM telemetry_raw WHERE tenant_id = @tenant AND vin = @vin AND ts >= @from AND ts < @to GROUP BY b ORDER BY b LIMIT 5000`,
		clickhouse.Named("step", uint32(step)), clickhouse.Named("tenant", p.TenantID), clickhouse.Named("vin", vin),
		clickhouse.Named("from", from), clickhouse.Named("to", to))
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	out := []telemetryPoint{}
	for rows.Next() {
		var pt telemetryPoint
		if err := rows.Scan(&pt.TS, &pt.SocPct, &pt.SpeedKmh, &pt.TempC, &pt.Lat, &pt.Lon); err != nil {
			serverError(w, err)
			return
		}
		pt.Lat, pt.Lon = maskLocation(p, pt.Lat, pt.Lon)
		pt.SocPct, pt.SpeedKmh, pt.TempC = math.Round(pt.SocPct*10)/10, math.Round(pt.SpeedKmh*10)/10, math.Round(pt.TempC*10)/10
		out = append(out, pt)
	}
	writeJSON(w, 200, map[string]any{"vin": vin, "step_seconds": step, "points": out})
}

type cell struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Count  int     `json:"count"`
	AvgSoc float64 `json:"avg_soc_pct"`
	Low    int     `json:"low_soc"`
}

// mapCells aggregates the live fleet into grid cells on the server (the browser never receives 100K points).
// Roles without PermPreciseGeo get cells of at least ~2 km.
func (s *Server) mapCells(w http.ResponseWriter, r *http.Request, p *Principal) {
	size := 0.01
	if v := r.URL.Query().Get("cell"); v != "" {
		var f float64
		if _, err := fmtSscan(v, &f); err == nil && f >= 0.001 && f <= 1 {
			size = f
		}
	}
	if !p.Has(PermPreciseGeo) && size < 0.02 {
		size = 0.02
	}
	snap, err := s.fleetSnapshot(r.Context(), p.TenantID)
	if err != nil {
		serverError(w, err)
		return
	}
	type key struct{ i, j int }
	agg := map[key]*cell{}
	for _, v := range snap {
		k := key{int(math.Floor(v.Lat / size)), int(math.Floor(v.Lon / size))}
		c := agg[k]
		if c == nil {
			c = &cell{Lat: (float64(k.i) + 0.5) * size, Lon: (float64(k.j) + 0.5) * size}
			agg[k] = c
		}
		c.Count++
		c.AvgSoc += float64(v.SocPct)
		if v.SocPct < 20 {
			c.Low++
		}
	}
	out := make([]cell, 0, len(agg))
	for _, c := range agg {
		c.AvgSoc = math.Round(c.AvgSoc/float64(c.Count)*10) / 10
		out = append(out, *c)
	}
	writeJSON(w, 200, map[string]any{"cell_degrees": size, "vehicles": len(snap), "cells": out})
}
