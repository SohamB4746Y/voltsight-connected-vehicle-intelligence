package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type alertRow struct {
	ID         uuid.UUID       `json:"id"`
	VIN        string          `json:"vin"`
	Rule       string          `json:"rule"`
	Severity   string          `json:"severity"`
	Status     string          `json:"status"`
	WindowFrom time.Time       `json:"window_start"`
	DetectedAt time.Time       `json:"detected_at"`
	Evidence   json.RawMessage `json:"evidence,omitempty"`
}

// queryAlerts runs the alert list query with keyset pagination. extra is an additional "AND ..." predicate whose
// placeholders start at $2; cursor (detected_at, id) selects the next page.
func queryAlerts(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, extra string, args []any, limit int, cur *alertCursor) ([]alertRow, error) {
	params := append([]any{tenant}, args...)
	q := `SELECT id, vin, rule, severity, status, window_start, detected_at, evidence FROM alert WHERE tenant_id = $1 ` + extra
	if cur != nil {
		params = append(params, cur.At, cur.ID)
		q += fmt.Sprintf(` AND (detected_at, id) < ($%d, $%d)`, len(params)-1, len(params))
	}
	params = append(params, limit)
	q += fmt.Sprintf(` ORDER BY detected_at DESC, id DESC LIMIT $%d`, len(params))
	rs, err := tx.Query(ctx, q, params...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := []alertRow{}
	for rs.Next() {
		var a alertRow
		if err := rs.Scan(&a.ID, &a.VIN, &a.Rule, &a.Severity, &a.Status, &a.WindowFrom, &a.DetectedAt, &a.Evidence); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rs.Err()
}

type alertCursor struct {
	At time.Time
	ID uuid.UUID
}

func encodeCursor(c alertCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", c.At.UnixMicro(), c.ID)))
}

func decodeCursor(s string) (*alertCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var us int64
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("bad cursor")
	}
	if _, err := fmt.Sscanf(parts[0], "%d", &us); err != nil {
		return nil, err
	}
	u, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, err
	}
	return &alertCursor{At: time.UnixMicro(us).UTC(), ID: u}, nil
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request, p *Principal) {
	q := r.URL.Query()
	limit := intParam(r, "limit", 50, 1, 200)
	extra, args := "", []any{}
	add := func(col, val string, allowed ...string) bool {
		if val == "" {
			return true
		}
		ok := false
		for _, a := range allowed {
			ok = ok || a == val
		}
		if !ok {
			return false
		}
		args = append(args, val)
		extra += fmt.Sprintf(" AND %s = $%d", col, len(args)+1)
		return true
	}
	if !add("status", q.Get("status"), "open", "acknowledged", "resolved") || !add("severity", q.Get("severity"), "INFO", "WARNING", "CRITICAL") ||
		!add("rule", q.Get("rule"), "RANGE_CRITICAL", "RANGE_LOW", "CHARGER_UNAVAILABLE_ON_ROUTE", "UNAPPROVED_DEPOT_CLUSTER", "SOH_DROP", "THERMAL_FAULT_DTC") {
		writeProblem(w, 400, "invalid filter value")
		return
	}
	if v := strings.ToUpper(q.Get("vin")); v != "" {
		if len(v) != 17 {
			writeProblem(w, 400, "invalid vin")
			return
		}
		args = append(args, v)
		extra += fmt.Sprintf(" AND vin = $%d", len(args)+1)
	}
	var cur *alertCursor
	if c := q.Get("after"); c != "" {
		var err error
		if cur, err = decodeCursor(c); err != nil {
			writeProblem(w, 400, "invalid cursor")
			return
		}
	}
	var rows []alertRow
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		rows, err = queryAlerts(r.Context(), tx, p.TenantID, extra, args, limit+1, cur)
		return err
	})
	if err != nil {
		serverError(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(alertCursor{At: rows[limit-1].DetectedAt, ID: rows[limit-1].ID})
	}
	for i := range rows {
		rows[i].Evidence = s.maskEvidence(p, rows[i].Evidence)
	}
	writeJSON(w, 200, map[string]any{"items": rows, "next": next})
}

// maskEvidence removes precise coordinates and the route polyline for roles that may not see them.
func (s *Server) maskEvidence(p *Principal, raw json.RawMessage) json.RawMessage {
	if p.Has(PermPreciseGeo) || len(raw) == 0 {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	if lat, ok := m["lat"].(float64); ok {
		if lon, ok2 := m["lon"].(float64); ok2 {
			m["lat"], m["lon"] = maskLocation(p, lat, lon)
		}
	}
	delete(m, "route")
	b, _ := json.Marshal(m)
	return b
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request, p *Principal) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return
	}
	var a alertRow
	type event struct {
		Actor  uuid.UUID `json:"actor"`
		Action string    `json:"action"`
		At     time.Time `json:"at"`
	}
	events := []event{}
	err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT id, vin, rule, severity, status, window_start, detected_at, evidence FROM alert WHERE tenant_id = $1 AND id = $2`,
			p.TenantID, id).Scan(&a.ID, &a.VIN, &a.Rule, &a.Severity, &a.Status, &a.WindowFrom, &a.DetectedAt, &a.Evidence); err != nil {
			return err
		}
		rs, err := tx.Query(r.Context(), `SELECT actor, action, at FROM alert_event WHERE tenant_id = $1 AND alert_id = $2 ORDER BY at`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var e event
			if err := rs.Scan(&e.Actor, &e.Action, &e.At); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rs.Err()
	})
	if err == pgx.ErrNoRows {
		notFound(w)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	a.Evidence = s.maskEvidence(p, a.Evidence)
	writeJSON(w, 200, map[string]any{"alert": a, "events": events})
}

// alertAction acknowledges or resolves an alert: the status change and its event row commit together.
func (s *Server) alertAction(action, status string) handler {
	return func(w http.ResponseWriter, r *http.Request, p *Principal) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			notFound(w)
			return
		}
		err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(r.Context(), `UPDATE alert SET status = $3 WHERE tenant_id = $1 AND id = $2`, p.TenantID, id, status)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return pgx.ErrNoRows
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO alert_event (tenant_id, alert_id, actor, action) VALUES ($1, $2, $3, $4)`, p.TenantID, id, p.UserID, action)
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
		writeJSON(w, 200, map[string]any{"id": id, "status": status})
	}
}

// alertStream streams new alerts of the caller's tenant (Server-Sent Events) from the alert service's Redis fan-out.
func (s *Server) alertStream(w http.ResponseWriter, r *http.Request, p *Principal) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, 500, "streaming unsupported")
		return
	}
	sub := s.cfg.Redis.Subscribe(r.Context(), "alerts:"+p.TenantID.String())
	defer sub.Close()
	if _, err := sub.Receive(r.Context()); err != nil {
		writeProblem(w, 503, "stream unavailable")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ch := sub.Channel()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case m, ok := <-ch:
			if !ok {
				return
			}
			payload := m.Payload
			if !p.Has(PermPreciseGeo) {
				var a map[string]json.RawMessage
				if json.Unmarshal([]byte(payload), &a) == nil {
					a["evidence"] = s.maskEvidence(p, a["evidence"])
					b, _ := json.Marshal(a)
					payload = string(b)
				}
			}
			fmt.Fprintf(w, "event: alert\ndata: %s\n\n", payload)
			fl.Flush()
		}
	}
}
