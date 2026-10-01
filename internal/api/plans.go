package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"voltsight/internal/chargeplan"
)

var ist = time.FixedZone("IST", 5*3600+1800) // all tariff zones of the synthetic world are in India

type planRow struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	CreatedBy  uuid.UUID  `json:"created_by"`
	ApprovedBy *uuid.UUID `json:"approved_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	Cost       *float64   `json:"cost_estimate_inr"`
	Baseline   *float64   `json:"baseline_cost_estimate_inr"`
	Items      int        `json:"items"`
	SavingsPct float64    `json:"savings_pct"`
}

func (s *Server) listPlans(w http.ResponseWriter, r *http.Request, p *Principal) {
	var rows []planRow
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT cp.id, cp.status, cp.created_by, cp.approved_by, cp.created_at, cp.cost_estimate::float8, cp.baseline_cost_estimate::float8,
			(SELECT count(*) FROM charge_plan_item i WHERE i.tenant_id = cp.tenant_id AND i.plan_id = cp.id)::int
			FROM charge_plan cp WHERE cp.tenant_id = $1 ORDER BY cp.created_at DESC LIMIT 50`, p.TenantID)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var x planRow
			if err := rs.Scan(&x.ID, &x.Status, &x.CreatedBy, &x.ApprovedBy, &x.CreatedAt, &x.Cost, &x.Baseline, &x.Items); err != nil {
				return err
			}
			if x.Cost != nil && x.Baseline != nil && *x.Baseline > 0 {
				x.SavingsPct = round1f((*x.Baseline - *x.Cost) / *x.Baseline * 100)
			}
			rows = append(rows, x)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	if rows == nil {
		rows = []planRow{}
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

func round1f(v float64) float64 { return float64(int64(v*10+0.5*sign(v))) / 10 }

// ProposeRequest asks for a charging plan.
type ProposeRequest struct {
	Departure *time.Time `json:"departure,omitempty"` // default: now + 10 h
	TargetSoC float64    `json:"target_soc_pct,omitempty"`
	Limit     int        `json:"limit,omitempty"`
	VINs      []string   `json:"vins,omitempty"`
}

// PlanSummary is the result of a proposal.
type PlanSummary struct {
	ID          uuid.UUID `json:"id"`
	Status      string    `json:"status"`
	Vehicles    int       `json:"vehicles_planned"`
	Items       int       `json:"items"`
	CostINR     float64   `json:"cost_estimate_inr"`
	BaselineINR float64   `json:"baseline_cost_estimate_inr"`
	SavingsPct  float64   `json:"savings_pct"`
	Infeasible  []string  `json:"infeasible_vins"`
}

func (s *Server) proposePlan(w http.ResponseWriter, r *http.Request, p *Principal) {
	var req ProposeRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	sum, err := s.ProposePlan(r, p, req)
	switch {
	case err == nil:
		writeJSON(w, 201, sum)
	case strings.HasPrefix(err.Error(), "bad request:"):
		writeProblem(w, 400, strings.TrimPrefix(err.Error(), "bad request: "))
	case strings.HasPrefix(err.Error(), "conflict:"):
		writeProblem(w, 409, strings.TrimPrefix(err.Error(), "conflict: "))
	default:
		serverError(w, err)
	}
}

// ProposePlan builds and stores a plan (status "proposed"). Nothing is scheduled until a person approves it.
func (s *Server) ProposePlan(r *http.Request, p *Principal, req ProposeRequest) (*PlanSummary, error) {
	ctx := r.Context()
	now := s.cfg.Now().UTC()
	dep := now.Add(10 * time.Hour)
	if req.Departure != nil {
		dep = req.Departure.UTC()
	}
	if !dep.After(now.Add(time.Hour)) || dep.After(now.Add(24*time.Hour)) {
		return nil, fmt.Errorf("bad request: departure must be between 1 h and 24 h from now")
	}
	target := req.TargetSoC
	if target == 0 {
		target = 90
	}
	if target < 30 || target > 100 {
		return nil, fmt.Errorf("bad request: target_soc_pct must be 30..100")
	}
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	snap, err := s.fleetSnapshot(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, v := range req.VINs {
		want[strings.ToUpper(v)] = true
	}
	var cand []VehicleState
	for _, v := range snap {
		if v.SoCLow(target) && v.SpeedKmh < 3 && (len(want) == 0 || want[v.VIN]) {
			cand = append(cand, v)
		}
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].SocPct < cand[j].SocPct })
	if len(cand) > limit {
		cand = cand[:limit]
	}
	if len(cand) == 0 {
		return nil, fmt.Errorf("conflict: no parked vehicle below the target is reporting live state")
	}
	vins := make([]string, len(cand))
	for i, c := range cand {
		vins[i] = c.VIN
	}

	var sum *PlanSummary
	err = s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		// vehicle capacity (latest SoH estimate if any, else nominal) and home depot
		rs, err := tx.Query(ctx, `SELECT v.vin, COALESCE(h.capacity_kwh, vm.battery_kwh_nominal)::float8, v.home_depot_id
			FROM vehicle v JOIN vehicle_model vm ON vm.id = v.model_id
			LEFT JOIN LATERAL (SELECT capacity_kwh FROM soh_estimate e WHERE e.tenant_id = v.tenant_id AND e.vin = v.vin ORDER BY as_of DESC LIMIT 1) h ON true
			WHERE v.tenant_id = $1 AND v.vin = ANY($2)`, p.TenantID, vins)
		if err != nil {
			return err
		}
		type vi struct {
			cap   float64
			depot *uuid.UUID
		}
		info := map[string]vi{}
		for rs.Next() {
			var vin string
			var x vi
			if err := rs.Scan(&vin, &x.cap, &x.depot); err != nil {
				rs.Close()
				return err
			}
			info[vin] = x
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return err
		}
		// depot chargers (own tenant) with their tariff
		cs, err := tx.Query(ctx, `SELECT id, depot_id, power_kw::float8, tariff_zone FROM charger WHERE tenant_id = $1 AND depot_id IS NOT NULL ORDER BY id`, p.TenantID)
		if err != nil {
			return err
		}
		type chg struct {
			id    uuid.UUID
			depot uuid.UUID
			kw    float64
			zone  int16
		}
		byDepot := map[uuid.UUID][]chg{}
		for cs.Next() {
			var c chg
			if err := cs.Scan(&c.id, &c.depot, &c.kw, &c.zone); err != nil {
				cs.Close()
				return err
			}
			byDepot[c.depot] = append(byDepot[c.depot], c)
		}
		cs.Close()
		if err := cs.Err(); err != nil {
			return err
		}
		bands := map[int16][][3]float64{} // zone -> (from, to, price)
		bs, err := tx.Query(ctx, `SELECT zone_id, hour_from, hour_to, price_per_kwh::float8 FROM tariff_band`)
		if err != nil {
			return err
		}
		for bs.Next() {
			var z int16
			var f, t int16
			var pr float64
			if err := bs.Scan(&z, &f, &t, &pr); err != nil {
				bs.Close()
				return err
			}
			bands[z] = append(bands[z], [3]float64{float64(f), float64(t), pr})
		}
		bs.Close()
		priceFn := func(zone int16) func(time.Time) float64 {
			return func(at time.Time) float64 {
				h := float64(at.In(ist).Hour())
				for _, b := range bands[zone] {
					if h >= b[0] && h < b[1] {
						return b[2]
					}
				}
				return 8
			}
		}
		var cvs []chargeplan.Vehicle
		chargers := map[string]chargeplan.Charger{}
		rr := map[uuid.UUID]int{}
		for _, c := range cand {
			x, ok := info[c.VIN]
			if !ok || x.depot == nil || len(byDepot[*x.depot]) == 0 {
				continue
			}
			list := byDepot[*x.depot]
			ch := list[rr[*x.depot]%len(list)]
			rr[*x.depot]++
			chargers[ch.id.String()] = chargeplan.Charger{ID: ch.id.String(), PowerKW: ch.kw, PriceAt: priceFn(ch.zone)}
			cvs = append(cvs, chargeplan.Vehicle{VIN: c.VIN, CapacityKWh: x.cap, SoCPct: float64(c.SocPct), ChargerID: ch.id.String()})
		}
		if len(cvs) == 0 {
			return fmt.Errorf("conflict: none of the vehicles has a depot charger")
		}
		var chs []chargeplan.Charger
		for _, c := range chargers {
			chs = append(chs, c)
		}
		plan := chargeplan.Build(cvs, chs, now, dep, target, 15)
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO charge_plan (tenant_id, created_by, cost_estimate, baseline_cost_estimate) VALUES ($1, $2, $3, $4) RETURNING id`,
			p.TenantID, p.UserID, plan.CostINR, plan.BaselineINR).Scan(&id); err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, it := range plan.Items {
			b.Queue(`INSERT INTO charge_plan_item (tenant_id, plan_id, vin, charger_id, start_at, end_at, target_soc) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				p.TenantID, id, it.VIN, it.ChargerID, it.Start, it.End, it.TargetSoC)
		}
		res := tx.SendBatch(ctx, b)
		for range plan.Items {
			if _, err := res.Exec(); err != nil {
				_ = res.Close()
				return err
			}
		}
		if err := res.Close(); err != nil {
			return err
		}
		sv := 0.0
		if plan.BaselineINR > 0 {
			sv = round1f((plan.BaselineINR - plan.CostINR) / plan.BaselineINR * 100)
		}
		inf := plan.Infeasible
		if inf == nil {
			inf = []string{}
		}
		sum = &PlanSummary{ID: id, Status: "proposed", Vehicles: len(cvs) - len(plan.Infeasible), Items: len(plan.Items), CostINR: plan.CostINR,
			BaselineINR: plan.BaselineINR, SavingsPct: sv, Infeasible: inf}
		return nil
	})
	return sum, err
}

