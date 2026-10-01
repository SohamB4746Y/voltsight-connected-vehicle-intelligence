package seedgen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Manifest describes a generated dataset so that a database load can be verified against it.
type Manifest struct {
	GeneratorVersion string            `json:"generator_version"`
	Seed             uint64            `json:"seed"`
	Vehicles         int               `json:"vehicles"`
	Counts           map[string]int    `json:"counts"`
	Digests          map[string]string `json:"digests"` // sha256 over the sorted canonical row lines
	Overall          string            `json:"overall"` // sha256 over counts and digests
}

func f6(f float64) string   { return strconv.FormatFloat(f, 'f', 6, 64) }
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func optUUID(u *uuid.UUID) string {
	if u == nil {
		return "-"
	}
	return u.String()
}

// Line returns the canonical text form of the row used for digests (identical on load verification).
func (f Fleet) Line() string { return join(f.ID.String(), f.TenantID.String(), f.Name) }

func (d Depot) Line() string {
	return join(d.ID.String(), d.TenantID.String(), d.FleetID.String(), d.Name, d.City, f6(d.Lat), f6(d.Lon))
}

func (v Vehicle) Line() string {
	return join(v.VIN, v.TenantID.String(), v.FleetID.String(), strconv.Itoa(int(v.ModelID)),
		v.HomeDepotID.String(), ts(v.CommissionedAt))
}

func (d Driver) Line() string { return join(d.ID.String(), d.TenantID.String(), d.Pseudonym) }

func (a Assignment) Line() string {
	to := "-"
	if a.ValidTo != nil {
		to = ts(*a.ValidTo)
	}
	return join(a.TenantID.String(), a.VIN, a.DriverID.String(), ts(a.ValidFrom), to)
}

func (c Charger) Line() string {
	return join(c.ID.String(), optUUID(c.TenantID), optUUID(c.DepotID), c.Name, f6(c.Lat), f6(c.Lon),
		c.Geohash, f6(c.PowerKW), strconv.Itoa(int(c.TariffZone)))
}

func join(parts ...string) string { return strings.Join(parts, "|") }

// Digest hashes the sorted lines.
func Digest(lines []string) string {
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func digestOf[T interface{ Line() string }](rows []T) string {
	lines := make([]string, len(rows))
	for i, r := range rows {
		lines[i] = r.Line()
	}
	return Digest(lines)
}

// Manifest computes counts and digests for the dataset.
func (w *World) Manifest() Manifest {
	m := Manifest{
		GeneratorVersion: GeneratorVersion, Seed: w.Config.Seed, Vehicles: w.Config.Vehicles,
		Counts: map[string]int{
			"oem": len(w.OEMs), "vehicle_model": len(w.Models), "tariff_zone": len(w.Zones),
			"tariff_band": len(w.Bands), "plan": len(w.Plans), "plan_feature": len(w.PlanFeatures),
			"role": len(w.Roles), "tenant": len(w.Tenants), "subscription": len(w.Subscriptions),
			"fleet": len(w.Fleets), "depot": len(w.Depots), "vehicle": len(w.Vehicles),
			"driver": len(w.Drivers), "vehicle_driver_assignment": len(w.Assignments),
			"charger": len(w.Chargers), "app_user": len(w.Users), "user_role": len(w.Users),
		},
		Digests: map[string]string{
			"fleet": digestOf(w.Fleets), "depot": digestOf(w.Depots), "vehicle": digestOf(w.Vehicles),
			"driver": digestOf(w.Drivers), "vehicle_driver_assignment": digestOf(w.Assignments),
			"charger": digestOf(w.Chargers),
		},
	}
	b, _ := json.Marshal(struct {
		C map[string]int
		D map[string]string
	}{m.Counts, m.Digests})
	sum := sha256.Sum256(b)
	m.Overall = hex.EncodeToString(sum[:])
	return m
}
