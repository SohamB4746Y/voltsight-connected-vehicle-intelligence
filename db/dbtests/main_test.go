//go:build integration

// Package dbtests exercises the PostgreSQL schema against a real PostgreSQL (pgvector) started with
// Testcontainers: migrations, schema lint, row-level-security attacks and ACID behaviour.
package dbtests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"voltsight/internal/dbtool"
	"voltsight/internal/vin"
)

const pgImage = "pgvector/pgvector:pg16"

var (
	ownerDSN  string
	ownerPool *pgxpool.Pool
	tenantA   = fx{ID: uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001"), Name: "tenant-a", Plan: "starter"}
	tenantB   = fx{ID: uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000002"), Name: "tenant-b", Plan: "enterprise"}
)

// fx holds the identities of one tenant's fixture rows.
type fx struct {
	ID   uuid.UUID
	Name string
	Plan string

	Fleet, Depot, Driver, User, Charger, Plan1, Alert uuid.UUID
	VIN                                               string
}

// startPostgres starts a throwaway PostgreSQL and returns its owner DSN and a terminate func.
func startPostgres(ctx context.Context) (string, func(), error) {
	ctr, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("voltsight"), postgres.WithUsername("voltsight"), postgres.WithPassword("test-only"),
		postgres.BasicWaitStrategies())
	if err != nil {
		return "", nil, err
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(ctr)
		return "", nil, err
	}
	return dsn, func() { _ = testcontainers.TerminateContainer(ctr) }, nil
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	dsn, stop, err := startPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot start PostgreSQL via Testcontainers:", err)
		os.Exit(1)
	}
	code := 1
	defer func() { stop(); os.Exit(code) }()

	if err := dbtool.MigrateUp(dsn); err != nil {
		fmt.Fprintln(os.Stderr, "migrate up:", err)
		return
	}
	ownerDSN = dsn
	ownerPool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	defer ownerPool.Close()
	if err := loadFixtures(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fixtures:", err)
		return
	}
	code = m.Run()
}

func mustVIN(tmpl string) string {
	v, err := vin.Build(tmpl)
	if err != nil {
		panic(err)
	}
	return v
}

