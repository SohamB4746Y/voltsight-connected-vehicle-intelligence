//go:build integration

package dbtests

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"voltsight/internal/dbtool"
)

func dsnFor(t *testing.T, user, password string) string {
	t.Helper()
	u, err := url.Parse(ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func loginAs(t *testing.T, user, password string) (*pgx.Conn, error) {
	t.Helper()
	return pgx.Connect(context.Background(), dsnFor(t, user, password))
}

// BootstrapRoles sets the service-role passwords; the roles must then be usable for real logins, and
// RLS must hold for a genuine login (not only for SET ROLE inside an owner session).
func TestBootstrapRoles_PasswordsWorkAndRLSHoldsForRealLogins(t *testing.T) {
	ctx := context.Background()
	hostile := `p'w"d;--` + "\n" + `'; DROP TABLE vehicle; --` // quoting must be handled server-side
	pw := map[string]string{
		"voltsight_app": hostile, "voltsight_gateway": "gw-pass", "voltsight_batch": "batch-pass",
		"voltsight_alerts": "alerts-pass", "voltsight_privacy": "privacy-pass", "voltsight_sealer": "sealer-pass",
	}
	if err := dbtool.BootstrapRoles(ctx, ownerPool, pw); err != nil {
		t.Fatal(err)
	}

	// correct password works, wrong password and empty password fail
	conn, err := loginAs(t, "voltsight_app", hostile)
	if err != nil {
		t.Fatalf("login with the bootstrapped password failed: %v", err)
	}
	defer conn.Close(ctx)
	if c, err := loginAs(t, "voltsight_app", "wrong"); err == nil {
		c.Close(ctx)
		t.Fatal("login with a wrong password succeeded")
	}
	if c, err := loginAs(t, "voltsight_app", ""); err == nil {
		c.Close(ctx)
		t.Fatal("login with an empty password succeeded")
	}
	if n := ownerCount(t, `SELECT count(*) FROM vehicle`); n < 2 {
		t.Fatalf("the hostile password must not have damaged the schema (vehicles=%d)", n)
	}

	// a genuine app-role login: no tenant GUC -> nothing; tenant A -> only A
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM vehicle`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("real app login without tenant context sees %d vehicles (err=%v), want 0", n, err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantA.ID.String()); err != nil {
		t.Fatal(err)
	}
	var other int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE tenant_id = $1), count(*) FILTER (WHERE tenant_id = $2) FROM vehicle`,
		tenantA.ID, tenantB.ID).Scan(&n, &other); err != nil {
		t.Fatal(err)
	}
	if n != 1 || other != 0 {
		t.Fatalf("real app login for tenant A sees own=%d other=%d, want 1 and 0", n, other)
	}
	var super bool
	if err := tx.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil || super {
		t.Fatalf("the app login must not be superuser/bypassrls (err=%v)", err)
	}
	// the gateway login can read credentials but not drivers
	gw, err := loginAs(t, "voltsight_gateway", "gw-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close(ctx)
	if _, err := gw.Exec(ctx, `SELECT count(*) FROM device_credential`); err != nil {
		t.Fatalf("gateway cannot read device_credential: %v", err)
	}
	if _, err := gw.Exec(ctx, `SELECT count(*) FROM driver`); err == nil {
		t.Fatal("gateway login read driver PII")
	}
}

func TestBootstrapRoles_RefusesNonServiceRolesAndReportsErrors(t *testing.T) {
	ctx := context.Background()
	if err := dbtool.BootstrapRoles(ctx, ownerPool, map[string]string{"postgres": "x"}); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a non-service role password change must be refused, got %v", err)
	}
	if err := dbtool.BootstrapRoles(ctx, ownerPool, map[string]string{"voltsight_nonexistent": "x"}); err == nil {
		t.Fatal("setting a password for a role that does not exist must fail")
	}
	// the owner role keeps working: its password was not touched
	if _, err := loginAs(t, "voltsight", "test-only"); err != nil {
		t.Fatalf("owner login broke: %v", err)
	}
}
