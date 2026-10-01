// Command vsseal seals and verifies the audit hash chain (voltsight_sealer role).
// usage: vsseal seal [-loop 10s] | vsseal verify
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/auditchain"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
)

func main() {
	loop := flag.Duration("loop", 0, "seal repeatedly at this interval (0 = once)")
	flag.Parse()
	cmd := flag.Arg(0)
	if cmd != "seal" && cmd != "verify" {
		fmt.Fprintln(os.Stderr, "usage: vsseal [-loop 10s] seal|verify")
		os.Exit(2)
	}
	ctx := context.Background()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_sealer", dotenv.Get(env, "DB_SEALER_PASSWORD"))
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()
	switch cmd {
	case "seal":
		for {
			n, err := auditchain.Seal(ctx, pool, uuid.Nil)
			must(err)
			fmt.Printf("sealed %d audit rows\n", n)
			if *loop == 0 {
				return
			}
			time.Sleep(*loop)
		}
	case "verify":
		rs, err := pool.Query(ctx, `SELECT DISTINCT tenant_id FROM audit_log`)
		must(err)
		var ts []uuid.UUID
		for rs.Next() {
			var t uuid.UUID
			must(rs.Scan(&t))
			ts = append(ts, t)
		}
		rs.Close()
		bad := 0
		for _, t := range ts {
			r, err := auditchain.Verify(ctx, pool, t)
			must(err)
			status := "OK"
			if !r.OK {
				status, bad = fmt.Sprintf("BROKEN at seq %d: %s", r.BrokenAt, r.Reason), bad+1
			}
			fmt.Printf("tenant %s: %d sealed rows: %s\n", t.String()[:8], r.Rows, status)
		}
		if bad > 0 {
			os.Exit(1)
		}
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsseal:", err)
		os.Exit(1)
	}
}
