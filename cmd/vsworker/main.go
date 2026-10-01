// Command vsworker runs the real-time stream processor (consumer group "rt-processor").
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"voltsight/internal/dotenv"
	"voltsight/internal/kafkautil"
	"voltsight/internal/rtp"
	"voltsight/internal/state"
)

func main() {
	var (
		brokers = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		topic   = flag.String("topic", kafkautil.Telemetry, "telemetry topic")
		group   = flag.String("group", "rt-processor", "consumer group")
		admin   = flag.String("admin", "127.0.0.1:9102", "admin listener (/healthz /metrics)")
		replay  = flag.Bool("replay", false, "a new group starts from the earliest offset instead of the end")
		poll    = flag.Int("max-poll", 20000, "records per batch")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	env, err := dotenv.Load(".env")
	must(err)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD"), PoolSize: 16})
	defer rdb.Close()
	must(rdb.Ping(ctx).Err())

	w, err := rtp.New(rtp.Config{Brokers: strings.Split(*brokers, ","), Topic: *topic, Group: *group, MaxPoll: *poll,
		StartFromBeginning: *replay, Logger: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}, &state.Store{R: rdb})
	must(err)
	defer w.Close()
	go func() {
		_ = (&http.Server{Addr: *admin, Handler: w.AdminHandler(), ReadHeaderTimeout: 5 * time.Second}).ListenAndServe()
	}()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n := w.Stats.Records.Load()
				fmt.Fprintf(os.Stderr, "rtp: %d events (%.0f/s) new=%d late=%d dup=%d stale=%d lag=%d\n", n, float64(n-last)/5,
					w.Stats.New.Load(), w.Stats.Late.Load(), w.Stats.Duplicate.Load(), w.Stats.Stale.Load(), w.Stats.Lag.Load())
				last = n
			}
		}
	}()
	fmt.Fprintf(os.Stderr, "rtp: group %q on %s (replay=%v)\n", *group, *topic, *replay)
	must(w.Run(ctx))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsworker:", err)
		os.Exit(1)
	}
}
