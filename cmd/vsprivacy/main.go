// Command vsprivacy is the erasure worker (voltsight_privacy role): it executes pending erasure requests.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/privacy"
)

func main() {
	loop := flag.Duration("loop", 0, "poll at this interval (0 = once)")
	flag.Parse()
	ctx := context.Background()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_privacy", dotenv.Get(env, "DB_PRIVACY_PASSWORD"))
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()
	for {
		n, err := privacy.ProcessPending(ctx, pool)
		must(err)
		fmt.Printf("completed %d erasure request(s)\n", n)
		if *loop == 0 {
			return
		}
		time.Sleep(*loop)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsprivacy:", err)
		os.Exit(1)
	}
}
