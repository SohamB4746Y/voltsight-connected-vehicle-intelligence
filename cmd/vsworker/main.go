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
	"voltsight/internal/rangerisk"
	"voltsight/internal/rtp"
	"voltsight/internal/seedgen"
	"voltsight/internal/sim"
	"voltsight/internal/state"
)

func main() {
	var (
		brokers  = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		topic    = flag.String("topic", kafkautil.Telemetry, "telemetry topic")
		group    = flag.String("group", "rt-processor", "consumer group")
		admin    = flag.String("admin", "127.0.0.1:9102", "admin listener (/healthz /metrics)")
		fromEnd  = flag.Bool("from-end", false, "a group with no committed offset skips the backlog (default: starts at the earliest offset, loses nothing)")
		poll     = flag.Int("max-poll", 20000, "records per batch")
		risk     = flag.Bool("risk", true, "run the range-risk engine and publish alerts.v1")
		estName  = flag.String("estimator", "ewma", "range estimator: baseline | ewma")
		gseed    = flag.Uint64("graph-seed", 1, "road graph seed (must equal the simulator's -seed)")
		vehicles = flag.Int("vehicles", 100000, "fleet size of the reference world (the database seed)")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	env, err := dotenv.Load(".env")
	must(err)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD"), PoolSize: 16})
	defer rdb.Close()
	must(rdb.Ping(ctx).Err())

	seeds := strings.Split(*brokers, ",")
	cfg := rtp.Config{Brokers: seeds, Topic: *topic, Group: *group, MaxPoll: *poll,
		StartAtEnd: *fromEnd, Logger: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}
	var riskProc *rangerisk.Processor
	if *risk {
		riskProc = startRisk(ctx, seeds, *gseed, *vehicles, *estName)
		cfg.Processor = riskProc
	}
	w, err := rtp.New(cfg, &state.Store{R: rdb})
	must(err)
	defer w.Close()
	if riskProc != nil {
		w.Registry().MustRegister(riskProc.Collectors()...)
	}
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
	fmt.Fprintf(os.Stderr, "rtp: group %q on %s (from-end=%v)\n", *group, *topic, *fromEnd)
	must(w.Run(ctx))
}

// startRisk loads the reference world, builds the road-graph overlays, and starts following charger status.
func startRisk(ctx context.Context, brokers []string, graphSeed uint64, vehicles int, estimator string) *rangerisk.Processor {
	t0 := time.Now()
	sw, err := seedgen.Generate(seedgen.Config{Seed: 20260925, Vehicles: vehicles})
	must(err)
	graphs := sim.CityGraphs(graphSeed)
	dir, chargers, err := rangerisk.FromWorld(sw, graphs)
	must(err)
	eng := rangerisk.NewEngine(graphs, chargers)
	pub, err := rangerisk.NewKafkaPublisher(brokers)
	must(err)
	feed, err := rangerisk.NewStatusFeed(brokers, eng)
	must(err)
	feed.Logger = func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }
	go feed.Run(ctx)
	var est rangerisk.Estimator = rangerisk.EWMA{}
	if estimator == "baseline" {
		est = rangerisk.Baseline{}
	}
	fmt.Fprintf(os.Stderr, "risk: %d vehicles, %d chargers, %d cities, estimator %s, ready in %s\n", len(dir), len(chargers), len(graphs), est.Name(), time.Since(t0).Round(time.Millisecond))
	return rangerisk.NewProcessor(rangerisk.DefaultConfig(), eng, dir, est, pub)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsworker:", err)
		os.Exit(1)
	}
}
