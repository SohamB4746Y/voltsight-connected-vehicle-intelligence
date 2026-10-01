// Command vsrecon reconciles the pipeline: what the gateway put into Kafka, what each consumer group has
// committed, and what ClickHouse holds. It exits non-zero if the books do not balance.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"voltsight/internal/dotenv"
	"voltsight/internal/kafkautil"
)

func main() {
	var (
		brokers = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		topic   = flag.String("topic", kafkautil.Telemetry, "telemetry topic")
		dlq     = flag.String("dlq", kafkautil.TelemetryDLQ, "dead-letter topic")
		table   = flag.String("table", "telemetry_raw", "ClickHouse table")
		rt      = flag.String("rt-group", "rt-processor", "real-time consumer group")
		sk      = flag.String("sink-group", "sink", "sink consumer group")
		sent    = flag.Int64("sent", -1, "events the producer says it sent (optional: checks sent == kafka + dlq)")
		out     = flag.String("report", "", "write the JSON report here")
	)
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env, err := dotenv.Load(".env")
	must(err)

	cl, err := kgo.NewClient(kgo.SeedBrokers(strings.Split(*brokers, ",")...))
	must(err)
	defer cl.Close()
	adm := kadm.NewClient(cl)
	endSum := func(t string) int64 {
		ends, err := adm.ListEndOffsets(ctx, t)
		must(err)
		var s int64
		ends.Each(func(o kadm.ListedOffset) { s += o.Offset })
		return s
	}
	lag := func(group, _ string) int64 { // total lag of the group (it consumes only the telemetry topic)
		lags, err := adm.Lag(ctx, group)
		must(err)
		if gl, ok := lags[group]; ok {
			return gl.Lag.Total()
		}
		return 0
	}
	accepted, rejected := endSum(*topic), endSum(*dlq)

	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{"127.0.0.1:9000"},
		Auth: clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")}})
	must(err)
	defer conn.Close()
	var raw, final uint64
	must(conn.QueryRow(ctx, "SELECT count() FROM "+*table).Scan(&raw))
	must(conn.QueryRow(ctx, "SELECT count() FROM "+*table+" FINAL").Scan(&final))

	rep := map[string]any{
		"kafka_telemetry_records": accepted, "kafka_dlq_records": rejected,
		"rt_group_lag": lag(*rt, *topic), "sink_group_lag": lag(*sk, *topic),
		"clickhouse_rows": raw, "clickhouse_rows_after_dedup": final,
		"clickhouse_duplicates_collapsed": int64(raw) - int64(final),
	}
	var problems []string
	if *sent >= 0 && *sent != accepted+rejected {
		problems = append(problems, fmt.Sprintf("sent %d != kafka %d + dlq %d", *sent, accepted, rejected))
	}
	if lag(*sk, *topic) == 0 && int64(raw) != accepted {
		problems = append(problems, fmt.Sprintf("sink drained but ClickHouse holds %d rows, Kafka has %d records", raw, accepted))
	}
	rep["problems"], rep["balanced"] = problems, len(problems) == 0
	b, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(b))
	if *out != "" {
		must(os.WriteFile(*out, append(b, '\n'), 0o644))
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsrecon:", err)
		os.Exit(1)
	}
}
