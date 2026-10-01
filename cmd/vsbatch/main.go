// Command vsbatch runs the historical analytics over ClickHouse and writes the results to PostgreSQL:
// state-of-health estimates (from charging sessions) and trips (from speed/odometer), per tenant.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/batch"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/geo"
)

func main() {
	var (
		hours = flag.Float64("hours", 6, "analyse this many hours of history")
		table = flag.String("table", "telemetry_raw", "ClickHouse table")
	)
	flag.Parse()
	ctx := context.Background()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_batch", dotenv.Get(env, "DB_BATCH_PASSWORD")) // least privilege
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()
	ch, err := clickhouse.Open(&clickhouse.Options{Addr: []string{dotenv.GetOr(env, "CLICKHOUSE_ADDR", "127.0.0.1:9000")},
		Auth:        clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}})
	must(err)
	defer ch.Close()

	from := time.Now().UTC().Add(-time.Duration(*hours * float64(time.Hour)))
	var tenants []uuid.UUID
	rs, err := pool.Query(ctx, `SELECT id FROM tenant`)
	must(err)
	for rs.Next() {
		var id uuid.UUID
		must(rs.Scan(&id))
		tenants = append(tenants, id)
	}
	rs.Close()

	nominal := map[string]float64{}
	rs, err = pool.Query(ctx, `SELECT v.vin, vm.battery_kwh_nominal::float8 FROM vehicle v JOIN vehicle_model vm ON vm.id = v.model_id`)
	must(err)
	for rs.Next() {
		var vin string
		var kwh float64
		must(rs.Scan(&vin, &kwh))
		nominal[vin] = kwh
	}
	rs.Close()

	asOf := time.Now().UTC().Truncate(time.Hour)
	var totSoH, totTrips, totVeh int
	for _, tenant := range tenants {
		rows, err := ch.Query(ctx, `SELECT vin, ts, soc_pct, speed_kmh, lat, lon, odo_km, pack_voltage_v, pack_current_a, toString(charge_state)
			FROM `+*table+` WHERE tenant_id = @t AND ts >= @from ORDER BY vin, ts`, clickhouse.Named("t", tenant), clickhouse.Named("from", from))
		must(err)
		type soh struct {
			vin string
			cap float64
		}
		var sohs []soh
		var trips []batch.Trip
		var curVIN string
		var se *batch.SoHEstimator
		var tg *batch.TripSegmenter
		flush := func() {
			if curVIN == "" {
				return
			}
			if c, _, ok := se.Finish(); ok {
				sohs = append(sohs, soh{curVIN, c})
			}
			trips = append(trips, tg.Finish()...)
			totVeh++
		}
		for rows.Next() {
			var s batch.Sample
			var soc, spd, v, a float32
			var lat, lon float64
			var state string
			var ts time.Time
			var odo float64
			var vin string
			must(rows.Scan(&vin, &ts, &soc, &spd, &lat, &lon, &odo, &v, &a, &state))
			if vin != curVIN {
				flush()
				curVIN, se, tg = vin, &batch.SoHEstimator{}, &batch.TripSegmenter{}
			}
			s = batch.Sample{VIN: vin, TS: ts, SoC: float64(soc), SpeedKmh: float64(spd), Lat: lat, Lon: lon, OdoKm: odo,
				PackV: float64(v), PackA: float64(a), Charging: state == "CHARGING", Plugged: state == "PLUGGED"}
			se.Feed(s)
			tg.Feed(s)
		}
		must(rows.Err())
		rows.Close()
		flush()

		tx, err := pool.Begin(ctx)
		must(err)
		_, err = tx.Exec(ctx, `DELETE FROM trip WHERE tenant_id = $1 AND started_at >= $2`, tenant, from)
		must(err)
		b := &pgx.Batch{}
		for _, t := range trips {
			energy := t.SoCDrop / 100 * nominal[t.VIN]
			b.Queue(`INSERT INTO trip (tenant_id, vin, started_at, ended_at, start_geohash, end_geohash, distance_km, energy_kwh) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				tenant, t.VIN, t.Start, t.End, geo.Encode(t.StartLat, t.StartLon, 7), geo.Encode(t.EndLat, t.EndLon, 7), t.DistanceKm, energy)
		}
		for _, s := range sohs {
			b.Queue(`INSERT INTO soh_estimate (tenant_id, vin, as_of, method, capacity_kwh) VALUES ($1,$2,$3,'charge-session-v1',$4)
				ON CONFLICT (vin, as_of, method) DO UPDATE SET capacity_kwh = EXCLUDED.capacity_kwh`, tenant, s.vin, asOf, s.cap)
		}
		res := tx.SendBatch(ctx, b)
		for i := 0; i < len(trips)+len(sohs); i++ {
			if _, err := res.Exec(); err != nil {
				fmt.Fprintln(os.Stderr, "vsbatch: write:", err)
				_ = res.Close()
				_ = tx.Rollback(ctx)
				os.Exit(1)
			}
		}
		must(res.Close())
		must(tx.Commit(ctx))
		totSoH += len(sohs)
		totTrips += len(trips)
		fmt.Printf("tenant %s: %d trips, %d SoH estimates\n", tenant.String()[:8], len(trips), len(sohs))
	}
	fmt.Printf("done: %d vehicles analysed, %d trips, %d SoH estimates (window %s .. now)\n", totVeh, totTrips, totSoH, from.Format(time.RFC3339))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsbatch:", err)
		os.Exit(1)
	}
}
