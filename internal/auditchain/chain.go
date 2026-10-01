// Package auditchain makes the audit log tamper-evident. The audit writers (API, services) only INSERT rows;
// a separate sealer role, which can read the log and write nothing but the chain columns, links every tenant's rows
// into a hash chain: hash(n) = SHA-256(hash(n-1) || canonical(row n)). Changing, deleting or reordering any sealed
// row breaks every later hash, which Verify detects.
package auditchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type row struct {
	id                                                                int64
	ts                                                                time.Time
	actor, actorType, action, resType, resID, reqID, details, subject string
}

const cols = `id, ts, actor, actor_type, action, resource_type, COALESCE(resource_id,''), COALESCE(request_id,''), details::text, COALESCE(subject_id::text,'')`

func (r *row) scan(rs pgx.Rows) error {
	return rs.Scan(&r.id, &r.ts, &r.actor, &r.actorType, &r.action, &r.resType, &r.resID, &r.reqID, &r.details, &r.subject)
}

func canonical(tenant uuid.UUID, r row) []byte {
	return []byte(fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s", r.id, r.ts.UTC().Format(time.RFC3339Nano), tenant, r.actor, r.actorType, r.action,
		r.resType, r.resID, r.reqID, r.details, r.subject))
}

func link(prev []byte, tenant uuid.UUID, r row) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write(canonical(tenant, r))
	return h.Sum(nil)
}

// Seal links all not yet sealed rows of every tenant (or of one tenant if only != uuid.Nil) and returns how many
// rows were sealed.
func Seal(ctx context.Context, pool *pgxpool.Pool, only uuid.UUID) (int, error) {
	q := `SELECT DISTINCT tenant_id FROM audit_log WHERE seq IS NULL`
	var tenants []uuid.UUID
	rs, err := pool.Query(ctx, q)
	if err != nil {
		return 0, err
	}
	for rs.Next() {
		var t uuid.UUID
		if err := rs.Scan(&t); err != nil {
			rs.Close()
			return 0, err
		}
		if only == uuid.Nil || only == t {
			tenants = append(tenants, t)
		}
	}
	rs.Close()
	total := 0
	for _, t := range tenants {
		n, err := sealTenant(ctx, pool, t)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func sealTenant(ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// one sealer per tenant at a time
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenant.String()); err != nil {
		return 0, err
	}
	var seq int64
	var prev []byte
	err = tx.QueryRow(ctx, `SELECT seq, hash FROM audit_log WHERE tenant_id = $1 AND seq IS NOT NULL ORDER BY seq DESC LIMIT 1`, tenant).Scan(&seq, &prev)
	if err != nil && err != pgx.ErrNoRows {
		return 0, err
	}
	rs, err := tx.Query(ctx, `SELECT `+cols+` FROM audit_log WHERE tenant_id = $1 AND seq IS NULL ORDER BY id`, tenant)
	if err != nil {
		return 0, err
	}
	var rows []row
	for rs.Next() {
		var r row
		if err := r.scan(rs); err != nil {
			rs.Close()
			return 0, err
		}
		rows = append(rows, r)
	}
	rs.Close()
	for _, r := range rows {
		seq++
		h := link(prev, tenant, r)
		if _, err := tx.Exec(ctx, `UPDATE audit_log SET seq = $1, prev_hash = $2, hash = $3 WHERE id = $4 AND ts = $5`, seq, prev, h, r.id, r.ts); err != nil {
			return 0, err
		}
		prev = h
	}
	return len(rows), tx.Commit(ctx)
}

// Report is the result of a verification.
type Report struct {
	Tenant   uuid.UUID
	Rows     int
	OK       bool
	BrokenAt int64 // seq of the first row that fails (0 if OK)
	Reason   string
}

// Verify recomputes a tenant's chain from the first sealed row.
func Verify(ctx context.Context, pool *pgxpool.Pool, tenant uuid.UUID) (Report, error) {
	rep := Report{Tenant: tenant, OK: true}
	rs, err := pool.Query(ctx, `SELECT seq, prev_hash, hash, `+cols+` FROM audit_log WHERE tenant_id = $1 AND seq IS NOT NULL ORDER BY seq`, tenant)
	if err != nil {
		return rep, err
	}
	defer rs.Close()
	var prev []byte
	var want int64 = 1
	for rs.Next() {
		var seq int64
		var ph, h []byte
		var r row
		if err := rs.Scan(&seq, &ph, &h, &r.id, &r.ts, &r.actor, &r.actorType, &r.action, &r.resType, &r.resID, &r.reqID, &r.details, &r.subject); err != nil {
			return rep, err
		}
		rep.Rows++
		fail := func(why string) (Report, error) {
			rep.OK, rep.BrokenAt, rep.Reason = false, seq, why
			return rep, nil
		}
		switch {
		case seq != want:
			return fail(fmt.Sprintf("sequence gap: expected %d, found %d (a row was deleted or inserted)", want, seq))
		case !bytes.Equal(ph, prev):
			return fail("prev_hash does not match the previous row")
		case !bytes.Equal(h, link(prev, tenant, r)):
			return fail("row content does not match its hash (modified)")
		}
		prev = h
		want++
	}
	return rep, rs.Err()
}
