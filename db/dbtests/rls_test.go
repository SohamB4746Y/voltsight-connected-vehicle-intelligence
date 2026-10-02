//go:build integration

package dbtests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// G1.5: tenant A, acting through the application role, must not be able to read, create, change or
// delete tenant B's data on ANY tenant-scoped table.

func TestRLS_ReadIsolationOnEveryTenantTable(t *testing.T) {
	tables := tenantTables(t)
	asTenant(t, tenantA.ID, func(ctx context.Context, tx pgx.Tx) {
		for _, tbl := range tables {
			var own, other int
			q := fmt.Sprintf(`SELECT count(*) FILTER (WHERE tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2) FROM %s`, tbl)
			if err := tx.QueryRow(ctx, q, tenantA.ID, tenantB.ID).Scan(&own, &other); err != nil {
				t.Errorf("%s: %v", tbl, err)
				// the transaction is now aborted; later tables are covered by the unset-GUC test and a rerun
				return
			}
			if other != 0 {
				t.Errorf("LEAK: tenant A sees %d of tenant B's rows in %s", other, tbl)
			}
			if own == 0 {
				t.Errorf("fixture gap: tenant A has no visible rows in %s (the test would prove nothing)", tbl)
			}
			var total int
			if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, tbl)).Scan(&total); err != nil {
				t.Fatal(err)
			}
			wantTotal := own
			if tbl == "charger" { // public chargers are visible to everyone
				wantTotal = own + 1
			}
			if total != wantTotal {
				t.Errorf("%s: unfiltered count %d, want %d (only own rows visible)", tbl, total, wantTotal)
			}
		}
	})
}

