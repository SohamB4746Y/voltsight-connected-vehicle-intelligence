// Command vsrecon reconciles the pipeline: what the gateway put into Kafka, what each consumer group has
// committed, what the stream worker decided about every record, and what ClickHouse holds. It exits non-zero
// if the books do not balance.
//
// The invariants (a record is one Kafka message; an event is one distinct (vin, seq)):
//
//	worker processed        == Kafka telemetry records           every record seen exactly once by the worker
//	ClickHouse (FINAL) rows == worker new + late + stale          every distinct event is stored, none invented
//	ClickHouse raw rows     >= ClickHouse FINAL rows              duplicates may still await a background merge
//	both groups' lag        == 0                                  nothing left behind
//	sent (optional)         == Kafka records + DLQ records        nothing lost between the producer and Kafka
//
// Group lag alone proves nothing: a group that started at the log end reports zero lag while having skipped
// everything before it joined (the defect found in G4). The distinct-event invariant is what catches that.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
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
		wm      = flag.String("worker-metrics", "http://127.0.0.1:9102/metrics", "stream worker /metrics URL (verdict counters)")
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
		gl, ok := lags[group]
		if !ok {
			must(fmt.Errorf("consumer group %q does not exist", group))
		}
		return gl.Lag.Total()
	}
	accepted, rejected := endSum(*topic), endSum(*dlq)

	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{"127.0.0.1:9000"},
		Auth: clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")}})
	must(err)
	defer conn.Close()
	var raw, final uint64
	must(conn.QueryRow(ctx, "SELECT count() FROM "+*table).Scan(&raw))
	must(conn.QueryRow(ctx, "SELECT count() FROM "+*table+" FINAL").Scan(&final))

	verdicts, err := workerVerdicts(*wm)
	must(err)
	processed := verdicts["new"] + verdicts["late"] + verdicts["duplicate"] + verdicts["stale"]
	distinct := verdicts["new"] + verdicts["late"] + verdicts["stale"]
	rtLag, skLag := lag(*rt, *topic), lag(*sk, *topic)
	rep := map[string]any{
		"kafka_telemetry_records": accepted, "kafka_dlq_records": rejected,
		"rt_group_lag": rtLag, "sink_group_lag": skLag,
		"worker_verdicts": verdicts, "worker_processed": processed, "worker_distinct_events": distinct,
		"clickhouse_rows": raw, "clickhouse_rows_after_dedup": final,
		"clickhouse_duplicates_awaiting_merge": int64(raw) - int64(final),
	}
	var problems []string
	if *sent >= 0 && *sent != accepted+rejected {
		problems = append(problems, fmt.Sprintf("sent %d != kafka %d + dlq %d", *sent, accepted, rejected))
	}
	if rtLag != 0 || skLag != 0 {
		problems = append(problems, fmt.Sprintf("consumers not drained: rt lag %d, sink lag %d", rtLag, skLag))
	}
	if processed != accepted {
		problems = append(problems, fmt.Sprintf("worker processed %d records, Kafka has %d", processed, accepted))
	}
	if int64(final) != distinct {
		problems = append(problems, fmt.Sprintf("ClickHouse holds %d distinct events, worker saw %d (%d missing)", final, distinct, distinct-int64(final)))
	}
	if raw < final {
		problems = append(problems, fmt.Sprintf("ClickHouse raw rows %d < FINAL rows %d", raw, final))
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

// workerVerdicts reads rtp_events_total{verdict=...} from the stream worker's Prometheus endpoint.
func workerVerdicts(url string) (map[string]int64, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("worker metrics: %w", err)
	}
	defer resp.Body.Close()
	out := map[string]int64{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		const p = `rtp_events_total{verdict="`
		if !strings.HasPrefix(line, p) {
			continue
		}
		rest := line[len(p):]
		q := strings.Index(rest, `"}`)
		if q < 0 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest[q+2:]), 64)
		if err != nil {
			return nil, err
		}
		out[rest[:q]] = int64(v)
	}
	return out, sc.Err()
}
