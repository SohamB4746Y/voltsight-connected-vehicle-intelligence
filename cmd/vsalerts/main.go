// Command vsalerts runs the alert service (consumer group "alert-svc"): alerts.v1 -> PostgreSQL + Redis pub/sub.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"voltsight/internal/alerts"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
)

func main() {
	var (
		brokers = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		group   = flag.String("group", "alert-svc", "consumer group")
		latency = flag.String("latency-log", "", "write receive->persist latency per alert (NDJSON) here")
		fromEnd = flag.Bool("from-end", false, "a group with no committed offset skips the backlog (default: starts at the earliest offset)")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, err := url.Parse(dsn)
	must(err)
	pw := dotenv.Get(env, "DB_ALERTS_PASSWORD")
	if pw == "" {
		must(fmt.Errorf("DB_ALERTS_PASSWORD not set (run `make env && make bootstrap`)"))
	}
	u.User = url.UserPassword("voltsight_alerts", pw) // the single writer of alert rows
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD")})
	defer rdb.Close()

	s, err := alerts.New(alerts.Config{Brokers: strings.Split(*brokers, ","), Group: *group, StartAtEnd: *fromEnd, LatencyLog: *latency}, pool, rdb)
	must(err)
	defer s.Close()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "alerts: consumed=%d inserted=%d duplicates=%d published=%d decode_errors=%d\n",
					s.Stats.Consumed.Load(), s.Stats.Inserted.Load(), s.Stats.Duplicates.Load(), s.Stats.Published.Load(), s.Stats.DecodeErrors.Load())
			}
		}
	}()
	fmt.Fprintf(os.Stderr, "alerts: group %q -> PostgreSQL + Redis\n", *group)
	must(s.Run(ctx))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsalerts:", err)
		os.Exit(1)
	}
}
