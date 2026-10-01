// Package privacy executes right-to-erasure requests (GDPR Art. 17, DPDP Act s.12) and proves the result.
//
// Personal data about a driver lives in exactly one place by design: the encrypted `driver.pii_enc` column and
// the pseudonym. Telemetry, trips, alerts and audit records reference only the driver's UUID or the vehicle, never
// a name. Erasure therefore (1) destroys the ciphertext and replaces the pseudonym (the vehicle-assignment
// history stays, now pointing at an anonymous record), (2) deletes every vector-store narrative tagged with the
// subject, and (3) re-queries each store and records the evidence in erasure_request.verification.
package privacy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessPending executes all pending requests and returns how many were completed.
func ProcessPending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	rs, err := pool.Query(ctx, `SELECT id, tenant_id, subject_id FROM erasure_request WHERE status = 'pending' ORDER BY requested_at LIMIT 100`)
	if err != nil {
		return 0, err
	}
	type req struct{ id, tenant, subject uuid.UUID }
	var reqs []req
	for rs.Next() {
		var r req
		if err := rs.Scan(&r.id, &r.tenant, &r.subject); err != nil {
			rs.Close()
			return 0, err
		}
		reqs = append(reqs, r)
	}
	rs.Close()
	done := 0
	for _, r := range reqs {
		if err := execute(ctx, pool, r.id, r.tenant, r.subject); err != nil {
			_, _ = pool.Exec(ctx, `UPDATE erasure_request SET status = 'failed', verification = $2 WHERE id = $1`, r.id, fmt.Sprintf(`{"error":%q}`, err.Error()))
			continue
		}
		done++
	}
	return done, nil
}

func execute(ctx context.Context, pool *pgxpool.Pool, id, tenant, subject uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE erasure_request SET status = 'running' WHERE id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE driver SET pii_enc = NULL, erased_at = now(), pseudonym = 'erased-' || left(id::text, 8)
		WHERE tenant_id = $1 AND id = $2`, tenant, subject)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("subject not found in tenant")
	}
	delTag, err := tx.Exec(ctx, `DELETE FROM incident_embedding WHERE tenant_id = $1 AND meta ->> 'subject_id' = $2`, tenant, subject.String())
	if err != nil {
		return err
	}

	// verification: re-read every place that could hold the subject's data
	ver := map[string]any{"verified_at": "now", "incident_narratives_deleted": delTag.RowsAffected()}
	var piiLeft, pseudoLeft, vecLeft int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pii_enc IS NOT NULL), count(*) FILTER (WHERE pseudonym NOT LIKE 'erased-%') FROM driver WHERE tenant_id = $1 AND id = $2`, tenant, subject).Scan(&piiLeft, &pseudoLeft); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM incident_embedding WHERE tenant_id = $1 AND meta ->> 'subject_id' = $2`, tenant, subject.String()).Scan(&vecLeft); err != nil {
		return err
	}
	var assignments int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicle_driver_assignment WHERE tenant_id = $1 AND driver_id = $2`, tenant, subject).Scan(&assignments); err != nil {
		return err
	}
	ver["driver_pii_ciphertext_remaining"], ver["driver_original_pseudonym_remaining"], ver["vector_rows_remaining"] = piiLeft, pseudoLeft, vecLeft
	ver["assignment_rows_kept_anonymised"] = assignments
	ver["stores_without_personal_data_by_design"] = []string{"clickhouse.telemetry_raw (VIN only)", "redis vehicle state (VIN only)", "kafka topics (VIN only, 72 h retention)", "parquet archive (VIN only)"}
	if piiLeft != 0 || pseudoLeft != 0 || vecLeft != 0 {
		return fmt.Errorf("verification failed: %v", ver)
	}
	b, _ := json.Marshal(ver)
	if _, err := tx.Exec(ctx, `UPDATE erasure_request SET status = 'completed', completed_at = now(), verification = $2 WHERE id = $1`, id, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ = pgx.ErrNoRows