func loadFixtures(ctx context.Context) error {
	exec := func(sql string, args ...any) error {
		if _, err := ownerPool.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("%w\n  sql: %s", err, sql)
		}
		return nil
	}
	shared := []string{
		`INSERT INTO oem VALUES ('AURORA','A')`,
		`INSERT INTO vehicle_model VALUES (1,'AURORA','Volt City',42,14.5,100)`,
		`INSERT INTO tariff_zone VALUES (1,'Test zone','INR')`,
		`INSERT INTO plan VALUES ('starter'),('enterprise')`,
		`INSERT INTO plan_feature VALUES ('starter','live_map'),('enterprise','live_map'),('enterprise','copilot')`,
		`INSERT INTO role VALUES ('viewer'),('dispatcher'),('tenant_admin')`,
		`INSERT INTO charger (id,tenant_id,depot_id,name,lat,lon,geohash,power_kw,tariff_zone)
		   VALUES (gen_random_uuid(), NULL, NULL, 'PUBLIC-1', 13.08, 80.27, 'tf3zxzr', 50, 1)`,
	}
	for _, s := range shared {
		if err := exec(s); err != nil {
			return err
		}
	}
	for i, t := range []*fx{&tenantA, &tenantB} {
		t.Fleet, t.Depot, t.Driver, t.User = uuid.New(), uuid.New(), uuid.New(), uuid.New()
		t.Charger, t.Plan1, t.Alert = uuid.New(), uuid.New(), uuid.New()
		t.VIN = mustVIN(fmt.Sprintf("1HGCM826x3A00%04d", 100+i)) // 'x' marks the check-digit position
		stmts := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenant (id,name,region) VALUES ($1,$2,'test')`, []any{t.ID, t.Name}},
			{`INSERT INTO subscription (tenant_id,plan_code) VALUES ($1,$2)`, []any{t.ID, t.Plan}},
			{`INSERT INTO fleet (id,tenant_id,name) VALUES ($1,$2,'fleet')`, []any{t.Fleet, t.ID}},
			{`INSERT INTO depot (id,tenant_id,fleet_id,name,city,lat,lon) VALUES ($1,$2,$3,'depot','Chennai',13,80)`, []any{t.Depot, t.ID, t.Fleet}},
			{`INSERT INTO vehicle (vin,tenant_id,fleet_id,model_id,home_depot_id,commissioned_at) VALUES ($1,$2,$3,1,$4,'2025-01-01')`, []any{t.VIN, t.ID, t.Fleet, t.Depot}},
			{`INSERT INTO driver (id,tenant_id,pseudonym,pii_enc) VALUES ($1,$2,'drv-1','\x00')`, []any{t.Driver, t.ID}},
			{`INSERT INTO vehicle_driver_assignment (tenant_id,vin,driver_id,valid_from) VALUES ($1,$2,$3,'2025-01-01')`, []any{t.ID, t.VIN, t.Driver}},
			{`INSERT INTO charger (id,tenant_id,depot_id,name,lat,lon,geohash,power_kw,tariff_zone) VALUES ($1,$2,$3,'PRIV',13,80,'tf3zxzr',22,1)`, []any{t.Charger, t.ID, t.Depot}},
			{`INSERT INTO app_user (id,tenant_id,email,display_name) VALUES ($1,$2,'u@example.test','User')`, []any{t.User, t.ID}},
			{`INSERT INTO user_role (tenant_id,user_id,role_code) VALUES ($1,$2,'dispatcher')`, []any{t.ID, t.User}},
			{`INSERT INTO device_credential (tenant_id,oem_code,cert_serial,cert_fingerprint,not_before,not_after)
			    VALUES ($1,'AURORA',$2,$3,now(),now()+interval '30 days')`, []any{t.ID, "serial-" + t.Name, fmt.Sprintf("%064x", i+1)}},
			{`INSERT INTO trip (tenant_id,vin,started_at,ended_at,start_geohash,end_geohash,distance_km,energy_kwh)
			    VALUES ($1,$2,now()-interval '2 hours',now()-interval '1 hour','tf3zxzr','tf3zxzs',12.5,2.4)`, []any{t.ID, t.VIN}},
			{`INSERT INTO charge_session (tenant_id,vin,charger_id,started_at,ended_at,soc_start,soc_end,kwh)
			    VALUES ($1,$2,$3,now()-interval '5 hours',now()-interval '4 hours',20,80,25)`, []any{t.ID, t.VIN, t.Charger}},
			{`INSERT INTO soh_estimate (tenant_id,vin,as_of,method,capacity_kwh) VALUES ($1,$2,now(),'session',40.1)`, []any{t.ID, t.VIN}},
			{`INSERT INTO charge_plan (id,tenant_id,created_by) VALUES ($1,$2,$3)`, []any{t.Plan1, t.ID, t.User}},
			{`INSERT INTO charge_plan_item (tenant_id,plan_id,vin,charger_id,start_at,end_at,target_soc)
			    VALUES ($1,$2,$3,$4,now()+interval '1 hour',now()+interval '2 hours',90)`, []any{t.ID, t.Plan1, t.VIN, t.Charger}},
			{`INSERT INTO alert (id,tenant_id,vin,rule,severity,window_start,detected_at)
			    VALUES ($1,$2,$3,'RANGE_CRITICAL','CRITICAL','2026-01-01T00:00:00Z',now())`, []any{t.Alert, t.ID, t.VIN}},
			{`INSERT INTO alert_event (tenant_id,alert_id,actor,action) VALUES ($1,$2,$3,'acknowledge')`, []any{t.ID, t.Alert, t.User}},
			{`INSERT INTO audit_log (tenant_id,actor,actor_type,action,resource_type) VALUES ($1,'fixture','service','seed','fixture')`, []any{t.ID}},
			{`INSERT INTO erasure_request (tenant_id,subject_id,requested_by) VALUES ($1,$2,$3)`, []any{t.ID, t.Driver, t.User}},
			{`INSERT INTO agent_session (id,tenant_id,user_id,llm) VALUES ($1,$2,$3,'stub')`, []any{t.Plan1, t.ID, t.User}},
			{`INSERT INTO agent_action (tenant_id,session_id,tool,args_hash,status) VALUES ($1,$2,'list_at_risk_vehicles',repeat('a',64),'proposed')`, []any{t.ID, t.Plan1}},
			{`INSERT INTO incident_embedding (tenant_id,kind,body,embedding) VALUES ($1,'alert','body',('['||repeat('0,',383)||'0]')::vector)`, []any{t.ID}},
		}
		for _, s := range stmts {
			if err := exec(s.sql, s.args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// asTenant runs fn inside a transaction that mimics one API request: SET LOCAL ROLE voltsight_app plus the
// tenant GUC. The transaction is always rolled back.
func asTenant(t *testing.T, tenant uuid.UUID, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := ownerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE voltsight_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant.String()); err != nil {
		t.Fatal(err)
	}
	fn(ctx, tx)
}

// tryAs executes one statement as the app role bound to tenant, in its own transaction.
func tryAs(t *testing.T, role string, guc *string, sql string, args ...any) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := ownerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL ROLE %s`, pgx.Identifier{role}.Sanitize())); err != nil {
		t.Fatal(err)
	}
	if guc != nil {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, *guc); err != nil {
			t.Fatal(err)
		}
	}
	tag, err := tx.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func ownerCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := ownerPool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n  sql: %s", err, sql)
	}
	return n
}

func strp(s string) *string { return &s }