// SoCLow reports whether the vehicle is below the target state of charge.
func (v VehicleState) SoCLow(target float64) bool { return float64(v.SocPct) < target-5 }

func (s *Server) decidePlan(status string) handler {
	return func(w http.ResponseWriter, r *http.Request, p *Principal) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			notFound(w)
			return
		}
		var n int64
		err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
			var tag interface{ RowsAffected() int64 }
			var e error
			if status == "approved" {
				tag, e = tx.Exec(r.Context(), `UPDATE charge_plan SET status = 'approved', approved_by = $3, approved_at = now() WHERE tenant_id = $1 AND id = $2 AND status = 'proposed'`, p.TenantID, id, p.UserID)
			} else {
				tag, e = tx.Exec(r.Context(), `UPDATE charge_plan SET status = 'rejected' WHERE tenant_id = $1 AND id = $2 AND status = 'proposed'`, p.TenantID, id)
			}
			if e == nil {
				n = tag.RowsAffected()
			}
			return e
		})
		if err != nil {
			serverError(w, err)
			return
		}
		if n == 0 {
			writeProblem(w, 409, "plan not found or not in the proposed state")
			return
		}
		writeJSON(w, 200, map[string]any{"id": id, "status": status})
	}
}

func (s *Server) reportCost(w http.ResponseWriter, r *http.Request, p *Principal) {
	type day struct {
		Day      string  `json:"day"`
		Plans    int     `json:"plans"`
		Cost     float64 `json:"cost_inr"`
		Baseline float64 `json:"baseline_inr"`
	}
	var out []day
	var cost, base float64
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT created_at::date::text, count(*)::int, COALESCE(sum(cost_estimate),0)::float8, COALESCE(sum(baseline_cost_estimate),0)::float8
			FROM charge_plan WHERE tenant_id = $1 AND status = 'approved' GROUP BY 1 ORDER BY 1 DESC LIMIT 60`, p.TenantID)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var d day
			if err := rs.Scan(&d.Day, &d.Plans, &d.Cost, &d.Baseline); err != nil {
				return err
			}
			cost += d.Cost
			base += d.Baseline
			out = append(out, d)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	if out == nil {
		out = []day{}
	}
	writeJSON(w, 200, map[string]any{"approved_plans_by_day": out, "total_cost_inr": cost, "total_baseline_inr": base, "total_saved_inr": base - cost})
}

func (s *Server) reportSoH(w http.ResponseWriter, r *http.Request, p *Principal) {
	type vrow struct {
		VIN      string  `json:"vin"`
		Capacity float64 `json:"capacity_kwh"`
		Nominal  float64 `json:"nominal_kwh"`
		SoHPct   float64 `json:"soh_pct"`
		Method   string  `json:"method"`
	}
	var worst []vrow
	var n int
	var avg float64
	buckets := map[string]int{"<70": 0, "70-80": 0, "80-90": 0, ">=90": 0}
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT DISTINCT ON (e.vin) e.vin, e.capacity_kwh::float8, vm.battery_kwh_nominal::float8, e.method
			FROM soh_estimate e JOIN vehicle v ON v.tenant_id = e.tenant_id AND v.vin = e.vin JOIN vehicle_model vm ON vm.id = v.model_id
			WHERE e.tenant_id = $1 ORDER BY e.vin, e.as_of DESC`, p.TenantID)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var v vrow
			if err := rs.Scan(&v.VIN, &v.Capacity, &v.Nominal, &v.Method); err != nil {
				return err
			}
			v.SoHPct = round1f(v.Capacity / v.Nominal * 100)
			avg += v.SoHPct
			n++
			switch {
			case v.SoHPct < 70:
				buckets["<70"]++
			case v.SoHPct < 80:
				buckets["70-80"]++
			case v.SoHPct < 90:
				buckets["80-90"]++
			default:
				buckets[">=90"]++
			}
			worst = append(worst, v)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	sort.Slice(worst, func(i, j int) bool { return worst[i].SoHPct < worst[j].SoHPct })
	if len(worst) > 20 {
		worst = worst[:20]
	}
	if worst == nil {
		worst = []vrow{}
	}
	mean := 0.0
	if n > 0 {
		mean = round1f(avg / float64(n))
	}
	writeJSON(w, 200, map[string]any{"vehicles_estimated": n, "mean_soh_pct": mean, "distribution": buckets, "lowest": worst})
}

// reportEnergy reads the daily trip rollup through trip_daily_v (tenant-filtered security-barrier view).
func (s *Server) reportEnergy(w http.ResponseWriter, r *http.Request, p *Principal) {
	type day struct {
		Day   time.Time `json:"day"`
		Trips int       `json:"trips"`
		Km    float64   `json:"km"`
		KWh   float64   `json:"kwh"`
	}
	out := []day{}
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT day, trips, km, kwh FROM trip_daily_v ORDER BY day DESC LIMIT 60`)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var d day
			if err := rs.Scan(&d.Day, &d.Trips, &d.Km, &d.KWh); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": out})
}
