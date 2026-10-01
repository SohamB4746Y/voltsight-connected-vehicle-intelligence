// Package dbtool applies migrations, bootstraps roles, bulk-loads and verifies seed data.
package dbtool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/db"
)

func newMigrator(dsn string) (*migrate.Migrate, error) {
	src, err := iofs.New(db.Migrations, "migrations")
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, err
	}
	u.Scheme = "pgx5"
	return migrate.NewWithSourceInstance("iofs", src, u.String())
}

// MigrateUp applies all pending migrations.
func MigrateUp(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrateDown reverts every migration.
func MigrateDown(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Version returns the current migration version (0 when none applied).
func Version(dsn string) (uint, bool, error) {
	m, err := newMigrator(dsn)
	if err != nil {
		return 0, false, err
	}
	defer m.Close()
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}

// Fingerprint hashes the structural catalogue (columns, constraints, indexes, policies, grants,
// functions) of the public schema, ignoring the migration bookkeeping table.
func Fingerprint(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	queries := []string{
		`SELECT table_name||'.'||column_name||':'||data_type||':'||is_nullable||':'||coalesce(column_default,'')
		   FROM information_schema.columns WHERE table_schema='public' AND table_name <> 'schema_migrations'
		  ORDER BY 1`,
		`SELECT conrelid::regclass::text||':'||conname||':'||pg_get_constraintdef(oid)
		   FROM pg_constraint WHERE connamespace='public'::regnamespace AND conrelid::regclass::text <> 'schema_migrations'
		  ORDER BY 1`,
		`SELECT indexdef FROM pg_indexes WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY 1`,
		`SELECT tablename||':'||policyname||':'||cmd||':'||roles::text||':'||coalesce(qual,'')||':'||coalesce(with_check,'')
		   FROM pg_policies WHERE schemaname='public' ORDER BY 1`,
		`SELECT relname||':'||relrowsecurity::text||':'||relforcerowsecurity::text
		   FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind IN ('r','p') AND relname <> 'schema_migrations' ORDER BY 1`,
		`SELECT table_name||':'||grantee||':'||privilege_type FROM information_schema.role_table_grants
		  WHERE table_schema='public' AND grantee LIKE 'voltsight_%' ORDER BY 1`,
		`SELECT p.proname||':'||pg_get_function_arguments(p.oid)||':'||md5(p.prosrc)
		   FROM pg_proc p WHERE p.pronamespace='public'::regnamespace
		    AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e') ORDER BY 1`,
	}
	h := sha256.New()
	for _, q := range queries {
		rows, err := pool.Query(ctx, q)
		if err != nil {
			return "", fmt.Errorf("fingerprint query: %w", err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return "", err
			}
			h.Write([]byte(s))
			h.Write([]byte{'\n'})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", err
		}
		h.Write([]byte("--\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// BootstrapRoles sets the login password of each service role. Passwords are passed in, never stored.
func BootstrapRoles(ctx context.Context, pool *pgxpool.Pool, passwords map[string]string) error {
	for role, pw := range passwords {
		if !strings.HasPrefix(role, "voltsight_") {
			return fmt.Errorf("refusing to set a password for non-service role %q", role)
		}
		var stmt string
		if err := pool.QueryRow(ctx, `SELECT format('ALTER ROLE %I PASSWORD %L', $1::text, $2::text)`, role, pw).Scan(&stmt); err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("set password for %s: %w", role, err)
		}
	}
	return nil
}
