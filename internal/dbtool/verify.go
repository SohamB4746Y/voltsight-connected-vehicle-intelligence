package dbtool

import (
	"context"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/seedgen"
)

// Report is the outcome of verifying the database against a manifest.
type Report struct {
	OK             bool              `json:"ok"`
	CountMismatch  map[string]string `json:"count_mismatch,omitempty"`
	DigestMismatch map[string]string `json:"digest_mismatch,omitempty"`
	InvalidVINs    int               `json:"invalid_vins_in_db"`
	Counts         map[string]int    `json:"counts"`
}

var tableName = regexp.MustCompile(`^[a-z_]+$`)

// Verify recomputes counts and canonical digests from the database and compares them with the manifest.
func Verify(ctx context.Context, pool *pgxpool.Pool, m seedgen.Manifest) (Report, error) {
	r := Report{OK: true, CountMismatch: map[string]string{}, DigestMismatch: map[string]string{}, Counts: map[string]int{}}
	for table, want := range m.Counts {
		if !tableName.MatchString(table) {
			return r, fmt.Errorf("unexpected table name %q", table)
		}
		var got int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			return r, err
		}
		r.Counts[table] = got
		if got != want {
			r.CountMismatch[table] = fmt.Sprintf("db=%d manifest=%d", got, want)
			r.OK = false
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM vehicle WHERE NOT vin_valid(vin::text)`).Scan(&r.InvalidVINs); err != nil {
		return r, err
	}
	if r.InvalidVINs != 0 {
		r.OK = false
	}

	check := func(name string, lines []string) {
		if got := seedgen.Digest(lines); got != m.Digests[name] {
			r.DigestMismatch[name] = fmt.Sprintf("db=%s manifest=%s", got, m.Digests[name])
			r.OK = false
		}
	}

	{
		rows, err := pool.Query(ctx, `SELECT id, tenant_id, name FROM fleet`)
		if err != nil {
			return r, err
		}
		var lines []string
		for rows.Next() {
			var f seedgen.Fleet
			if err := rows.Scan(&f.ID, &f.TenantID, &f.Name); err != nil {
				return r, err
			}
			lines = append(lines, f.Line())
		}
		rows.Close()
		check("fleet", lines)
	}
	{
		rows, err := pool.Query(ctx, `SELECT id, tenant_id, fleet_id, name, city, lat, lon FROM depot`)
		if err != nil {
			return r, err
		}
		var lines []string
		for rows.Next() {
			var d seedgen.Depot
			if err := rows.Scan(&d.ID, &d.TenantID, &d.FleetID, &d.Name, &d.City, &d.Lat, &d.Lon); err != nil {
				return r, err
			}
			lines = append(lines, d.Line())
		}
		rows.Close()
		check("depot", lines)
	}
	{
		rows, err := pool.Query(ctx, `SELECT trim(vin), tenant_id, fleet_id, model_id, home_depot_id, commissioned_at FROM vehicle`)
		if err != nil {
			return r, err
		}
		lines := make([]string, 0, m.Counts["vehicle"])
		for rows.Next() {
			var v seedgen.Vehicle
			if err := rows.Scan(&v.VIN, &v.TenantID, &v.FleetID, &v.ModelID, &v.HomeDepotID, &v.CommissionedAt); err != nil {
				return r, err
			}
			lines = append(lines, v.Line())
		}
		rows.Close()
		check("vehicle", lines)
	}
	{
		rows, err := pool.Query(ctx, `SELECT id, tenant_id, pseudonym FROM driver`)
		if err != nil {
			return r, err
		}
		lines := make([]string, 0, m.Counts["driver"])
		for rows.Next() {
			var d seedgen.Driver
			if err := rows.Scan(&d.ID, &d.TenantID, &d.Pseudonym); err != nil {
				return r, err
			}
			lines = append(lines, d.Line())
		}
		rows.Close()
		check("driver", lines)
	}
	{
		rows, err := pool.Query(ctx, `SELECT tenant_id, trim(vin), driver_id, valid_from, valid_to FROM vehicle_driver_assignment`)
		if err != nil {
			return r, err
		}
		lines := make([]string, 0, m.Counts["vehicle_driver_assignment"])
		for rows.Next() {
			var a seedgen.Assignment
			if err := rows.Scan(&a.TenantID, &a.VIN, &a.DriverID, &a.ValidFrom, &a.ValidTo); err != nil {
				return r, err
			}
			lines = append(lines, a.Line())
		}
		rows.Close()
		check("vehicle_driver_assignment", lines)
	}
	{
		rows, err := pool.Query(ctx, `SELECT id, tenant_id, depot_id, name, lat, lon, geohash, power_kw::float8, tariff_zone FROM charger`)
		if err != nil {
			return r, err
		}
		lines := make([]string, 0, m.Counts["charger"])
		for rows.Next() {
			var c seedgen.Charger
			var tid, did *uuid.UUID
			if err := rows.Scan(&c.ID, &tid, &did, &c.Name, &c.Lat, &c.Lon, &c.Geohash, &c.PowerKW, &c.TariffZone); err != nil {
				return r, err
			}
			c.TenantID, c.DepotID = tid, did
			lines = append(lines, c.Line())
		}
		rows.Close()
		check("charger", lines)
	}
	return r, nil
}
