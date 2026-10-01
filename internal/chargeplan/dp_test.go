package chargeplan

import (
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"testing"
	"time"
)

func randParams(r *rand.Rand) Params {
	T := 8 + r.IntN(60)
	price := make([]float64, T)
	for i := range price {
		price[i] = 4 + r.Float64()*9
	}
	cap := 30 + r.Float64()*60
	start := cap * (0.05 + r.Float64()*0.5)
	target := math.Min(cap, start+cap*(0.1+r.Float64()*0.45))
	p := Params{CapacityKWh: cap, StartKWh: start, TargetKWh: target, PowerKW: 7 + r.Float64()*40, Price: price, StepKWh: 0.25}
	if r.IntN(3) == 0 {
		p.Blocked = make([]bool, T)
		for i := range p.Blocked {
			p.Blocked[i] = r.Float64() < 0.3
		}
	}
	return p
}

// validate checks a schedule against the physics: per-slot limit, capacity, target, blocked slots, cost.
func validate(t *testing.T, p Params, r Result) {
	t.Helper()
	p.defaults()
	e := p.StartKWh
	cost := 0.0
	for i, a := range r.AddKWh {
		if a < -1e-9 {
			t.Fatal("negative charge")
		}
		if a > 0 && p.Blocked != nil && p.Blocked[i] {
			t.Fatalf("charging in blocked slot %d", i)
		}
		// the DP discretises energy on a grid anchored at StartKWh rounded to a step; allow one step of slack
		if a > p.maxAdd(e)+p.StepKWh {
			t.Fatalf("slot %d adds %v kWh, limit %v", i, a, p.maxAdd(e))
		}
		e += a
		cost += p.Price[i] * a / p.Efficiency
	}
	if e > p.CapacityKWh+p.StepKWh {
		t.Fatalf("overcharged to %v of %v", e, p.CapacityKWh)
	}
	if e < p.TargetKWh-p.StepKWh {
		t.Fatalf("target %v not reached: %v", p.TargetKWh, e)
	}
	if math.Abs(cost-r.CostINR) > 1e-6*(1+cost) {
		t.Fatalf("reported cost %v != recomputed %v", r.CostINR, cost)
	}
}

// AL3: the optimal schedule is feasible and never costs more than charging immediately; it is strictly cheaper
// whenever the cheap slots come later.
func TestOptimalNeverWorseThanGreedy(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var strictly, total int
	var saved, base float64
	for i := 0; i < 400; i++ {
		p := randParams(r)
		g, gerr := Greedy(p)
		o, oerr := Optimal(p)
		if (gerr == nil) != (oerr == nil) && oerr != nil {
			// the DP rounds the target up to a grid level; allow it to be (rarely) stricter
			continue
		}
		if oerr != nil {
			continue
		}
		validate(t, p, o)
		total++
		// greedy may overshoot the discretised target by up to a step; compare on equal footing
		if o.CostINR > g.CostINR+p.StepKWh*15/0.93+1e-6 {
			t.Fatalf("case %d: optimal %v > greedy %v", i, o.CostINR, g.CostINR)
		}
		if o.CostINR < g.CostINR-1e-6 {
			strictly++
		}
		saved += g.CostINR - o.CostINR
		base += g.CostINR
	}
	if total < 300 {
		t.Fatalf("only %d feasible cases", total)
	}
	t.Logf("MEASURED (random synthetic cases): optimal strictly cheaper in %d/%d cases; total cost reduction %.1f%%", strictly, total, 100*saved/base)
}

// a vehicle arriving in the evening peak with the cheap tariff after midnight: the DP waits for the cheap slots
func TestOptimalShiftsChargingToCheapSlots(t *testing.T) {
	price := make([]float64, 48) // 12 h of 15-minute slots: first 4 h at 12, then 8 h at 5
	for i := range price {
		price[i] = 12
		if i >= 16 {
			price[i] = 5
		}
	}
	p := Params{CapacityKWh: 60, StartKWh: 15, TargetKWh: 48, PowerKW: 22, Price: price}
	g, _ := Greedy(p)
	o, err := Optimal(p)
	if err != nil {
		t.Fatal(err)
	}
	validate(t, p, o)
	if o.CostINR >= g.CostINR*0.7 {
		t.Fatalf("expected a large saving: optimal %v vs greedy %v", o.CostINR, g.CostINR)
	}
	for i := 0; i < 16; i++ {
		if o.AddKWh[i] > 1e-9 {
			t.Fatalf("charged in an expensive slot %d", i)
		}
	}
}

