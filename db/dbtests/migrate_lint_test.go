//go:build integration

package dbtests

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
)

// G1.3: up from empty, down to zero, up again; the structural fingerprint must be identical.
// Uses its own container because roles are cluster-wide and the down migration drops them.
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()
	dsn, stop, err := startPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if err := dbtool.MigrateUp(dsn); err != nil {
		t.Fatalf("first up: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fp1, err := dbtool.Fingerprint(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, dirty, err := dbtool.Version(dsn)
	if err != nil || dirty || v != 3 {
		t.Fatalf("version after up = %d dirty=%v err=%v, want 3", v, dirty, err)
	}

	if err := dbtool.MigrateDown(dsn); err != nil {
		t.Fatalf("down: %v", err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("%d tables remain after down", tables)
	}
	var roles int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'voltsight\_%'`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if roles != 0 {
		t.Fatalf("%d service roles remain after down", roles)
	}

	if err := dbtool.MigrateUp(dsn); err != nil {
		t.Fatalf("second up: %v", err)
	}
	fp2, err := dbtool.Fingerprint(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("schema fingerprint differs after up/down/up:\n  %s\n  %s", fp1, fp2)
	}
	t.Logf("schema fingerprint stable across up/down/up: %s", fp1)
}

var referenceTables = map[string]bool{ // global read-only data: deliberately without tenant_id
	"oem": true, "vehicle_model": true, "tariff_zone": true, "tariff_band": true, "plan": true,
	"plan_feature": true, "role": true, "model_version": true,
}

func tablesWithoutPartitions(t *testing.T) []string {
	t.Helper()
	rows, err := ownerPool.Query(context.Background(), `
		SELECT c.relname FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r','p') AND NOT c.relispartition
		  AND c.relname <> 'schema_migrations' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func tenantTables(t *testing.T) []string {
	t.Helper()
	rows, err := ownerPool.Query(context.Background(), `
		SELECT c.relname FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r','p') AND NOT c.relispartition
		  AND a.attname = 'tenant_id' AND NOT a.attisdropped ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// G1.4a: every table has a primary key; every table is either reference data or tenant-scoped.
func TestSchemaLint_KeysAndClassification(t *testing.T) {
	ctx := context.Background()
	tenantSet := map[string]bool{}
	for _, n := range tenantTables(t) {
		tenantSet[n] = true
	}
	for _, tbl := range tablesWithoutPartitions(t) {
		var pk int
		if err := ownerPool.QueryRow(ctx, `SELECT count(*) FROM pg_index WHERE indrelid = $1::regclass AND indisprimary`, tbl).Scan(&pk); err != nil {
			t.Fatal(err)
		}
		if pk != 1 {
			t.Errorf("table %s has no primary key", tbl)
		}
		if tbl == "tenant" || referenceTables[tbl] {
			continue
		}
		if !tenantSet[tbl] {
			t.Errorf("table %s is neither tenant-scoped nor listed as reference data", tbl)
		}
	}
}

// G1.4b: tenant tables have tenant_id NOT NULL (charger excepted: NULL = public network), RLS enabled and
// FORCED, and a policy for voltsight_app.
func TestSchemaLint_TenantTablesAreProtected(t *testing.T) {
	ctx := context.Background()
	tables := append(tenantTables(t), "tenant")
	if len(tables) < 20 {
		t.Fatalf("expected >= 20 tenant tables, found %d: %v", len(tables), tables)
	}
	for _, tbl := range tables {
		var enabled, forced bool
		if err := ownerPool.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $1::regclass`, tbl).Scan(&enabled, &forced); err != nil {
			t.Fatal(err)
		}
		if !enabled || !forced {
			t.Errorf("%s: RLS enabled=%v forced=%v, both must be true", tbl, enabled, forced)
		}
		if n := ownerCount(t, `SELECT count(*) FROM pg_policies WHERE schemaname='public' AND tablename=$1 AND 'voltsight_app' = ANY(roles)`, tbl); n == 0 {
			t.Errorf("%s: no policy for voltsight_app", tbl)
		}
		if tbl == "tenant" || tbl == "charger" {
			continue
		}
		var nullable string
		if err := ownerPool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name=$1 AND column_name='tenant_id'`, tbl).Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		if nullable != "NO" {
			t.Errorf("%s.tenant_id must be NOT NULL", tbl)
		}
	}
}

// G1.4c: every foreign key is covered by an index whose leading columns are the FK columns.
func TestSchemaLint_ForeignKeysIndexed(t *testing.T) {
	rows, err := ownerPool.Query(context.Background(), `
		SELECT c.conrelid::regclass::text, c.conname, c.conkey
		FROM pg_constraint c
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace AND NOT EXISTS (
		  SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.conrelid)`)
	if err != nil {
		t.Fatal(err)
	}
	type fk struct {
		table, name string
		cols        []int16
	}
	var fks []fk
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.table, &f.name, &f.cols); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, f)
	}
	rows.Close()
	if len(fks) < 30 { // sanity bound: the catalogue query must actually find the schema's FKs
		t.Fatalf("suspiciously few foreign keys: %d", len(fks))
	}
	for _, f := range fks {
		ok := false
		irows, err := ownerPool.Query(context.Background(), `SELECT indkey::int2[] FROM pg_index WHERE indrelid = $1::regclass AND indisvalid`, f.table)
		if err != nil {
			t.Fatal(err)
		}
		for irows.Next() {
			var key []int16
			if err := irows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			if len(key) >= len(f.cols) {
				lead := append([]int16(nil), key[:len(f.cols)]...)
				want := append([]int16(nil), f.cols...)
				sort.Slice(lead, func(i, j int) bool { return lead[i] < lead[j] })
				sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
				match := true
				for i := range lead {
					if lead[i] != want[i] {
						match = false
					}
				}
				ok = ok || match
			}
		}
		irows.Close()
		if !ok {
			t.Errorf("foreign key %s.%s has no supporting index", f.table, f.name)
		}
	}
}

// G1.4d: service roles are least-privilege.
func TestSchemaLint_RolesAreLeastPrivilege(t *testing.T) {
	ctx := context.Background()
	roles := []string{"voltsight_app", "voltsight_gateway", "voltsight_batch", "voltsight_alerts", "voltsight_privacy", "voltsight_sealer"}
	for _, r := range roles {
		var super, bypass, createRole, createDB bool
		if err := ownerPool.QueryRow(ctx, `SELECT rolsuper, rolbypassrls, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname=$1`, r).Scan(&super, &bypass, &createRole, &createDB); err != nil {
			t.Fatal(err)
		}
		if super || bypass || createRole || createDB {
			t.Errorf("%s has elevated attributes super=%v bypassrls=%v createrole=%v createdb=%v", r, super, bypass, createRole, createDB)
		}
		if n := ownerCount(t, `SELECT count(*) FROM pg_tables WHERE tableowner=$1`, r); n != 0 {
			t.Errorf("%s owns %d tables", r, n)
		}
		var canCreate bool
		if err := ownerPool.QueryRow(ctx, `SELECT has_schema_privilege($1,'public','CREATE')`, r).Scan(&canCreate); err != nil {
			t.Fatal(err)
		}
		if canCreate {
			t.Errorf("%s can create objects in schema public", r)
		}
		for _, p := range []string{"TRUNCATE", "REFERENCES", "TRIGGER"} {
			if n := ownerCount(t, `SELECT count(*) FROM information_schema.role_table_grants WHERE grantee=$1 AND privilege_type=$2`, r, p); n != 0 {
				t.Errorf("%s holds %s on %d tables", r, p, n)
			}
		}
	}
	// the application role must not be able to write reference data
	for _, tbl := range []string{"oem", "vehicle_model", "plan", "plan_feature", "role", "tariff_zone", "tariff_band", "model_version"} {
		if n := ownerCount(t, `SELECT count(*) FROM information_schema.role_table_grants WHERE grantee='voltsight_app' AND table_name=$1 AND privilege_type IN ('INSERT','UPDATE','DELETE')`, tbl); n != 0 {
			t.Errorf("voltsight_app can modify reference table %s", tbl)
		}
	}
}
