//go:build stack

package sink

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/dotenv"
	"voltsight/internal/testkit"
)

func chOptions(t testing.TB) *clickhouse.Options {
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	return &clickhouse.Options{
		Addr: []string{"127.0.0.1:9000"}, Auth: clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}, DialTimeout: 10 * time.Second,
	}
}

func scalar(t testing.TB, conn driver.Conn, q string) uint64 {
	var n uint64
	if err := conn.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func runSink(t *testing.T, topic, group, table string) *Sink {
	s, err := Open(context.Background(), Config{Brokers: testkit.Brokers, Topic: topic, Group: group, Table: table,
		CH: chOptions(t), MaxPoll: 20_000})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.Run(ctx); err != nil {
			t.Errorf("sink: %v", err)
		}
	}()
	testkit.WaitDrained(t, group, topic, 90*time.Second)
	cancel()
	wg.Wait()
	return s
}

// G4.7 every consumed record is inserted; duplicates collapse on the event identity; storage is
// partitioned by event day.
func TestSinkInsertsEverythingAndDeduplicatesOnIdentity(t *testing.T) {
	conn, err := clickhouse.Open(chOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc := testkit.NewScenario(21, 200, 500, 0.05, 0.08, 0.01)
	table := "t4_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	t.Cleanup(func() { _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table+" SYNC") })
	topic := testkit.Topic(t, "t4.sink", 6)
	testkit.Produce(t, topic, sc.Events)

	s := runSink(t, topic, "gs-"+uuid.NewString()[:8], table)
	defer s.Close()
	if s.Stats.DecodeErrors.Load() != 0 || s.Stats.Inserted.Load() != int64(len(sc.Events)) {
		t.Fatalf("inserted %d of %d (%d decode errors)", s.Stats.Inserted.Load(), len(sc.Events), s.Stats.DecodeErrors.Load())
	}
	// before any merge the table holds every delivered record, duplicates included
	if got := scalar(t, conn, "SELECT count() FROM "+table); got < uint64(sc.Distinct) || got > uint64(len(sc.Events)) {
		t.Fatalf("raw rows %d outside [%d distinct, %d delivered]", got, sc.Distinct, len(sc.Events))
	}
	// FINAL applies the ReplacingMergeTree collapse: exactly one row per (tenant, vin, ts, seq)
	if got := scalar(t, conn, "SELECT count() FROM "+table+" FINAL"); got != uint64(sc.Distinct) {
		t.Fatalf("rows after FINAL = %d, want the %d distinct events", got, sc.Distinct)
	}
	if err := conn.Exec(context.Background(), "OPTIMIZE TABLE "+table+" FINAL"); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, conn, "SELECT count() FROM "+table); got != uint64(sc.Distinct) {
		t.Fatalf("rows after OPTIMIZE FINAL = %d, want %d", got, sc.Distinct)
	}
	if got := scalar(t, conn, fmt.Sprintf("SELECT uniqExact(vin) FROM %s WHERE tenant_id = '%s'", table, sc.Tenant)); got != uint64(len(sc.VINs)) {
		t.Fatalf("%d distinct vehicles stored, want %d", got, len(sc.VINs))
	}
	if parts := scalar(t, conn, fmt.Sprintf("SELECT uniqExact(partition) FROM system.parts WHERE table = '%s' AND active", table)); parts < 1 {
		t.Fatal("no partitions")
	}

	// values survive the trip exactly
	var vin string
	var seq uint64
	var soc float32
	var evt, charge, oem string
	var dtc []string
	var ts time.Time
	e := sc.Events[0]
	if err := conn.QueryRow(context.Background(), fmt.Sprintf(
		"SELECT vin, seq, soc_pct, toString(evt), toString(charge_state), oem, dtc, ts FROM %s FINAL WHERE vin = '%s' AND seq = %d", table, e.Vin, e.Seq)).
		Scan(&vin, &seq, &soc, &evt, &charge, &oem, &dtc, &ts); err != nil {
		t.Fatal(err)
	}
	if vin != e.Vin || seq != e.Seq || soc != e.SocPct || evt != "UNSPECIFIED" || charge != "UNSPECIFIED" || oem != "AURORA" || len(dtc) != 0 ||
		ts.UnixMilli() != e.Ts.AsTime().UnixMilli() {
		t.Fatalf("stored row differs from the event: %v %v %v %v %v %v %v %v", vin, seq, soc, evt, charge, oem, dtc, ts)
	}
	rate := float64(s.Stats.Inserted.Load()) / (float64(s.Stats.InsertNanos.Load()) / 1e9)
	t.Logf("MEASURED: %d rows inserted in %d batches; ClickHouse insert throughput %.0f rows/s (insert time only)", s.Stats.Inserted.Load(), s.Stats.Batches.Load(), rate)
}

