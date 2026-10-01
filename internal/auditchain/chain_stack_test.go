//go:build stack

package auditchain

import (
	"context"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
)

func pools(t *testing.T) (owner, sealer *pgxpool.Pool) {
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	dsn, _ := dbtool.OwnerDSN(env)
	owner, err = pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_sealer", dotenv.Get(env, "DB_SEALER_PASSWORD"))
	sealer, err = pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(); sealer.Close() })
	return
}

// G12: the chain verifies, and any modification, deletion or injected row is detected.
func TestChainDetectsTampering(t *testing.T) {
	ctx := context.Background()
	owner, sealer := pools(t)
	tenant := uuid.New() // audit_log has no FK to tenant: an isolated chain
	t.Cleanup(func() { _, _ = owner.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenant) })
	for i := 0; i < 20; i++ {
		if _, err := owner.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, actor_type, action, resource_type, resource_id, details) VALUES ($1,'u1','user','vehicle.read','vehicle',$2,'{"status":200}')`, tenant, uuid.NewString()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := Seal(ctx, sealer, tenant); err != nil || n != 20 {
		t.Fatalf("sealed %d: %v", n, err)
	}
	if r, err := Verify(ctx, sealer, tenant); err != nil || !r.OK || r.Rows != 20 {
		t.Fatalf("fresh chain: %+v %v", r, err)
	}
	// incremental sealing continues the chain
	_, _ = owner.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, actor_type, action, resource_type) VALUES ($1,'u1','user','alert.ack','alert')`, tenant)
	if n, _ := Seal(ctx, sealer, tenant); n != 1 {
		t.Fatalf("incremental seal: %d", n)
	}
	if r, _ := Verify(ctx, sealer, tenant); !r.OK || r.Rows != 21 {
		t.Fatalf("chain after incremental seal: %+v", r)
	}

	// the sealer role cannot rewrite history: it may only set the chain columns
	if _, err := sealer.Exec(ctx, `UPDATE audit_log SET action = 'x' WHERE tenant_id = $1`, tenant); err == nil {
		t.Fatal("sealer role could modify audit content")
	}
	if _, err := sealer.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1`, tenant); err == nil {
		t.Fatal("sealer role could delete audit rows")
	}

	// tamper (as the table owner, i.e. someone with database access): change one row
	if _, err := owner.Exec(ctx, `UPDATE audit_log SET details = '{"status":403}' WHERE tenant_id = $1 AND seq = 7`, tenant); err != nil {
		t.Fatal(err)
	}
	if r, _ := Verify(ctx, sealer, tenant); r.OK || r.BrokenAt != 7 {
		t.Fatalf("modification not detected at seq 7: %+v", r)
	}
	if _, err := owner.Exec(ctx, `UPDATE audit_log SET details = '{"status":200}' WHERE tenant_id = $1 AND seq = 7`, tenant); err != nil {
		t.Fatal(err)
	}
	if r, _ := Verify(ctx, sealer, tenant); !r.OK {
		t.Fatalf("restored chain should verify: %+v", r)
	}
	// delete a middle row
	if _, err := owner.Exec(ctx, `DELETE FROM audit_log WHERE tenant_id = $1 AND seq = 12`, tenant); err != nil {
		t.Fatal(err)
	}
	if r, _ := Verify(ctx, sealer, tenant); r.OK || r.BrokenAt != 13 {
		t.Fatalf("deletion not detected: %+v", r)
	}
}
