//go:build stack

package sink

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
)

// SC5: why one SQL database is the wrong store for raw telemetry - the same rows, same schema shape, inserted into
// PostgreSQL (COPY, the fastest path) and ClickHouse (native batch), measuring rate and bytes on disk.
func TestInsertThroughputPostgresVsClickHouse(t *testing.T) {
	const n = 1_000_000
	env, _ := dotenv.Load("../../.env")
	dsn, _ := dbtool.OwnerDSN(env)
	pg, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ctx := context.Background()
	_, _ = pg.Exec(ctx, `DROP TABLE IF EXISTS bench_telemetry`)
	if _, err := pg.Exec(ctx, `CREATE TABLE bench_telemetry (tenant_id uuid, vin char(17), ts timestamptz, recv_ts timestamptz, seq bigint,
		lat float8, lon float8, speed_kmh float4, soc_pct float4, odo_km float8, heading_deg float4, pack_temp_c float4, ambient_temp_c float4,
		pack_voltage_v float4, pack_current_a float4, charge_state text, charger_id text, evt text, oem text, schema_ver smallint, dtc text[],
		PRIMARY KEY (tenant_id, vin, ts, seq))`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pg.Exec(ctx, `DROP TABLE IF EXISTS bench_telemetry`) }()

	tenant := uuid.New()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	row := func(i int) []any {
		vin := fmt.Sprintf("ZAR%014d", i%100000)
		ts := base.Add(time.Duration(i/100000) * time.Second)
		return []any{tenant, vin, ts, ts, int64(i / 100000), 13.0 + float64(i%1000)/1000, 80.0 + float64(i%777)/1000, float32(30), float32(20 + i%80), 5000 + float64(i)*0.001,
			float32(i % 360), float32(30), float32(30), float32(350), float32(-20), "NONE", "", "UNSPECIFIED", "AURORA", int16(1), []string{}}
	}
	t0 := time.Now()
	src := pgx.CopyFromSlice(n, func(i int) ([]any, error) { return row(i), nil })
	if _, err := pg.CopyFrom(ctx, pgx.Identifier{"bench_telemetry"}, []string{"tenant_id", "vin", "ts", "recv_ts", "seq", "lat", "lon", "speed_kmh", "soc_pct", "odo_km", "heading_deg",
		"pack_temp_c", "ambient_temp_c", "pack_voltage_v", "pack_current_a", "charge_state", "charger_id", "evt", "oem", "schema_ver", "dtc"}, src); err != nil {
		t.Fatal(err)
	}
	pgSecs := time.Since(t0).Seconds()
	var pgBytes int64
	_ = pg.QueryRow(ctx, `SELECT pg_total_relation_size('bench_telemetry')`).Scan(&pgBytes)

	conn, err := clickhouse.Open(chOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	table := "bench_" + uuid.NewString()[:8]
	table = "b" + table[6:]
	if err := conn.Exec(ctx, DDL(table)); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" SYNC") }()
	t1 := time.Now()
	for lo := 0; lo < n; lo += 50_000 {
		b, err := conn.PrepareBatch(ctx, "INSERT INTO "+table)
		if err != nil {
			t.Fatal(err)
		}
		for i := lo; i < lo+50_000; i++ {
			r := row(i)
			if err := b.Append(r[0], r[1], r[2], r[3], uint64(r[4].(int64)), r[5], r[6], r[7], r[8], r[9], r[10], r[11], r[12], r[13], r[14], r[15], r[16], r[17], r[18], uint8(1), []string{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := b.Send(); err != nil {
			t.Fatal(err)
		}
	}
	chSecs := time.Since(t1).Seconds()
	_ = conn.Exec(ctx, "OPTIMIZE TABLE "+table+" FINAL")
	var chBytes uint64
	_ = conn.QueryRow(ctx, fmt.Sprintf("SELECT sum(bytes_on_disk) FROM system.parts WHERE table = '%s' AND active", table)).Scan(&chBytes)
	t.Logf("MEASURED (%d rows incl. client-side row generation): PostgreSQL COPY %.0f rows/s, %.1f B/row on disk (with its primary-key index) | ClickHouse %.0f rows/s, %.1f B/row | ClickHouse is %.1fx faster and %.1fx smaller",
		n, float64(n)/pgSecs, float64(pgBytes)/n, float64(n)/chSecs, float64(chBytes)/n, pgSecs/chSecs, float64(pgBytes)/float64(chBytes))
	_ = url.Values{}
}