func TestRLS_TenantTableSeesOnlyItself(t *testing.T) {
	asTenant(t, tenantA.ID, func(ctx context.Context, tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("tenant A sees %d tenant rows, want 1", n)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tenant WHERE id = $1`, tenantB.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("tenant B row visible to A: n=%d err=%v", n, err)
		}
	})
}

func TestRLS_UnsetOrEmptyGUCSeesNothing(t *testing.T) {
	for _, tbl := range append(tenantTables(t), "tenant") {
		for name, guc := range map[string]*string{"unset": nil, "empty": strp("")} {
			ctx := context.Background()
			tx, err := ownerPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE voltsight_app`); err != nil {
				t.Fatal(err)
			}
			if guc != nil {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, *guc); err != nil {
					t.Fatal(err)
				}
			}
			var n int
			err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`, tbl, publicFilter(tbl))).Scan(&n)
			_ = tx.Rollback(ctx)
			if err != nil {
				t.Errorf("%s (%s GUC): %v", tbl, name, err)
				continue
			}
			if n != 0 {
				t.Errorf("fail-closed violated: %s returned %d rows with %s GUC", tbl, n, name)
			}
		}
	}
}

// charger deliberately exposes the public network; exclude it from the "sees nothing" probe.
func publicFilter(tbl string) string {
	if tbl == "charger" {
		return "tenant_id IS NOT NULL"
	}
	return "true"
}

func TestRLS_MalformedGUCErrorsInsteadOfLeaking(t *testing.T) {
	for _, bad := range []string{"not-a-uuid", "' OR '1'='1", "*", tenantA.ID.String() + "," + tenantB.ID.String()} {
		_, err := tryAs(t, "voltsight_app", strp(bad), `SELECT count(*) FROM vehicle`)
		if err == nil {
			t.Errorf("malformed tenant GUC %q was accepted", bad)
		}
	}
}

func TestRLS_WriteAttacksAreRejectedAndChangeNothing(t *testing.T) {
	type counts struct{ a, b int }
	snapshot := func(tbl string) counts {
		return counts{
			ownerCount(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id=$1`, tbl), tenantA.ID),
			ownerCount(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE tenant_id=$1`, tbl), tenantB.ID),
		}
	}
	for _, tbl := range tenantTables(t) {
		before := snapshot(tbl)
		a := tenantA.ID.String()

		// 1. DELETE of tenant B's rows
		n, err := tryAs(t, "voltsight_app", &a, fmt.Sprintf(`DELETE FROM %s WHERE tenant_id=$1`, tbl), tenantB.ID)
		if err == nil && n != 0 {
			t.Errorf("%s: DELETE of tenant B rows affected %d rows", tbl, n)
		}
		// 2. UPDATE moving A's rows into tenant B (WITH CHECK) or re-owning B's rows
		for _, q := range []string{
			fmt.Sprintf(`UPDATE %s SET tenant_id=$2 WHERE tenant_id=$1`, tbl), // A -> B
			fmt.Sprintf(`UPDATE %s SET tenant_id=$1 WHERE tenant_id=$2`, tbl), // B -> A
		} {
			n, err := tryAs(t, "voltsight_app", &a, q, tenantA.ID, tenantB.ID)
			if err == nil && n != 0 {
				t.Errorf("%s: cross-tenant UPDATE affected %d rows (query %q)", tbl, n, q)
			}
		}
		if after := snapshot(tbl); after != before {
			t.Errorf("%s: row counts changed by attacks: before=%v after=%v", tbl, before, after)
		}
	}
}

func TestRLS_InsertAttacks(t *testing.T) {
	a := tenantA.ID.String()
	// explicit insert into another tenant
	_, err := tryAs(t, "voltsight_app", &a, `INSERT INTO fleet (id,tenant_id,name) VALUES (gen_random_uuid(), $1, 'evil')`, tenantB.ID)
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("INSERT into tenant B's fleet should violate RLS, got: %v", err)
	}
	// own tenant but pointing at the other tenant's fleet: composite FK must reject
	_, err = tryAs(t, "voltsight_app", &a,
		`INSERT INTO depot (id,tenant_id,fleet_id,name,city,lat,lon) VALUES (gen_random_uuid(), $1, $2, 'x','y',1,1)`, tenantA.ID, tenantB.Fleet)
	if err == nil {
		t.Error("depot of tenant A referencing tenant B's fleet was accepted (composite FK failed)")
	}
	// the public charger network is read-only for tenants
	_, err = tryAs(t, "voltsight_app", &a,
		`INSERT INTO charger (id,tenant_id,name,lat,lon,geohash,power_kw,tariff_zone) VALUES (gen_random_uuid(), NULL, 'rogue', 1,1,'s0000',50,1)`)
	if err == nil {
		t.Error("tenant created a public charger")
	}
	n, err := tryAs(t, "voltsight_app", &a, `UPDATE charger SET power_kw = 1 WHERE tenant_id IS NULL`)
	if err == nil && n != 0 {
		t.Errorf("tenant modified %d public chargers", n)
	}
	// reference data is read-only
	_, err = tryAs(t, "voltsight_app", &a, `INSERT INTO plan VALUES ('hacked')`)
	if err == nil {
		t.Error("application role inserted reference data")
	}
	_, err = tryAs(t, "voltsight_app", &a, `INSERT INTO plan_feature VALUES ('starter','copilot')`)
	if err == nil {
		t.Error("application role granted itself a feature")
	}
	_, err = tryAs(t, "voltsight_app", &a, `UPDATE subscription SET plan_code='enterprise'`)
	if err == nil {
		t.Error("application role upgraded its own subscription")
	}
}

func TestRLS_ViewsAndFunctionsDoNotLeak(t *testing.T) {
	asTenant(t, tenantA.ID, func(ctx context.Context, tx pgx.Tx) {
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicle_alert_summary WHERE tenant_id <> $1`, tenantA.ID).Scan(&others); err != nil {
			t.Fatal(err)
		}
		if others != 0 {
			t.Fatalf("view vehicle_alert_summary leaks %d rows of other tenants", others)
		}
		var mine int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicle_alert_summary`).Scan(&mine); err != nil || mine != 1 {
			t.Fatalf("view should show tenant A's single vehicle, got %d err=%v", mine, err)
		}

		var has bool
		// B is enterprise (has copilot); A must not learn that, and A (starter) lacks copilot itself.
		if err := tx.QueryRow(ctx, `SELECT tenant_has_feature($1,'copilot')`, tenantB.ID).Scan(&has); err != nil || has {
			t.Fatalf("entitlement function leaked tenant B's plan: has=%v err=%v", has, err)
		}
		if err := tx.QueryRow(ctx, `SELECT tenant_has_feature($1,'copilot')`, tenantA.ID).Scan(&has); err != nil || has {
			t.Fatalf("starter plan must not include copilot: has=%v err=%v", has, err)
		}
		if err := tx.QueryRow(ctx, `SELECT tenant_has_feature($1,'live_map')`, tenantA.ID).Scan(&has); err != nil || !has {
			t.Fatalf("starter plan must include live_map: has=%v err=%v", has, err)
		}
	})
	asTenant(t, tenantB.ID, func(ctx context.Context, tx pgx.Tx) {
		var has bool
		if err := tx.QueryRow(ctx, `SELECT tenant_has_feature($1,'copilot')`, tenantB.ID).Scan(&has); err != nil || !has {
			t.Fatalf("enterprise plan must include copilot: has=%v err=%v", has, err)
		}
	})
}

func TestRLS_AuditLogIsAppendOnlyAndTenantBound(t *testing.T) {
	a := tenantA.ID.String()
	n, err := tryAs(t, "voltsight_app", &a, `INSERT INTO audit_log (tenant_id,actor,actor_type,action,resource_type) VALUES ($1,'u','user','read','vehicle')`, tenantA.ID)
	if err != nil || n != 1 {
		t.Fatalf("app must be able to append its own audit rows: n=%d err=%v", n, err)
	}
	if _, err := tryAs(t, "voltsight_app", &a, `INSERT INTO audit_log (tenant_id,actor,actor_type,action,resource_type) VALUES ($1,'u','user','read','vehicle')`, tenantB.ID); err == nil {
		t.Error("app wrote an audit row for another tenant")
	}
	if _, err := tryAs(t, "voltsight_app", &a, `UPDATE audit_log SET action='tampered'`); err == nil {
		t.Error("app updated audit_log")
	}
	if _, err := tryAs(t, "voltsight_app", &a, `DELETE FROM audit_log`); err == nil {
		t.Error("app deleted from audit_log")
	}
	if _, err := tryAs(t, "voltsight_app", &a, `TRUNCATE audit_log`); err == nil {
		t.Error("app truncated audit_log")
	}
	// the sealer may assign chain fields but nothing else
	n, err = tryAs(t, "voltsight_sealer", nil, `UPDATE audit_log SET seq=1, prev_hash='\x00', hash='\x01' WHERE tenant_id=$1`, tenantA.ID)
	if err != nil || n == 0 {
		t.Errorf("sealer must be able to seal rows: n=%d err=%v", n, err)
	}
	if _, err := tryAs(t, "voltsight_sealer", nil, `UPDATE audit_log SET action='tampered'`); err == nil {
		t.Error("sealer modified audit content")
	}
	if _, err := tryAs(t, "voltsight_sealer", nil, `DELETE FROM audit_log`); err == nil {
		t.Error("sealer deleted audit rows")
	}
}

func TestRLS_ServiceRolesAreScoped(t *testing.T) {
	// gateway: read-only credentials/vehicles, nothing else
	if _, err := tryAs(t, "voltsight_gateway", nil, `SELECT count(*) FROM device_credential`); err != nil {
		t.Errorf("gateway must read device_credential: %v", err)
	}
	for _, q := range []string{`SELECT count(*) FROM driver`, `SELECT count(*) FROM alert`, `DELETE FROM vehicle`, `UPDATE device_credential SET status='revoked'`} {
		if _, err := tryAs(t, "voltsight_gateway", nil, q); err == nil {
			t.Errorf("gateway should not be allowed: %s", q)
		}
	}
	// alert service: may write alerts, may not touch drivers or credentials
	if _, err := tryAs(t, "voltsight_alerts", nil, `SELECT count(*) FROM alert`); err != nil {
		t.Errorf("alert service must read alerts: %v", err)
	}
	for _, q := range []string{`SELECT count(*) FROM driver`, `SELECT count(*) FROM device_credential`, `DELETE FROM alert`} {
		if _, err := tryAs(t, "voltsight_alerts", nil, q); err == nil {
			t.Errorf("alert service should not be allowed: %s", q)
		}
	}
	// privacy worker: may erase drivers, may not read alerts or credentials
	if _, err := tryAs(t, "voltsight_privacy", nil, `SELECT count(*) FROM driver`); err != nil {
		t.Errorf("privacy worker must read drivers: %v", err)
	}
	for _, q := range []string{`SELECT count(*) FROM alert`, `SELECT count(*) FROM device_credential`, `SELECT count(*) FROM audit_log`} {
		if _, err := tryAs(t, "voltsight_privacy", nil, q); err == nil {
			t.Errorf("privacy worker should not be allowed: %s", q)
		}
	}
	// batch: no PII
	if _, err := tryAs(t, "voltsight_batch", nil, `SELECT count(*) FROM driver`); err == nil {
		t.Error("batch role must not read driver PII")
	}
	// without a tenant GUC the service roles see rows (explicit policies), the app role does not
	n := 0
	ctx := context.Background()
	tx, _ := ownerPool.Begin(ctx)
	defer tx.Rollback(ctx) //nolint:errcheck
	_, _ = tx.Exec(ctx, `SET LOCAL ROLE voltsight_alerts`)
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT tenant_id) FROM alert`).Scan(&n); err != nil || n != 2 {
		t.Errorf("alert service should see both tenants' alerts through its explicit policy, got %d tenants err=%v", n, err)
	}
}

// Residual risk, asserted so the documentation stays truthful: the tenant GUC is chosen by the caller of
// the database session. Isolation therefore depends on the API deriving it from the verified JWT and
// using SET LOCAL per request (tested at gate G9). The database cannot defend against an API that lies.
func TestKnownLimitation_TenantGUCIsCallerControlled(t *testing.T) {
	asTenant(t, tenantA.ID, func(ctx context.Context, tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantB.ID.String()); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM vehicle WHERE tenant_id=$1`, tenantB.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatal("expected the documented limitation to hold (caller-controlled GUC); if the DB now prevents it, update docs/security/STRIDE.md")
		}
		t.Logf("LIMITATION (documented): a session that sets app.tenant_id itself sees that tenant's rows (%d); mitigated at the API layer", n)
	})
}