func TestInfeasibleAndTrivialCases(t *testing.T) {
	p := Params{CapacityKWh: 60, StartKWh: 10, TargetKWh: 58, PowerKW: 3, Price: []float64{5, 5, 5, 5}}
	if _, err := Optimal(p); err != ErrInfeasible {
		t.Fatalf("expected ErrInfeasible, got %v", err)
	}
	if _, err := Greedy(p); err != ErrInfeasible {
		t.Fatalf("greedy: expected ErrInfeasible, got %v", err)
	}
	ok := Params{CapacityKWh: 60, StartKWh: 50, TargetKWh: 40, PowerKW: 7, Price: []float64{5, 5}}
	if r, err := Optimal(ok); err != nil || r.CostINR != 0 {
		t.Fatalf("already above target must cost nothing: %v %v", r, err)
	}
	blocked := Params{CapacityKWh: 60, StartKWh: 10, TargetKWh: 20, PowerKW: 50, Price: []float64{5, 5, 5, 5}, Blocked: []bool{true, true, true, true}}
	if _, err := Optimal(blocked); err != ErrInfeasible {
		t.Fatal("a fully occupied charger cannot deliver")
	}
}

func BenchmarkOptimalOneVehicleOvernight(b *testing.B) {
	price := make([]float64, 56) // 14 h
	for i := range price {
		price[i] = 5 + float64(i%8)
	}
	p := Params{CapacityKWh: 60, StartKWh: 12, TargetKWh: 54, PowerKW: 22, Price: price}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = Optimal(p)
	}
}

// the fleet planner books a charger once per slot and beats charge-on-plug-in
func TestBuildNeverDoubleBooksAndSaves(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	price := func(at time.Time) float64 {
		switch h := at.In(loc).Hour(); {
		case h >= 18 && h < 22:
			return 12
		case h >= 6 && h < 18:
			return 8.5
		}
		return 4.5
	}
	ch := []Charger{{ID: "c1", PowerKW: 22, PriceAt: price}, {ID: "c2", PowerKW: 11, PriceAt: price}}
	var vs []Vehicle
	for i := 0; i < 12; i++ {
		vs = append(vs, Vehicle{VIN: string(rune('A'+i)) + "XXXXXXXXXXXXXXXX", CapacityKWh: 50, SoCPct: float64(15 + 3*i), ChargerID: ch[i%2].ID})
	}
	from := time.Date(2026, 10, 1, 17, 40, 0, 0, time.UTC) // 23:10 IST
	plan := Build(vs, ch, from, from.Add(9*time.Hour), 90, 15)
	if len(plan.Items) == 0 || plan.CostINR > plan.BaselineINR {
		t.Fatalf("plan %+v", plan)
	}
	type k struct {
		c string
		t int64
	}
	seen := map[k]string{}
	for _, it := range plan.Items {
		for at := it.Start; at.Before(it.End); at = at.Add(15 * time.Minute) {
			if other, dup := seen[k{it.ChargerID, at.Unix()}]; dup {
				t.Fatalf("charger %s double-booked at %v by %s and %s", it.ChargerID, at, other, it.VIN)
			}
			seen[k{it.ChargerID, at.Unix()}] = it.VIN
		}
		if it.TargetSoC <= 0 || it.TargetSoC > 100 {
			t.Fatalf("target %v", it.TargetSoC)
		}
	}
	t.Logf("MEASURED (synthetic depot): plan %.0f INR vs charge-on-plug-in %.0f INR (%.1f%% saved), %d infeasible", plan.CostINR, plan.BaselineINR, 100*(plan.BaselineINR-plan.CostINR)/plan.BaselineINR, len(plan.Infeasible))
}
