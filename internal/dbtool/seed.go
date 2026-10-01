package dbtool

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/seedgen"
)

// LoadStats reports how a seed load went.
type LoadStats struct {
	Rows     int           `json:"rows"`
	Duration time.Duration `json:"duration_ns"`
	RowsPerS float64       `json:"rows_per_s"`
}

func resetTables(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `TRUNCATE tenant, oem, vehicle_model, tariff_zone, plan, role, model_version CASCADE`)
	return err
}

// Load bulk-loads the world with COPY in one transaction. With reset, previously seeded data is removed
// first (so the load is repeatable). piiKey encrypts driver names/phones (AES-256-CBC, random IV per row).
func Load(ctx context.Context, pool *pgxpool.Pool, w *seedgen.World, piiKey string, reset bool) (LoadStats, error) {
	start := time.Now()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return LoadStats{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if reset {
		if err := resetTables(ctx, tx); err != nil {
			return LoadStats{}, fmt.Errorf("reset: %w", err)
		}
	}
	rows := 0
	copyTo := func(table string, cols []string, data [][]any) error {
		n, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(data))
		if err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
		rows += int(n)
		return nil
	}

	var data [][]any
	for _, o := range w.OEMs {
		data = append(data, []any{o.Code, o.Dialect})
	}
	if err := copyTo("oem", []string{"code", "dialect"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, m := range w.Models {
		data = append(data, []any{m.ID, m.OEM, m.Name, m.BatteryKWh, m.KWhPer100, m.MaxDCkW})
	}
	if err := copyTo("vehicle_model", []string{"id", "oem_code", "name", "battery_kwh_nominal", "kwh_per_100km_wltp", "max_dc_kw"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, z := range w.Zones {
		data = append(data, []any{z.ID, z.Name, z.Currency})
	}
	if err := copyTo("tariff_zone", []string{"id", "name", "currency"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, b := range w.Bands {
		data = append(data, []any{b.Zone, b.From, b.To, b.PricePerKWh})
	}
	if err := copyTo("tariff_band", []string{"zone_id", "hour_from", "hour_to", "price_per_kwh"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, p := range w.Plans {
		data = append(data, []any{p})
	}
	if err := copyTo("plan", []string{"code"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, pf := range w.PlanFeatures {
		data = append(data, []any{pf.Plan, pf.Feature})
	}
	if err := copyTo("plan_feature", []string{"plan_code", "feature"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, r := range w.Roles {
		data = append(data, []any{r})
	}
	if err := copyTo("role", []string{"code"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, t := range w.Tenants {
		data = append(data, []any{t.ID, t.Name, t.Region})
	}
	if err := copyTo("tenant", []string{"id", "name", "region"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, s := range w.Subscriptions {
		data = append(data, []any{s.ID, s.TenantID, s.Plan, s.ValidFrom})
	}
	if err := copyTo("subscription", []string{"id", "tenant_id", "plan_code", "valid_from"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, f := range w.Fleets {
		data = append(data, []any{f.ID, f.TenantID, f.Name})
	}
	if err := copyTo("fleet", []string{"id", "tenant_id", "name"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	for _, d := range w.Depots {
		data = append(data, []any{d.ID, d.TenantID, d.FleetID, d.Name, d.City, d.Lat, d.Lon})
	}
	if err := copyTo("depot", []string{"id", "tenant_id", "fleet_id", "name", "city", "lat", "lon"}, data); err != nil {
		return LoadStats{}, err
	}

	data = make([][]any, 0, len(w.Vehicles))
	for _, v := range w.Vehicles {
		data = append(data, []any{v.VIN, v.TenantID, v.FleetID, v.ModelID, v.HomeDepotID, v.CommissionedAt})
	}
	if err := copyTo("vehicle", []string{"vin", "tenant_id", "fleet_id", "model_id", "home_depot_id", "commissioned_at"}, data); err != nil {
		return LoadStats{}, err
	}

	// Drivers: PII goes through a staging table and is encrypted inside the database, so the key and
	// plaintext never need to be written to disk by this tool.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE driver_stage (id uuid, tenant_id uuid, pseudonym text, payload text) ON COMMIT DROP`); err != nil {
		return LoadStats{}, err
	}
	data = make([][]any, 0, len(w.Drivers))
	for _, d := range w.Drivers {
		data = append(data, []any{d.ID, d.TenantID, d.Pseudonym, d.FullName + "|" + d.Phone})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"driver_stage"}, []string{"id", "tenant_id", "pseudonym", "payload"}, pgx.CopyFromRows(data)); err != nil {
		return LoadStats{}, fmt.Errorf("copy driver_stage: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO driver (id, tenant_id, pseudonym, pii_enc)
		SELECT id, tenant_id, pseudonym,
		       iv || encrypt_iv(convert_to(payload, 'UTF8'), digest($1::text, 'sha256'), iv, 'aes-cbc/pad:pkcs')
		FROM (SELECT *, gen_random_bytes(16) AS iv FROM driver_stage) s`, piiKey)
	if err != nil {
		return LoadStats{}, fmt.Errorf("insert drivers: %w", err)
	}
	rows += int(tag.RowsAffected())

	data = make([][]any, 0, len(w.Assignments))
	for _, a := range w.Assignments {
		var to any
		if a.ValidTo != nil {
			to = *a.ValidTo
		}
		data = append(data, []any{a.TenantID, a.VIN, a.DriverID, a.ValidFrom, to})
	}
	if err := copyTo("vehicle_driver_assignment", []string{"tenant_id", "vin", "driver_id", "valid_from", "valid_to"}, data); err != nil {
		return LoadStats{}, err
	}

	data = make([][]any, 0, len(w.Chargers))
	for _, c := range w.Chargers {
		var tid, did any
		if c.TenantID != nil {
			tid = *c.TenantID
		}
		if c.DepotID != nil {
			did = *c.DepotID
		}
		data = append(data, []any{c.ID, tid, did, c.Name, c.Lat, c.Lon, c.Geohash, c.PowerKW, c.TariffZone})
	}
	if err := copyTo("charger", []string{"id", "tenant_id", "depot_id", "name", "lat", "lon", "geohash", "power_kw", "tariff_zone"}, data); err != nil {
		return LoadStats{}, err
	}

	data = nil
	var roles [][]any
	for _, u := range w.Users {
		data = append(data, []any{u.ID, u.TenantID, u.Email, u.Name})
		roles = append(roles, []any{u.TenantID, u.ID, u.Role})
	}
	if err := copyTo("app_user", []string{"id", "tenant_id", "email", "display_name"}, data); err != nil {
		return LoadStats{}, err
	}
	if err := copyTo("user_role", []string{"tenant_id", "user_id", "role_code"}, roles); err != nil {
		return LoadStats{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LoadStats{}, err
	}
	d := time.Since(start)
	return LoadStats{Rows: rows, Duration: d, RowsPerS: float64(rows) / d.Seconds()}, nil
}
