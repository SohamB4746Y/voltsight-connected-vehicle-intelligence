package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Stub is the deterministic provider: it routes a question to tools by intent and composes the answer from the
// tool results with citations. It needs no network and no API key, which makes the whole agent loop testable in
// CI, and it keeps working when an LLM provider is down.
type Stub struct{}

// Name implements Provider.
func (Stub) Name() string { return "stub-rules-v1" }

var vinRe = regexp.MustCompile(`\b[A-HJ-NPR-Z0-9]{17}\b`)

func has(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func call(i int, name string, args any) ToolCall {
	b, _ := json.Marshal(args)
	return ToolCall{ID: fmt.Sprintf("c%d", i), Name: name, Args: b}
}

// Next implements Provider.
func (Stub) Next(_ context.Context, _ string, hist []Turn) (Step, error) {
	// the current question = the last user turn; results = tool results produced after it
	ui := -1
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role == "user" {
			ui = i
			break
		}
	}
	if ui < 0 {
		return Step{Answer: "Ask me about at-risk vehicles, alerts, chargers or charging plans."}, nil
	}
	msg := strings.ToLower(hist[ui].Text)
	var results []ToolResult
	for _, t := range hist[ui+1:] {
		results = append(results, t.Results...)
	}
	vin := vinRe.FindString(strings.ToUpper(hist[ui].Text))

	if len(results) == 0 {
		switch {
		case has(msg, "plan", "schedule", "tonight", "charging cost", "cheapest"):
			return Step{Calls: []ToolCall{call(1, "propose_charge_plan", map[string]any{})}}, nil
		case has(msg, "similar", "past incident", "happened before", "previous incident", "history of"):
			return Step{Calls: []ToolCall{call(1, "search_similar_incidents", map[string]any{"query": hist[ui].Text, "k": 3})}}, nil
		case vin != "":
			cs := []ToolCall{call(1, "get_vehicle_state", map[string]any{"vin": vin})}
			if has(msg, "why", "evidence", "reach", "charger", "risk", "alert") {
				cs = append(cs, call(2, "get_alert_evidence", map[string]any{"vin": vin}), call(3, "find_reachable_chargers", map[string]any{"vin": vin}))
			}
			return Step{Calls: cs}, nil
		case has(msg, "why", "explain", "evidence", "most critical"):
			return Step{Calls: []ToolCall{call(1, "list_at_risk_vehicles", map[string]any{"limit": 1, "min_severity": "CRITICAL"})}}, nil
		case has(msg, "risk", "critical", "strand", "alert", "low battery", "range"):
			return Step{Calls: []ToolCall{call(1, "list_at_risk_vehicles", map[string]any{"limit": 10})}}, nil
		case has(msg, "telemetry", "average", "fleet soc", "summary", "how many"):
			return Step{Calls: []ToolCall{call(2, "query_telemetry_summary", map[string]any{"minutes": 30})}}, nil
		}
		return Step{Answer: "I can list at-risk vehicles, explain an alert, find chargers within reach, summarise telemetry, search similar past incidents or propose a charging plan. Which would you like?"}, nil
	}

	// second round for "why is the most critical vehicle at risk": explain the top vehicle
	if len(results) == 1 && results[0].Name == "list_at_risk_vehicles" && has(msg, "why", "explain", "evidence", "most critical") {
		if v := firstVehicle(results[0]); v != "" {
			return Step{Calls: []ToolCall{call(2, "get_alert_evidence", map[string]any{"vin": v}), call(3, "find_reachable_chargers", map[string]any{"vin": v})}}, nil
		}
	}
	return Step{Answer: compose(results)}, nil
}

func asMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func firstVehicle(r ToolResult) string {
	vs, _ := asMap(r.Data)["vehicles"].([]any)
	if len(vs) == 0 {
		return ""
	}
	m, _ := vs[0].(map[string]any)
	s, _ := m["vin"].(string)
	return s
}

func num(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%.1f", v)
	case nil:
		return "n/a"
	default:
		return fmt.Sprint(v)
	}
}

func ref(r ToolResult, i int) string {
	if i < len(r.Refs) {
		return " [" + r.Refs[i] + "]"
	}
	return ""
}

