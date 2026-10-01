package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type auditRow struct {
	ID         int64           `json:"id"`
	TS         time.Time       `json:"ts"`
	Actor      string          `json:"actor"`
	ActorType  string          `json:"actor_type"`
	Action     string          `json:"action"`
	Resource   string          `json:"resource_type"`
	ResourceID *string         `json:"resource_id,omitempty"`
	RequestID  *string         `json:"request_id,omitempty"`
	Details    json.RawMessage `json:"details"`
}

// listAudit pages through the tenant's audit trail (newest first, keyset on id).
func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, p *Principal) {
	limit := intParam(r, "limit", 100, 1, 500)
	before := int64(1) << 62
	if v := intParam(r, "before", 0, 0, 1<<30); v > 0 {
		before = int64(v)
	}
	if v := r.URL.Query().Get("before"); v != "" {
		var n int64
		if _, err := fmtSscanInt(v, &n); err == nil && n > 0 {
			before = n
		}
	}
	actor := r.URL.Query().Get("actor")
	action := r.URL.Query().Get("action")
	var rows []auditRow
	err := s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rs, err := tx.Query(r.Context(), `SELECT id, ts, actor, actor_type, action, resource_type, resource_id, request_id, details FROM audit_log
			WHERE tenant_id = $1 AND id < $2 AND ($3 = '' OR actor = $3) AND ($4 = '' OR action = $4) ORDER BY id DESC LIMIT $5`,
			p.TenantID, before, actor, action, limit)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var a auditRow
			if err := rs.Scan(&a.ID, &a.TS, &a.Actor, &a.ActorType, &a.Action, &a.Resource, &a.ResourceID, &a.RequestID, &a.Details); err != nil {
				return err
			}
			rows = append(rows, a)
		}
		return rs.Err()
	})
	if err != nil {
		serverError(w, err)
		return
	}
	next := int64(0)
	if len(rows) == limit {
		next = rows[len(rows)-1].ID
	}
	writeJSON(w, 200, map[string]any{"items": rows, "next_before": next})
}

// opsPipeline reports platform health for engineers: real-time state size, alert rate, charger outages.
func (s *Server) opsPipeline(w http.ResponseWriter, r *http.Request, _ *Principal) {
	out := map[string]any{}
	if n, err := s.cfg.Redis.DBSize(r.Context()).Result(); err == nil {
		out["redis_keys"] = n
	}
	if h, err := s.cfg.Redis.HGetAll(r.Context(), chargerStatusHash).Result(); err == nil {
		oos := 0
		for _, v := range h {
			if v == "OUT_OF_SERVICE" {
				oos++
			}
		}
		out["chargers_out_of_service"], out["chargers_with_status"] = oos, len(h)
	}
	if s.cfg.CH != nil {
		var n uint64
		if err := s.cfg.CH.QueryRow(r.Context(), `SELECT count() FROM telemetry_raw WHERE ts > now() - INTERVAL 60 SECOND`).Scan(&n); err == nil {
			out["telemetry_rows_last_minute"] = n
		}
	}
	out["server_time"] = time.Now().UTC()
	writeJSON(w, 200, out)
}

var _ = uuid.Nil
