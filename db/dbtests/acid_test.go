//go:build integration

package dbtests

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// G1.6: transactional integrity and the constraints that carry business rules.

func pgCode(err error) string {
	if pe, ok := err.(*pgconn.PgError); ok {
		return pe.Code
	}
	return ""
}

func TestACID_TransactionRollsBackAtomically(t *testing.T) {
	ctx := context.Background()
	fleetID := uuid.New()
	tx, err := ownerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fleet (id,tenant_id,name) VALUES ($1,$2,'atomic-fleet')`, fleetID, tenantA.ID); err != nil {
		t.Fatal(err)
	}
	// second statement violates the VIN check constraint -> whole transaction must fail
	_, err = tx.Exec(ctx, `INSERT INTO vehicle (vin,tenant_id,fleet_id,model_id,commissioned_at) VALUES ('1HGCM82633A004353',$1,$2,1,'2025-01-01')`, tenantA.ID, fleetID)
	if pgCode(err) != "23514" { // check_violation
		t.Fatalf("invalid VIN should raise check_violation, got %v", err)
	}
	_ = tx.Rollback(ctx)
	if n := ownerCount(t, `SELECT count(*) FROM fleet WHERE id=$1`, fleetID); n != 0 {
		t.Fatalf("partial work survived the rollback: %d fleet rows", n)
	}
}

func TestACID_AlertIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	const ins = `INSERT INTO alert (tenant_id,vin,rule,severity,window_start,detected_at)
	             VALUES ($1,$2,'RANGE_LOW','WARNING','2026-02-02T10:00:00Z',now())`
	if _, err := ownerPool.Exec(ctx, ins, tenantA.ID, tenantA.VIN); err != nil {
		t.Fatal(err)
	}
	_, err := ownerPool.Exec(ctx, ins, tenantA.ID, tenantA.VIN)
	if pgCode(err) != "23505" {
		t.Fatalf("duplicate (vin, rule, window_start) must violate uniqueness, got %v", err)
	}
	tag, err := ownerPool.Exec(ctx, ins+` ON CONFLICT (vin, rule, window_start) DO NOTHING`, tenantA.ID, tenantA.VIN)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("replayed alert must be absorbed by ON CONFLICT DO NOTHING: rows=%d err=%v", tag.RowsAffected(), err)
	}
	if n := ownerCount(t, `SELECT count(*) FROM alert WHERE vin=$1 AND rule='RANGE_LOW'`, tenantA.VIN); n != 1 {
		t.Fatalf("exactly one alert expected after replays, got %d", n)
	}
}

func TestACID_CompositeForeignKeysBlockCrossTenantReferences(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		sql  string
		args []any
	}{
		"vehicle -> other tenant's fleet": {
			`INSERT INTO vehicle (vin,tenant_id,fleet_id,model_id,commissioned_at) VALUES ($1,$2,$3,1,'2025-01-01')`,
			[]any{mustVIN("1HGCM826x3A009999"), tenantA.ID, tenantB.Fleet}},
		"assignment -> other tenant's driver": {
			// window in the past so only the foreign key (not the overlap constraint) can reject it
			`INSERT INTO vehicle_driver_assignment (tenant_id,vin,driver_id,valid_from,valid_to) VALUES ($1,$2,$3,'2000-01-01','2001-01-01')`,
			[]any{tenantA.ID, tenantA.VIN, tenantB.Driver}},
		"alert -> other tenant's vehicle": {
			`INSERT INTO alert (tenant_id,vin,rule,severity,window_start,detected_at) VALUES ($1,$2,'SOH_DROP','INFO',now(),now())`,
			[]any{tenantA.ID, tenantB.VIN}},
		"charge plan approved by other tenant's user": {
			`INSERT INTO charge_plan (tenant_id,status,created_by,approved_by,approved_at) VALUES ($1,'approved',$2,$3,now())`,
			[]any{tenantA.ID, tenantA.User, tenantB.User}},
		"depot -> other tenant's fleet": {
			`INSERT INTO depot (id,tenant_id,fleet_id,name,city,lat,lon) VALUES (gen_random_uuid(),$1,$2,'d','c',1,1)`,
			[]any{tenantA.ID, tenantB.Fleet}},
	}
	for name, c := range cases {
		_, err := ownerPool.Exec(ctx, c.sql, c.args...)
		if pgCode(err) != "23503" { // foreign_key_violation
			t.Errorf("%s: expected foreign_key_violation, got %v", name, err)
		}
	}
}

func TestACID_DriverAssignmentsCannotOverlap(t *testing.T) {
	ctx := context.Background()
	// tenant A's vehicle already has an open-ended assignment from 2025-01-01
	_, err := ownerPool.Exec(ctx,
		`INSERT INTO vehicle_driver_assignment (tenant_id,vin,driver_id,valid_from,valid_to) VALUES ($1,$2,$3,'2025-06-01','2025-07-01')`,
		tenantA.ID, tenantA.VIN, tenantA.Driver)
	if pgCode(err) != "23P01" { // exclusion_violation
		t.Fatalf("overlapping assignment must be rejected by the exclusion constraint, got %v", err)
	}
}

func TestACID_BusinessCheckConstraints(t *testing.T) {
	ctx := context.Background()
	bad := map[string]string{
		"soc out of range": `INSERT INTO charge_session (tenant_id,vin,charger_id,started_at,ended_at,soc_start,soc_end,kwh)
			SELECT tenant_id, vin, (SELECT id FROM charger LIMIT 1), now(), now()+interval '1 hour', 120, 80, 1 FROM vehicle LIMIT 1`,
		"session ends before it starts": `INSERT INTO charge_session (tenant_id,vin,charger_id,started_at,ended_at,soc_start,soc_end,kwh)
			SELECT tenant_id, vin, (SELECT id FROM charger LIMIT 1), now(), now()-interval '1 hour', 10, 80, 1 FROM vehicle LIMIT 1`,
		"approved plan without approver": `INSERT INTO charge_plan (tenant_id,status,created_by) SELECT tenant_id, 'approved', id FROM app_user LIMIT 1`,
		"unknown alert rule": `INSERT INTO alert (tenant_id,vin,rule,severity,window_start,detected_at)
			SELECT tenant_id, vin, 'NOT_A_RULE','INFO',now(),now() FROM vehicle LIMIT 1`,
		"erased driver keeps PII": `UPDATE driver SET erased_at = now() WHERE id IN (SELECT id FROM driver LIMIT 1)`,
		"lat out of range":        `INSERT INTO depot (id,tenant_id,fleet_id,name,city,lat,lon) SELECT gen_random_uuid(), tenant_id, id, 'x','y',123,1 FROM fleet LIMIT 1`,
	}
	for name, q := range bad {
		if _, err := ownerPool.Exec(ctx, q); pgCode(err) != "23514" {
			t.Errorf("%s: expected check_violation, got %v", name, err)
		}
	}
}

// Two racing approvals of the same plan: exactly one must win, every time.
func TestACID_ConcurrentPlanApprovalHasExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 100; round++ {
		planID := uuid.New()
		if _, err := ownerPool.Exec(ctx, `INSERT INTO charge_plan (id,tenant_id,created_by) VALUES ($1,$2,$3)`, planID, tenantA.ID, tenantA.User); err != nil {
			t.Fatal(err)
		}
		var winners atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tx, err := ownerPool.Begin(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				<-start
				tag, err := tx.Exec(ctx, `UPDATE charge_plan SET status='approved', approved_by=$2, approved_at=now()
				                          WHERE id=$1 AND status='proposed'`, planID, tenantA.User)
				if err != nil {
					t.Error(err)
					_ = tx.Rollback(ctx)
					return
				}
				if err := tx.Commit(ctx); err != nil {
					t.Error(err)
					return
				}
				if tag.RowsAffected() == 1 {
					winners.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if w := winners.Load(); w != 1 {
			t.Fatalf("round %d: %d approvals succeeded, want exactly 1", round, w)
		}
	}
}

func TestACID_VINFunctionAgreesWithGoImplementation(t *testing.T) {
	ctx := context.Background()
	cases := map[string]bool{
		"1HGCM82633A004352": true, "1M8GDM9AXKP042788": true, "11111111111111111": true,
		"1HGCM82633A004353": false, "1HGCM82I33A004352": false, "SHORT": false,
	}
	for v, want := range cases {
		var got bool
		if err := ownerPool.QueryRow(ctx, `SELECT vin_valid($1)`, v).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("vin_valid(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestACID_AuditPartitionHelper(t *testing.T) {
	ctx := context.Background()
	var created int
	// the migration pre-creates 36 months from 2026-01; the helper must extend beyond that window
	if err := ownerPool.QueryRow(ctx, `SELECT audit_ensure_partitions('2030-01-15', 3)`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 3 {
		t.Fatalf("created %d partitions, want 3", created)
	}
	if err := ownerPool.QueryRow(ctx, `SELECT audit_ensure_partitions('2030-01-15', 3)`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("helper must be idempotent: created=%d err=%v", created, err)
	}
	if _, err := ownerPool.Exec(ctx, `INSERT INTO audit_log (ts,tenant_id,actor,actor_type,action,resource_type) VALUES ('2030-02-02T00:00:00Z',$1,'x','service','probe','probe')`, tenantA.ID); err != nil {
		t.Fatal(err)
	}
	if n := ownerCount(t, `SELECT count(*) FROM audit_log_2030_02 WHERE action='probe'`); n != 1 {
		t.Fatalf("row did not land in the monthly partition (found %d)", n)
	}
	// rows written "now" (fixtures, the app-role inserts) must be covered by the pre-created window,
	// otherwise a later partition creation would fail against rows stuck in the default partition
	if n := ownerCount(t, `SELECT count(*) FROM audit_log_default`); n != 0 {
		t.Fatalf("%d audit rows landed in the default partition", n)
	}
}