// compose writes the answer strictly from tool results.
func compose(results []ToolResult) string {
	var parts []string
	for _, r := range results {
		if !r.OK {
			parts = append(parts, fmt.Sprintf("I could not use %s: %s.", r.Name, r.Error))
			continue
		}
		d := asMap(r.Data)
		switch r.Name {
		case "list_at_risk_vehicles":
			vs, _ := d["vehicles"].([]any)
			if len(vs) == 0 {
				parts = append(parts, "No vehicle currently has an open range-risk alert.")
				continue
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%d vehicle(s) with open range-risk alerts:", len(vs))
			for i, x := range vs {
				m := x.(map[string]any)
				fmt.Fprintf(&sb, "\n• %s — %s, SoC %s%%, usable range %s km, nearest available charger %s km%s", m["vin"], m["severity"], num(m, "soc_pct"), num(m, "usable_range_km"), num(m, "distance_to_charger_km"), ref(r, i))
			}
			parts = append(parts, sb.String())
		case "get_vehicle_state":
			st, _ := d["state"].(map[string]any)
			if st == nil {
				parts = append(parts, fmt.Sprintf("%s (%s) has %s open alert(s) but no live telemetry.%s", d["vin"], d["model"], num(d, "open_alerts"), ref(r, 0)))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s (%s): SoC %s%%, speed %s km/h, %s, %s open alert(s).%s", d["vin"], d["model"], num(st, "soc_pct"), num(st, "speed_kmh"), strings.ToLower(fmt.Sprint(st["charge_state"])), num(d, "open_alerts"), ref(r, 0)))
		case "get_alert_evidence":
			ev, _ := d["evidence"].(map[string]any)
			reach := "no available charger is reachable"
			if b, _ := ev["reachable_charger"].(bool); b {
				reach = fmt.Sprintf("the nearest available charger is %s km away", num(ev, "distance_to_charger_km"))
			}
			parts = append(parts, fmt.Sprintf("%s alert for %s (%s): SoC %s%%; estimated range %s km (%v), %s km usable after the %s safety margin, but %s; %s of %s chargers in the city are available.%s",
				d["severity"], d["vin"], d["status"], num(ev, "soc_pct"), num(ev, "estimated_range_km"), ev["estimator"], num(ev, "usable_range_km"),
				fmt.Sprintf("%.0f%%", 100*floatOf(ev["safety_margin"])), reach, num(ev, "city_chargers_available"), num(ev, "city_chargers_total"), ref(r, 0)))
		case "find_reachable_chargers":
			cs, _ := d["chargers_in_service_nearby"].([]any)
			if len(cs) == 0 {
				parts = append(parts, fmt.Sprintf("No in-service charger near %v (baseline usable range %s km); %s nearby chargers are out of service.%s", d["vin"], num(d, "baseline_usable_range_km"), num(d, "chargers_out_of_service_nearby"), ref(r, 0)))
				continue
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Nearest in-service chargers for %v (baseline usable range %s km; %s nearby are out of service):", d["vin"], num(d, "baseline_usable_range_km"), num(d, "chargers_out_of_service_nearby"))
			for i, c := range cs {
				m := c.(map[string]any)
				fmt.Fprintf(&sb, "\n• %v — %s km straight line, %s kW, %s%s", m["name"], num(m, "straight_line_km"), num(m, "power_kw"), map[bool]string{true: "reachable", false: "NOT reachable with margin"}[m["reachable_with_margin"] == true], ref(r, i+1))
			}
			parts = append(parts, sb.String())
		case "query_telemetry_summary":
			parts = append(parts, fmt.Sprintf("Last %s min: %s samples from %s vehicle(s); SoC avg %s%% (min %s, max %s); distance driven %s km.%s", num(d, "minutes"), num(d, "samples"), num(d, "vehicles"), num(d, "avg_soc_pct"), num(d, "min_soc_pct"), num(d, "max_soc_pct"), num(d, "km_driven"), ref(r, 0)))
		case "search_similar_incidents":
			ms, _ := d["matches"].([]any)
			if len(ms) == 0 {
				parts = append(parts, "No similar past incidents found.")
				continue
			}
			var sb strings.Builder
			sb.WriteString("Most similar past incidents (quoted as data):")
			for i, x := range ms {
				m := x.(map[string]any)
				fmt.Fprintf(&sb, "\n• (%s, similarity %s) “%v”%s", m["kind"], num(m, "similarity"), m["text"], ref(r, i))
			}
			parts = append(parts, sb.String())
		case "propose_charge_plan", "submit_charge_plan":
			if p, ok := d["plan"].(map[string]any); ok {
				parts = append(parts, fmt.Sprintf("I proposed a plan for %s vehicle(s): ₹%s versus ₹%s if charged on plug-in (%s%% saved).%s It is NOT scheduled: please approve it below if you agree.", num(p, "vehicles_planned"), num(p, "cost_estimate_inr"), num(p, "baseline_cost_estimate_inr"), num(p, "savings_pct"), ref(r, 0)))
			} else {
				parts = append(parts, "I asked for approval of the plan.\nIt will be scheduled only when you approve it."+ref(r, 0))
			}
		}
	}
	if len(parts) == 0 {
		return "Insufficient evidence."
	}
	return strings.Join(parts, "\n\n")
}

func floatOf(v any) float64 { f, _ := v.(float64); return f }
