// Package chargeplan computes minimum-cost charging schedules under time-of-use tariffs.
//
// Problem: a vehicle plugged in at a depot charger must reach a target energy by its departure. Time is cut
// into slots; each slot has a price per kWh; in a slot the charger can add at most power*slot*efficiency, and
// the battery accepts less as it fills (CC-CV taper above 80%). Because of the taper the cost is not simply
// "fill the cheapest slots", so the schedule is found by dynamic programming over (slot, energy level):
//
//	cost[t+1][e'] = min over e <= e' feasible in slot t of cost[t][e] + price[t] * (e'-e) / efficiency
//
// Complexity O(T * S * A) time and O(T * S) space per vehicle (T slots, S energy levels, A admissible
// increments per slot). The baseline is "charge at full power as soon as plugged in".
package chargeplan

import (
	"errors"
	"math"
)

// Params describes one vehicle's charging problem. Energies are kWh, prices per kWh drawn from the grid.
type Params struct {
	CapacityKWh float64
	StartKWh    float64
	TargetKWh   float64   // must be reached by the end of the last slot
	PowerKW     float64   // charger limit (the vehicle's own DC limit already applied by the caller)
	Efficiency  float64   // grid -> battery, e.g. 0.93
	SlotHours   float64   // e.g. 0.25
	Price       []float64 // price per kWh for each slot (len = T)
	Blocked     []bool    // slots the charger is already occupied in (optional, len = T)
	StepKWh     float64   // energy discretisation, default 0.25
}

// Result is a schedule: AddKWh[t] is energy stored in the battery during slot t.
type Result struct {
	AddKWh   []float64
	CostINR  float64 // grid energy cost
	Feasible bool
}

// ErrInfeasible is returned when even charging in every free slot cannot reach the target.
var ErrInfeasible = errors.New("chargeplan: target not reachable before departure")

// taper returns the share of rated power the battery accepts at the given state of charge.
func taper(frac float64) float64 {
	if frac <= 0.8 {
		return 1
	}
	return math.Max(0.15, 1-(frac-0.8)/0.2*0.85)
}

func (p *Params) defaults() {
	if p.StepKWh == 0 {
		p.StepKWh = 0.25
	}
	if p.Efficiency == 0 {
		p.Efficiency = 0.93
	}
	if p.SlotHours == 0 {
		p.SlotHours = 0.25
	}
}

// maxAdd is the most energy (kWh into the battery) one slot can add when starting at energy e.
func (p *Params) maxAdd(e float64) float64 {
	return p.PowerKW * p.SlotHours * p.Efficiency * taper(e/p.CapacityKWh)
}

// Greedy charges at full power from the first free slot until the target is reached.
func Greedy(p Params) (Result, error) {
	p.defaults()
	T := len(p.Price)
	r := Result{AddKWh: make([]float64, T)}
	e := p.StartKWh
	for t := 0; t < T && e < p.TargetKWh-1e-9; t++ {
		if p.Blocked != nil && p.Blocked[t] {
			continue
		}
		add := math.Min(p.maxAdd(e), p.TargetKWh-e)
		add = math.Min(add, p.CapacityKWh-e)
		r.AddKWh[t] = add
		r.CostINR += p.Price[t] * add / p.Efficiency
		e += add
	}
	r.Feasible = e >= p.TargetKWh-1e-6
	if !r.Feasible {
		return r, ErrInfeasible
	}
	return r, nil
}

// Optimal returns the minimum-cost feasible schedule.
func Optimal(p Params) (Result, error) {
	p.defaults()
	T := len(p.Price)
	step := p.StepKWh
	// energy levels form a grid anchored at the start: level i = StartKWh + i*step
	hi := int(math.Ceil((p.TargetKWh-p.StartKWh)/step - 1e-9))
	if maxLvl := int(math.Floor((p.CapacityKWh-p.StartKWh)/step + 1e-9)); hi > maxLvl {
		hi = maxLvl
	}
	if hi <= 0 {
		return Result{AddKWh: make([]float64, T), Feasible: true}, nil
	}
	lo := 0
	n := hi + 1
	inf := math.Inf(1)
	cost := make([][]float64, T+1)
	from := make([][]int32, T+1) // level index we came from (for reconstruction)
	for t := range cost {
		cost[t] = make([]float64, n)
		from[t] = make([]int32, n)
		for i := range cost[t] {
			cost[t][i] = inf
		}
	}
	cost[0][0] = 0
	for t := 0; t < T; t++ {
		blocked := p.Blocked != nil && p.Blocked[t]
		for i := 0; i < n; i++ {
			c := cost[t][i]
			if c == inf {
				continue
			}
			// idle (always allowed)
			if c < cost[t+1][i] {
				cost[t+1][i], from[t+1][i] = c, int32(i)
			}
			if blocked {
				continue
			}
			e := p.StartKWh + float64(lo+i)*step
			room := p.maxAdd(e)
			for j := i + 1; j < n; j++ {
				add := float64(j-i) * step
				if add > room+1e-9 {
					break
				}
				nc := c + p.Price[t]*add/p.Efficiency
				if nc < cost[t+1][j] {
					cost[t+1][j], from[t+1][j] = nc, int32(i)
				}
			}
		}
	}
	if cost[T][n-1] == inf {
		return Result{AddKWh: make([]float64, T)}, ErrInfeasible
	}
	r := Result{AddKWh: make([]float64, T), CostINR: cost[T][n-1], Feasible: true}
	j := n - 1
	for t := T; t > 0; t-- {
		i := int(from[t][j])
		r.AddKWh[t-1] = float64(j-i) * step
		j = i
	}
	return r, nil
}