func TestSinkMapsEveryEnumAndDTCArray(t *testing.T) {
	conn, err := clickhouse.Open(chOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc := testkit.NewScenario(23, 30, 40, 0, 0, 0)
	for i, e := range sc.Events {
		e.Evt = telemetryv1.Event(i % 11)
		e.ChargeState = telemetryv1.ChargeState(i % 4)
		if i%5 == 0 {
			e.Dtc = []string{"P0A7F", "P0A0D"}
		}
	}
	table := "t4_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	t.Cleanup(func() { _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table+" SYNC") })
	topic := testkit.Topic(t, "t4.enum", 3)
	testkit.Produce(t, topic, sc.Events)
	s := runSink(t, topic, "ge-"+uuid.NewString()[:8], table)
	defer s.Close()
	if s.Stats.DecodeErrors.Load() != 0 {
		t.Fatalf("%d records failed to decode/append", s.Stats.DecodeErrors.Load())
	}
	if got := scalar(t, conn, "SELECT uniqExact(evt) FROM "+table); got != 11 {
		t.Fatalf("%d distinct event values stored, want 11", got)
	}
	if got := scalar(t, conn, "SELECT uniqExact(charge_state) FROM "+table); got != 4 {
		t.Fatalf("%d distinct charge states stored, want 4", got)
	}
	if got := scalar(t, conn, "SELECT countIf(length(dtc) = 2) FROM "+table); got == 0 {
		t.Fatal("DTC arrays were not stored")
	}
}

func TestOpenRejectsUnsafeTableNames(t *testing.T) {
	for _, name := range []string{"x; DROP TABLE y", "Telemetry", "a b", "", "1abc", strings.Repeat("a", 80)} {
		if _, err := Open(context.Background(), Config{Table: name, CH: chOptions(t)}); err == nil {
			t.Errorf("table name %q accepted", name)
		}
	}
}

// G4.9 defect fix: a ClickHouse that restarts mid-run must slow the sink down, not kill it or lose records.
// Offsets are committed only after an acknowledged insert, so after the restart every distinct event is present.
func TestSinkSurvivesClickHouseRestartWithoutLoss(t *testing.T) {
	conn, err := clickhouse.Open(chOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sc := testkit.NewScenario(31, 400, 500, 0.02, 0.03, 0.005)
	table := "t4_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	topic := testkit.Topic(t, "t4.restart", 6)
	testkit.Produce(t, topic, sc.Events)
	group := "gr-" + uuid.NewString()[:8]

	s, err := Open(context.Background(), Config{Brokers: testkit.Brokers, Topic: topic, Group: group, Table: table, CH: chOptions(t),
		MaxPoll: 5_000, MaxRetryTime: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()

	deadline := time.Now().Add(60 * time.Second)
	for s.Stats.Batches.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("sink did not start inserting")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out, err := exec.Command("docker", "restart", "voltsight-clickhouse-1").CombinedOutput(); err != nil {
		t.Fatalf("docker restart: %v: %s", err, out)
	}
	testkit.WaitDrained(t, group, topic, 150*time.Second)
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("sink died instead of retrying: %v", err)
	}
	t.Cleanup(func() { _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table+" SYNC") })
	if s.Stats.Retries.Load() == 0 {
		t.Skip("restart completed between batches; no insert had to be retried (rerun)")
	}
	rows := scalar(t, conn, "SELECT count() FROM "+table+" FINAL")
	if rows != uint64(sc.Distinct) {
		t.Fatalf("after ClickHouse restart: %d distinct rows, want %d (lost %d)", rows, sc.Distinct, uint64(sc.Distinct)-rows)
	}
	t.Logf("MEASURED: survived a ClickHouse restart with %d retried attempts; %d/%d distinct events present", s.Stats.Retries.Load(), rows, sc.Distinct)
}
