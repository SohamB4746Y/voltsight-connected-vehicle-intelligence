// Command vssink runs the ClickHouse sink (consumer group "sink").
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"voltsight/internal/dotenv"
	"voltsight/internal/kafkautil"
	"voltsight/internal/sink"
)

func main() {
	var (
		brokers = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		topic   = flag.String("topic", kafkautil.Telemetry, "telemetry topic")
		group   = flag.String("group", "sink", "consumer group")
		table   = flag.String("table", "telemetry_raw", "ClickHouse table")
		replay  = flag.Bool("replay", false, "a new group starts from the earliest offset instead of the end")
		poll    = flag.Int("max-poll", 50000, "records per insert")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := dotenv.Load(".env")
	must(err)
	s, err := sink.Open(ctx, sink.Config{Brokers: strings.Split(*brokers, ","), Topic: *topic, Group: *group, Table: *table,
		StartFromBeginning: *replay, MaxPoll: *poll, CH: &clickhouse.Options{
			Addr: []string{"127.0.0.1:9000"}, Auth: clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")},
			Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}, MaxOpenConns: 4}})
	must(err)
	defer s.Close()
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n := s.Stats.Inserted.Load()
				fmt.Fprintf(os.Stderr, "sink: %d rows (%.0f/s) batches=%d lag=%d\n", n, float64(n-last)/5, s.Stats.Batches.Load(), s.Stats.Lag.Load())
				last = n
			}
		}
	}()
	fmt.Fprintf(os.Stderr, "sink: group %q on %s -> %s (replay=%v)\n", *group, *topic, *table, *replay)
	must(s.Run(ctx))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vssink:", err)
		os.Exit(1)
	}
}
