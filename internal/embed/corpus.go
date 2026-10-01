package embed

import "fmt"

// Incident is a synthetic resolved-incident narrative. Group identifies the underlying situation: narratives of
// one group are paraphrases of each other (the retrieval ground truth).
type Incident struct {
	Group int
	Kind  string
	Text  string
}

var situations = []struct {
	kind  string
	forms []string
}{
	{"stranding", []string{
		"Delivery van ran out of battery two kilometres from the nearest charger after a regional charger outage; driver ignored the low battery warning; recovered by tow.",
		"Vehicle stranded with an empty pack because most public chargers were out of service and the driver did not heed the range warning; flatbed recovery was needed.",
		"Van stopped with zero charge after the charging network failed in the district, the dispatcher warning was ignored and a tow truck brought it in.",
	}},
	{"charger_outage", []string{
		"Depot chargers went offline after a breaker trip at night so vehicles could not recharge before the morning shift; electrician reset the breaker.",
		"Overnight outage of all depot charging points caused by a tripped breaker left the fleet undercharged for the shift start; resolved after resetting the breaker.",
		"Several depot chargers lost power at night, vehicles were not charged for the first trips, the breaker was reset by maintenance.",
	}},
	{"battery_health", []string{
		"Battery capacity fell faster than expected on an older van, range per charge dropped by about a fifth, pack inspected and cells rebalanced.",
		"State of health of an ageing vehicle declined sharply with shorter range per charge; the pack was checked and cell balancing performed.",
		"Older vehicle showed rapid battery degradation and reduced range, technicians rebalanced the cells in the pack.",
	}},
	{"thermal", []string{
		"Pack temperature warning during fast charging in extreme heat, charging throttled and the vehicle cooled before continuing; coolant pump later replaced.",
		"High battery temperature alert while charging on a hot afternoon; charge power was reduced, the car cooled down and the coolant pump was replaced.",
		"Thermal warning at a fast charger in the heat led to throttled charging, cooling pause and a coolant pump replacement.",
	}},
	{"tariff", []string{
		"Charging during the evening peak tariff doubled the energy cost for the depot; schedule moved to the cheap night band, saving about forty percent.",
		"Vehicles plugged in at peak evening prices made charging expensive, the plan was shifted to the low night tariff and costs dropped by roughly forty percent.",
		"Expensive evening peak charging was rescheduled to the cheaper overnight band, cutting the bill by about forty percent.",
	}},
	{"unapproved_stop", []string{
		"Several vehicles repeatedly parked for long periods at an unapproved yard outside the depot; operations were asked to confirm the location.",
		"Cluster of long stops at an unregistered site away from the depots was detected, operations reviewed and approved or blocked the location.",
		"Repeated extended parking of the fleet at a location that is not an approved depot was flagged for review by operations.",
	}},
	{"gps", []string{
		"Position jumped hundreds of metres for one vehicle because of GPS multipath between tall buildings; the sample was discarded as noise.",
		"A vehicle reported a sudden location jump of several hundred metres caused by GPS multipath in the city canyon, treated as a sensor glitch.",
		"Tall buildings caused GPS multipath and a spurious position jump for a van, the reading was ignored as noise.",
	}},
	{"offline", []string{
		"Telematics unit went silent after a firmware update and buffered data for two hours before flushing a burst when connectivity returned.",
		"After an over the air update the connected device stopped reporting, stored readings and sent them all at once when the network recovered.",
		"Vehicle stopped sending telemetry after software update, buffered two hours of data and uploaded it in a burst after reconnecting.",
	}},
}

// Corpus returns all narratives (len(situations) * paraphrases) with group labels.
func Corpus() []Incident {
	var out []Incident
	for g, s := range situations {
		for _, f := range s.forms {
			out = append(out, Incident{Group: g, Kind: s.kind, Text: f})
		}
	}
	return out
}

// Describe names a group for reports.
func Describe(g int) string { return fmt.Sprintf("%d:%s", g, situations[g].kind) }
