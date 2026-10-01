package chargeplan

import (
	"math"
	"sort"
	"time"
)

// Vehicle is one vehicle to charge.
type Vehicle struct {
	VIN         string
	CapacityKWh float64
	SoCPct      float64
	ChargerID   string // the depot charger it is assigned to
}

// Charger is a depot charger.
type Charger struct {
	ID      string
	PowerKW float64
	// PriceAt returns the price per kWh at a wall-clock time (time-of-use band of the charger's tariff zone).
	PriceAt func(time.Time) float64
}

// Item is a contiguous block of charging.
type Item struct {
	VIN       string
	ChargerID string
	Start     time.Time
	End       time.Time
	TargetSoC float64
}

// Plan is the proposed schedule with its cost against the charge-on-plug-in baseline.
type Plan struct {
	Items       []Item
	CostINR     float64
	BaselineINR float64
	Vehicles    int
	Infeasible  []string
}

// Build schedules every vehicle on its charger from `from` until `departure` in slotMinutes slots. Vehicles on
// the same charger are served in order of urgency (lowest SoC first): a vehicle's slots become unavailable to
// the next one, so a charger is never double-booked. The same procedure is run with the greedy strategy to get
// the baseline cost.
func Build(vehicles []Vehicle, chargers []Charger, from, departure time.Time, targetSoCPct float64, slotMinutes int) Plan {
	if slotMinutes <= 0 {
		slotMinutes = 15
	}
	slot := time.Duration(slotMinutes) * time.Minute
	from = from.Truncate(slot).Add(slot)
	T := int(departure.Sub(from) / slot)
	if T <= 0 {
		return Plan{}
	}
	byID := map[string]Charger{}
	for _, c := range chargers {
		byID[c.ID] = c
	}
	order := append([]Vehicle(nil), vehicles...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].SoCPct < order[j].SoCPct })

	run := func(optimal bool) (items []Item, cost float64, infeasible []string) {
		busy := map[string][]bool{}
		for _, v := range order {
			ch, ok := byID[v.ChargerID]
			if !ok || v.CapacityKWh <= 0 || v.SoCPct >= targetSoCPct {
				continue
			}
			if busy[ch.ID] == nil {
				busy[ch.ID] = make([]bool, T)
			}
			price := make([]float64, T)
			for t := range price {
				price[t] = ch.PriceAt(from.Add(time.Duration(t) * slot))
			}
			p := Params{CapacityKWh: v.CapacityKWh, StartKWh: v.SoCPct / 100 * v.CapacityKWh, TargetKWh: targetSoCPct / 100 * v.CapacityKWh,
				PowerKW: ch.PowerKW, SlotHours: slot.Hours(), Price: price, Blocked: busy[ch.ID], StepKWh: 0.25}
			var res Result
			var err error
			if optimal {
				res, err = Optimal(p)
			} else {
				res, err = Greedy(p)
			}
			if err != nil {
				infeasible = append(infeasible, v.VIN)
				continue
			}
			cost += res.CostINR
			e := p.StartKWh
			start := -1
			flush := func(end int) {
				if start >= 0 {
					items = append(items, Item{VIN: v.VIN, ChargerID: ch.ID, Start: from.Add(time.Duration(start) * slot),
						End: from.Add(time.Duration(end) * slot), TargetSoC: math.Min(100, e/v.CapacityKWh*100)})
					start = -1
				}
			}
			for t, a := range res.AddKWh {
				if a > 1e-9 {
					if start < 0 {
						start = t
					}
					busy[ch.ID][t] = true
					e += a
				} else {
					flush(t)
				}
			}
			flush(T)
		}
		return
	}
	items, cost, bad := run(true)
	_, base, _ := run(false)
	return Plan{Items: items, CostINR: round2(cost), BaselineINR: round2(base), Vehicles: len(vehicles), Infeasible: bad}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
