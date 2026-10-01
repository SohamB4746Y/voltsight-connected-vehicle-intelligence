//go:build integration

package dbtests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/seedgen"
)

const seedPIIKey = "test-only-pii-key"

// G1.7: the full-size seed loads, verifies against its manifest, is repeatable, and tampering is detected.
// Own container: the shared one holds hand-made fixtures and the seed reset truncates tenant data.
func TestSeed100KVehicles(t *testing.T) {
	ctx := context.Background()
	dsn, stop, err := startPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := dbtool.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	const seed = 20260925
	w, err := seedgen.Generate(seedgen.Config{Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	m := w.Manifest()
	if m.Vehicles != 100_000 || m.Counts["vehicle"] != 100_000 {
		t.Fatalf("manifest vehicles=%d count=%d, want 100000", m.Vehicles, m.Counts["vehicle"])
	}

	st, err := dbtool.Load(ctx, pool, w, seedPIIKey, false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("loaded %d rows in %s (%.0f rows/s)", st.Rows, st.Duration, st.RowsPerS)

	rep, err := dbtool.Verify(ctx, pool, m)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatalf("verification against manifest failed: %+v", rep)
	}
	if rep.Counts["vehicle"] != 100_000 || rep.InvalidVINs != 0 {
		t.Fatalf("vehicles=%d invalid_vins=%d", rep.Counts["vehicle"], rep.InvalidVINs)
	}
	if out := os.Getenv("VOLTSIGHT_EVIDENCE_DIR"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{"manifest": m, "load": st, "verify": rep}, "", "  ")
		if err := os.WriteFile(filepath.Join(out, "seed_run.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// idempotent: --reset reload of the same seed yields the same verified state
	if _, err := dbtool.Load(ctx, pool, w, seedPIIKey, true); err != nil {
		t.Fatalf("reset reload: %v", err)
	}
	if rep, err := dbtool.Verify(ctx, pool, m); err != nil || !rep.OK {
		t.Fatalf("after reset reload: %+v err=%v", rep, err)
	}

	// a different seed produces a different manifest, and the old manifest no longer verifies
	w2, err := seedgen.Generate(seedgen.Config{Seed: seed + 1})
	if err != nil {
		t.Fatal(err)
	}
	if w2.Manifest().Overall == m.Overall {
		t.Fatal("different seed must produce a different manifest")
	}
	if _, err := dbtool.Load(ctx, pool, w2, seedPIIKey, true); err != nil {
		t.Fatal(err)
	}
	if rep, _ := dbtool.Verify(ctx, pool, m); rep.OK {
		t.Fatal("verification must fail when the database holds data from a different seed")
	}
	if _, err := dbtool.Load(ctx, pool, w, seedPIIKey, true); err != nil {
		t.Fatal(err)
	}

	// tampering with one vehicle row is detected by the digest, with row counts unchanged
	if _, err := pool.Exec(ctx, `UPDATE vehicle SET model_id = CASE WHEN model_id = 1 THEN 2 ELSE 1 END
	                              WHERE vin = (SELECT min(vin) FROM vehicle)`); err != nil {
		t.Fatal(err)
	}
	rep, err = dbtool.Verify(ctx, pool, m)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.DigestMismatch["vehicle"] == "" || len(rep.CountMismatch) != 0 {
		t.Fatalf("one-row tampering not caught by digest alone: %+v", rep)
	}
}

// PII is encrypted at rest inside the database: not readable without the key, recoverable with it.
func TestSeedPIIIsEncrypted(t *testing.T) {
	ctx := context.Background()
	dsn, stop, err := startPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := dbtool.MigrateUp(dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	w, err := seedgen.Generate(seedgen.Config{Seed: 7, Vehicles: 500})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbtool.Load(ctx, pool, w, seedPIIKey, false); err != nil {
		t.Fatal(err)
	}
	first := w.Drivers[0]

	var enc []byte
	if err := pool.QueryRow(ctx, `SELECT pii_enc FROM driver WHERE id=$1`, first.ID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) < 32 || strings.Contains(string(enc), first.FullName) || strings.Contains(string(enc), first.Phone) {
		t.Fatalf("pii_enc looks unencrypted (%d bytes)", len(enc))
	}
	var plain string
	if err := pool.QueryRow(ctx, `
		SELECT convert_from(decrypt_iv(substring(pii_enc from 17), digest($2::text,'sha256'), substring(pii_enc from 1 for 16), 'aes-cbc/pad:pkcs'), 'UTF8')
		FROM driver WHERE id=$1`, first.ID, seedPIIKey).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if plain != first.FullName+"|"+first.Phone {
		t.Fatalf("decrypted PII %q does not match the generated record", plain)
	}
	// the wrong key must not yield the plaintext
	err = pool.QueryRow(ctx, `
		SELECT convert_from(decrypt_iv(substring(pii_enc from 17), digest('wrong-key','sha256'), substring(pii_enc from 1 for 16), 'aes-cbc/pad:pkcs'), 'UTF8')
		FROM driver WHERE id=$1`, first.ID).Scan(&plain)
	if err == nil && plain == first.FullName+"|"+first.Phone {
		t.Fatal("wrong key decrypted the PII")
	}
	// IVs are random per row: identical plaintext must not give identical ciphertext prefixes
	var distinctIV int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT substring(pii_enc from 1 for 16)) FROM driver`).Scan(&distinctIV); err != nil || distinctIV != len(w.Drivers) {
		t.Fatalf("expected %d distinct IVs, got %d (err=%v)", len(w.Drivers), distinctIV, err)
	}
}
