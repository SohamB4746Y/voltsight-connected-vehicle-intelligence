package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// requestErasure records a right-to-erasure request for a data subject (a driver) of the caller's organisation.
// The privacy worker executes it and writes the verification evidence.
func (s *Server) requestErasure(w http.ResponseWriter, r *http.Request, p *Principal) {
	var body struct {
		SubjectID string `json:"subject_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	sub, err := uuid.Parse(body.SubjectID)
	if err != nil {
		writeProblem(w, 400, "subject_id must be a UUID")
		return
	}
	var id uuid.UUID
	err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM driver WHERE tenant_id = $1 AND id = $2)`, p.TenantID, sub).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return pgx.ErrNoRows
		}
		return tx.QueryRow(r.Context(), `INSERT INTO erasure_request (tenant_id, subject_id, requested_by) VALUES ($1, $2, $3) RETURNING id`, p.TenantID, sub, p.UserID).Scan(&id)
	})
	if err == pgx.ErrNoRows {
		notFound(w)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"id": id, "status": "pending"})
}

func (s *Server) getErasure(w http.ResponseWriter, r *http.Request, p *Principal) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		notFound(w)
		return
	}
	var status string
	var requested time.Time
	var completed *time.Time
	var ver []byte
	err = s.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT status, requested_at, completed_at, verification FROM erasure_request WHERE tenant_id = $1 AND id = $2`, p.TenantID, id).Scan(&status, &requested, &completed, &ver)
	})
	if err == pgx.ErrNoRows {
		notFound(w)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	out := map[string]any{"id": id, "status": status, "requested_at": requested, "completed_at": completed}
	if len(ver) > 0 {
		out["verification"] = rawJSON(ver)
	}
	writeJSON(w, 200, out)
}

func rawJSON(b []byte) json.RawMessage { return json.RawMessage(b) }
