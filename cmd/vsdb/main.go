// Command vsdb manages the VoltSight PostgreSQL database: migrations, service-role passwords,
// seed data and seed verification.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/seedgen"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx := context.Background()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)

	switch os.Args[1] {
	case "migrate-up":
		must(dbtool.MigrateUp(dsn))
		v, dirty, err := dbtool.Version(dsn)
		must(err)
		fmt.Printf("migrated to version %d (dirty=%v)\n", v, dirty)
	case "migrate-down":
		must(dbtool.MigrateDown(dsn))
		fmt.Println("all migrations reverted")
	case "bootstrap-roles":
		pool := connect(ctx, dsn)
		defer pool.Close()
		pw := map[string]string{}
		for role, key := range map[string]string{
			"voltsight_app": "DB_APP_PASSWORD", "voltsight_gateway": "DB_GATEWAY_PASSWORD",
			"voltsight_batch": "DB_BATCH_PASSWORD", "voltsight_alerts": "DB_ALERTS_PASSWORD",
			"voltsight_privacy": "DB_PRIVACY_PASSWORD", "voltsight_sealer": "DB_SEALER_PASSWORD",
		} {
			v := dotenv.Get(env, key)
			if v == "" {
				must(fmt.Errorf("%s not set (run `make env`)", key))
			}
			pw[role] = v
		}
		must(dbtool.BootstrapRoles(ctx, pool, pw))
		fmt.Printf("passwords set for %d service roles\n", len(pw))
	case "seed":
		fs := flag.NewFlagSet("seed", flag.ExitOnError)
		seed := fs.Uint64("seed", 20260925, "deterministic seed")
		vehicles := fs.Int("vehicles", 100_000, "number of vehicles")
		reset := fs.Bool("reset", false, "remove previously seeded data first")
		manifestPath := fs.String("manifest", "", "write the manifest JSON here")
		must(fs.Parse(os.Args[2:]))
		key := dotenv.Get(env, "PII_KEY")
		if key == "" {
			must(fmt.Errorf("PII_KEY not set (run `make env`)"))
		}
		t0 := time.Now()
		w, err := seedgen.Generate(seedgen.Config{Seed: *seed, Vehicles: *vehicles})
		must(err)
		genDur := time.Since(t0)
		m := w.Manifest()
		pool := connect(ctx, dsn)
		defer pool.Close()
		st, err := dbtool.Load(ctx, pool, w, key, *reset)
		must(err)
		fmt.Printf("generated %d vehicles in %s; loaded %d rows in %s (%.0f rows/s)\n",
			*vehicles, genDur.Round(time.Millisecond), st.Rows, st.Duration.Round(time.Millisecond), st.RowsPerS)
		if *manifestPath != "" {
			writeJSON(*manifestPath, m)
			fmt.Println("manifest written to", *manifestPath)
		}
		fmt.Println("manifest overall digest:", m.Overall)
	case "manifest":
		fs := flag.NewFlagSet("manifest", flag.ExitOnError)
		seed := fs.Uint64("seed", 20260925, "deterministic seed")
		vehicles := fs.Int("vehicles", 100_000, "number of vehicles")
		out := fs.String("out", "db/seed/manifest.json", "where to write the manifest")
		must(fs.Parse(os.Args[2:]))
		w, err := seedgen.Generate(seedgen.Config{Seed: *seed, Vehicles: *vehicles})
		must(err)
		writeJSON(*out, w.Manifest())
		fmt.Println("manifest written to", *out)
	case "verify":
		fs := flag.NewFlagSet("verify", flag.ExitOnError)
		manifestPath := fs.String("manifest", "", "manifest JSON to verify against (required)")
		must(fs.Parse(os.Args[2:]))
		raw, err := os.ReadFile(*manifestPath)
		must(err)
		var m seedgen.Manifest
		must(json.Unmarshal(raw, &m))
		pool := connect(ctx, dsn)
		defer pool.Close()
		rep, err := dbtool.Verify(ctx, pool, m)
		must(err)
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		if !rep.OK {
			os.Exit(1)
		}
	default:
		usage()
	}
}

func connect(ctx context.Context, dsn string) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, dsn)
	must(err)
	must(pool.Ping(ctx))
	return pool
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	must(err)
	must(os.WriteFile(path, append(b, '\n'), 0o644))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vsdb migrate-up|migrate-down|bootstrap-roles|manifest [-out f]|seed [-seed N -vehicles N -reset -manifest f]|verify -manifest f")
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsdb:", err)
		os.Exit(1)
	}
}
