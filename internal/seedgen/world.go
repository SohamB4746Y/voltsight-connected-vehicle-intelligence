// Package seedgen deterministically generates the synthetic master data of the platform:
// tenants, fleets, depots, vehicles with valid VINs, drivers, chargers, tariffs and demo users.
// The same Config always yields byte-identical data; no real vehicle or person data is used.
package seedgen

import (
	"fmt"
	"math"
	// Waiver (recorded in evidence/G1): this PRNG generates synthetic test data and must be deterministic
	// for a given seed; it is not used for any security purpose.
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"time"

	"github.com/google/uuid"

	"voltsight/internal/geo"
	"voltsight/internal/vin"
)

// GeneratorVersion changes whenever the generated data for a given seed changes.
const GeneratorVersion = "1"

var namespace = uuid.MustParse("6b1f0a3e-7c2d-4a55-9d1e-0c5f8e2b7a11")

// ID derives a stable UUIDv5 identity for an entity.
func ID(kind, name string) uuid.UUID { return uuid.NewSHA1(namespace, []byte(kind+":"+name)) }

// PlatformTenantID is the tenant that holds platform administrators (it owns no fleet data).
var PlatformTenantID = ID("tenant", "VoltSight Platform")

// Config selects the dataset. Vehicles defaults to 100,000.
type Config struct {
	Seed     uint64
	Vehicles int
}

type OEM struct{ Code, Dialect string }

type Model struct {
	ID                int16
	OEM, Name         string
	BatteryKWh        float64
	KWhPer100         float64
	MaxDCkW           float64
	wmi, vdsModelCode string
}

type Zone struct {
	ID       int16
	Name     string
	Currency string
}

type Band struct {
	Zone        int16
	From, To    int16
	PricePerKWh float64
}

type PlanFeature struct{ Plan, Feature string }

type Tenant struct {
	ID     uuid.UUID
	Name   string
	Slug   string
	Region string
	Plan   string // empty for the platform tenant
	cities []int
	share  float64
}

type Subscription struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Plan      string
	ValidFrom time.Time
}

type Fleet struct {
	ID, TenantID uuid.UUID
	Name         string
}

type Depot struct {
	ID, TenantID, FleetID uuid.UUID
	Name, City            string
	Lat, Lon              float64
	zone                  int16
}

type Vehicle struct {
	VIN            string
	TenantID       uuid.UUID
	FleetID        uuid.UUID
	ModelID        int16
	HomeDepotID    uuid.UUID
	CommissionedAt time.Time // date at 00:00 UTC
}

type Driver struct {
	ID, TenantID uuid.UUID
	Pseudonym    string
	FullName     string // synthetic; encrypted before it is stored
	Phone        string // synthetic
}

type Assignment struct {
	TenantID  uuid.UUID
	VIN       string
	DriverID  uuid.UUID
	ValidFrom time.Time
	ValidTo   *time.Time
}

type Charger struct {
	ID         uuid.UUID
	TenantID   *uuid.UUID // nil = public network
	DepotID    *uuid.UUID
	Name       string
	Lat, Lon   float64
	Geohash    string
	PowerKW    float64
	TariffZone int16
}

type User struct {
	ID, TenantID uuid.UUID
	Email, Name  string
	Role         string
}

// World is the complete generated dataset.
type World struct {
	Config        Config
	OEMs          []OEM
	Models        []Model
	Zones         []Zone
	Bands         []Band
	Plans         []string
	PlanFeatures  []PlanFeature
	Roles         []string
	Tenants       []Tenant
	Subscriptions []Subscription
	Fleets        []Fleet
	Depots        []Depot
	Vehicles      []Vehicle
	Drivers       []Driver
	Assignments   []Assignment
	Chargers      []Charger
	Users         []User
}

type city struct {
	name     string
	lat, lon float64
	code     string
	zone     int16
}

var cities = []city{
	{"Chennai", 13.0827, 80.2707, "CHE", 1},
	{"Bengaluru", 12.9716, 77.5946, "BLR", 2},
	{"Surat", 21.1702, 72.8311, "SRT", 3},
}

var tenantSpecs = []Tenant{
	{Name: "Meridian Logistics", Slug: "meridian", Region: "south", Plan: "enterprise", share: 0.40, cities: []int{0, 1}},
	{Name: "Coastal Rentals", Slug: "coastal", Region: "west", Plan: "pro", share: 0.25, cities: []int{2}},
	{Name: "Urban Courier Co", Slug: "urban", Region: "south", Plan: "pro", share: 0.15, cities: []int{1}},
	{Name: "Greenline Transit", Slug: "greenline", Region: "west", Plan: "starter", share: 0.12, cities: []int{2, 1}},
	{Name: "Apex Fleet Services", Slug: "apex", Region: "south", Plan: "enterprise", share: 0.08, cities: []int{0}},
}

// DemoRoles are the roles created for the demo users of the first two tenants.
var DemoRoles = []string{"viewer", "dispatcher", "energy_manager", "tenant_admin"}

var (
	firstNames = []string{"Aarav", "Vihaan", "Aditya", "Arjun", "Ishaan", "Kabir", "Rohan", "Karthik", "Nikhil", "Rahul",
		"Suresh", "Ramesh", "Vikram", "Manoj", "Deepak", "Ananya", "Diya", "Meera", "Priya", "Kavya",
		"Lakshmi", "Nisha", "Pooja", "Riya", "Sneha", "Divya", "Anjali", "Isha", "Neha", "Shreya"}
	lastNames = []string{"Iyer", "Nair", "Reddy", "Patel", "Shah", "Desai", "Kumar", "Singh", "Rao", "Menon",
		"Gupta", "Mehta", "Joshi", "Pillai", "Naidu", "Chandra", "Bhat", "Kulkarni", "Sharma", "Verma"}
)

func rng(seed uint64, stream string) *rand.Rand {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(stream); i++ {
		h = (h ^ uint64(stream[i])) * 1099511628211
	}
	return rand.New(rand.NewPCG(seed, h))
}

func tsDate(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// Generate builds the dataset for cfg. It is pure and deterministic.
func Generate(cfg Config) (*World, error) {
	if cfg.Vehicles <= 0 {
		cfg.Vehicles = 100_000
	}
	w := &World{Config: cfg}
	w.genReference()
	w.genTenants()
	r := rng(cfg.Seed, "topology")
	w.genFleetsAndDepots(r)
	if err := w.genVehicles(rng(cfg.Seed, "vehicles")); err != nil {
		return nil, err
	}
	w.genDrivers(rng(cfg.Seed, "drivers"))
	w.genChargers(rng(cfg.Seed, "chargers"))
	w.genUsers()
	return w, nil
}

func (w *World) genReference() {
	w.OEMs = []OEM{{"AURORA", "A"}, {"NIMBUS", "B"}, {"ZEPHYR", "A"}, {"ORION", "B"}}
	type m struct {
		oem, name       string
		kwh, per100, dc float64
	}
	table := []m{
		{"AURORA", "Volt City", 42, 14.5, 100}, {"AURORA", "Volt Van", 72, 19.5, 120}, {"AURORA", "Volt Hauler", 105, 27.0, 150},
		{"NIMBUS", "Cirrus Mini", 38, 13.8, 80}, {"NIMBUS", "Cirrus Cargo", 68, 20.5, 110}, {"NIMBUS", "Cirrus Max", 98, 26.0, 150},
		{"ZEPHYR", "Gale Hatch", 45, 15.0, 90}, {"ZEPHYR", "Gale Express", 75, 21.0, 125}, {"ZEPHYR", "Gale Freight", 110, 28.0, 175},
		{"ORION", "Nova Lite", 40, 14.0, 85}, {"ORION", "Nova Carrier", 70, 20.0, 115}, {"ORION", "Nova Titan", 100, 26.5, 140},
	}
	wmi := map[string]string{"AURORA": "ZAR", "NIMBUS": "ZNB", "ZEPHYR": "ZZP", "ORION": "ZRN"}
	for i, t := range table {
		w.Models = append(w.Models, Model{
			ID: int16(i + 1), OEM: t.oem, Name: t.name, BatteryKWh: t.kwh, KWhPer100: t.per100, MaxDCkW: t.dc,
			wmi: wmi[t.oem], vdsModelCode: fmt.Sprintf("E%02dB1", i+1),
		})
	}
	for _, c := range cities {
		w.Zones = append(w.Zones, Zone{ID: c.zone, Name: c.name + " time-of-use", Currency: "INR"})
		scale := 1.0 + 0.05*float64(c.zone-1)
		for _, b := range []struct {
			from, to int16
			p        float64
		}{{0, 6, 4.5}, {6, 10, 7.0}, {10, 18, 8.5}, {18, 22, 12.0}, {22, 24, 5.5}} {
			w.Bands = append(w.Bands, Band{Zone: c.zone, From: b.from, To: b.to, PricePerKWh: round4(b.p * scale)})
		}
	}
	w.Plans = []string{"starter", "pro", "enterprise"}
	w.PlanFeatures = []PlanFeature{
		{"starter", "live_map"}, {"starter", "alerts"},
		{"pro", "live_map"}, {"pro", "alerts"}, {"pro", "soh"}, {"pro", "charge_planning"},
		{"enterprise", "live_map"}, {"enterprise", "alerts"}, {"enterprise", "soh"}, {"enterprise", "charge_planning"},
		{"enterprise", "copilot"}, {"enterprise", "api_export"},
	}
	w.Roles = []string{"viewer", "dispatcher", "energy_manager", "tenant_admin", "platform_admin"}
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

func (w *World) genTenants() {
	for _, t := range tenantSpecs {
		t.ID = ID("tenant", t.Name)
		w.Tenants = append(w.Tenants, t)
		w.Subscriptions = append(w.Subscriptions, Subscription{
			ID: ID("subscription", t.Name), TenantID: t.ID, Plan: t.Plan, ValidFrom: tsDate(2026, 1, 1),
		})
	}
	w.Tenants = append(w.Tenants, Tenant{ID: PlatformTenantID, Name: "VoltSight Platform", Slug: "platform", Region: "global"})
}

// TenantVehicleCount splits total vehicles across tenants by share; the remainder goes to the first tenant.
func TenantVehicleCount(total int) []int {
	counts := make([]int, len(tenantSpecs))
	sum := 0
	for i, t := range tenantSpecs {
		counts[i] = int(float64(total) * t.share)
		sum += counts[i]
	}
	counts[0] += total - sum
	return counts
}

const vehiclesPerFleet = 2500

func (w *World) genFleetsAndDepots(r *rand.Rand) {
	counts := TenantVehicleCount(w.Config.Vehicles)
	for ti := range tenantSpecs {
		t := w.Tenants[ti]
		nFleets := (counts[ti] + vehiclesPerFleet - 1) / vehiclesPerFleet
		if nFleets == 0 {
			nFleets = 1
		}
		for f := 0; f < nFleets; f++ {
			fleet := Fleet{ID: ID("fleet", fmt.Sprintf("%s/%02d", t.Name, f)), TenantID: t.ID,
				Name: fmt.Sprintf("%s fleet %02d", t.Name, f+1)}
			w.Fleets = append(w.Fleets, fleet)
			nDepots := 3 + r.IntN(4)
			for d := 0; d < nDepots; d++ {
				c := cities[t.cities[r.IntN(len(t.cities))]]
				w.Depots = append(w.Depots, Depot{
					ID: ID("depot", fmt.Sprintf("%s/%02d/%02d", t.Name, f, d)), TenantID: t.ID, FleetID: fleet.ID,
					Name: fmt.Sprintf("%s F%02d %s-%02d", t.Slug, f+1, c.code, d+1), City: c.name,
					Lat: round6(c.lat + (r.Float64()-0.5)*0.16), Lon: round6(c.lon + (r.Float64()-0.5)*0.16),
					zone: c.zone,
				})
			}
		}
	}
}

func round6(f float64) float64 { return math.Round(f*1e6) / 1e6 }

var (
	yearCodes = []struct {
		year int
		code byte
	}{{2022, 'N'}, {2023, 'P'}, {2024, 'R'}, {2025, 'S'}, {2026, 'T'}}
	plantCodes = []byte{'C', 'B', 'S'}
)

func (w *World) genVehicles(r *rand.Rand) error {
	counts := TenantVehicleCount(w.Config.Vehicles)
	fleetsByTenant := map[uuid.UUID][]Fleet{}
	for _, f := range w.Fleets {
		fleetsByTenant[f.TenantID] = append(fleetsByTenant[f.TenantID], f)
	}
	depotsByFleet := map[uuid.UUID][]Depot{}
	for _, d := range w.Depots {
		depotsByFleet[d.FleetID] = append(depotsByFleet[d.FleetID], d)
	}
	w.Vehicles = make([]Vehicle, 0, w.Config.Vehicles)
	serial := 0
	for ti := range tenantSpecs {
		t := w.Tenants[ti]
		fleets := fleetsByTenant[t.ID]
		for i := 0; i < counts[ti]; i++ {
			serial++
			model := w.Models[r.IntN(len(w.Models))]
			yc := yearCodes[r.IntN(len(yearCodes))]
			plant := plantCodes[r.IntN(len(plantCodes))]
			tmpl := fmt.Sprintf("%sx%c%c%06d", model.wmi+model.vdsModelCode, yc.code, plant, serial)
			v, err := vin.Build(tmpl)
			if err != nil {
				return fmt.Errorf("build vin from %q: %w", tmpl, err)
			}
			fleet := fleets[i%len(fleets)]
			depots := depotsByFleet[fleet.ID]
			w.Vehicles = append(w.Vehicles, Vehicle{
				VIN: v, TenantID: t.ID, FleetID: fleet.ID, ModelID: model.ID,
				HomeDepotID:    depots[r.IntN(len(depots))].ID,
				CommissionedAt: tsDate(yc.year, time.Month(1+r.IntN(12)), 1+r.IntN(28)),
			})
		}
	}
	return nil
}

func (w *World) genDrivers(r *rand.Rand) {
	n := len(w.Vehicles)
	relief := (n + 9) / 10 // one relief driver per vehicle with index % 10 == 0
	total := n + relief
	w.Drivers = make([]Driver, 0, total)
	tenantOf := func(i int) uuid.UUID {
		if i < n {
			return w.Vehicles[i].TenantID
		}
		return w.Vehicles[(i-n)*10].TenantID // relief driver belongs to the tenant of the vehicle they cover
	}
	for i := 0; i < total; i++ {
		first, last := firstNames[r.IntN(len(firstNames))], lastNames[r.IntN(len(lastNames))]
		w.Drivers = append(w.Drivers, Driver{
			ID:        ID("driver", fmt.Sprintf("%d", i)),
			TenantID:  tenantOf(i),
			Pseudonym: fmt.Sprintf("drv-%016x", r.Uint64()),
			FullName:  first + " " + last,
			Phone:     fmt.Sprintf("+91 90000 %05d", r.IntN(100000)),
		})
	}
	w.Assignments = make([]Assignment, 0, total)
	for i, v := range w.Vehicles {
		primaryFrom := v.CommissionedAt
		if i%10 == 0 { // a relief driver covered the first 60 days, then the primary driver took over
			reliefTo := v.CommissionedAt.AddDate(0, 0, 60)
			w.Assignments = append(w.Assignments, Assignment{
				TenantID: v.TenantID, VIN: v.VIN, DriverID: w.Drivers[n+i/10].ID,
				ValidFrom: v.CommissionedAt, ValidTo: &reliefTo,
			})
			primaryFrom = reliefTo
		}
		w.Assignments = append(w.Assignments, Assignment{
			TenantID: v.TenantID, VIN: v.VIN, DriverID: w.Drivers[i].ID, ValidFrom: primaryFrom,
		})
	}
}

func (w *World) genChargers(r *rand.Rand) {
	publicPower := []float64{22, 50, 60, 120, 150}
	for _, c := range cities {
		for i := 0; i < 200; i++ {
			lat := round6(c.lat + (r.Float64()-0.5)*0.30)
			lon := round6(c.lon + (r.Float64()-0.5)*0.30)
			w.Chargers = append(w.Chargers, Charger{
				ID: ID("charger", fmt.Sprintf("PUB-%s-%04d", c.code, i)), Name: fmt.Sprintf("PUB-%s-%04d", c.code, i),
				Lat: lat, Lon: lon, Geohash: geo.Encode(lat, lon, 7),
				PowerKW: publicPower[r.IntN(len(publicPower))], TariffZone: c.zone,
			})
		}
	}
	depotPower := []float64{11, 22, 22, 50}
	for _, d := range w.Depots {
		tid, did := d.TenantID, d.ID
		for i, n := 0, 6+r.IntN(15); i < n; i++ {
			lat := round6(d.Lat + (r.Float64()-0.5)*0.004)
			lon := round6(d.Lon + (r.Float64()-0.5)*0.004)
			name := fmt.Sprintf("%s-C%02d", d.Name, i+1)
			w.Chargers = append(w.Chargers, Charger{
				ID: ID("charger", d.ID.String()+"/"+name), TenantID: &tid, DepotID: &did, Name: name, Lat: lat, Lon: lon,
				Geohash: geo.Encode(lat, lon, 7), PowerKW: depotPower[r.IntN(len(depotPower))], TariffZone: d.zone,
			})
		}
	}
}

func (w *World) genUsers() {
	for _, t := range w.Tenants[:2] {
		for _, role := range DemoRoles {
			email := role + "@" + t.Slug + ".example"
			w.Users = append(w.Users, User{ID: UserID(t.Name, role), TenantID: t.ID, Email: email,
				Name: t.Name + " " + role, Role: role})
		}
	}
	w.Users = append(w.Users, User{ID: UserID("VoltSight Platform", "platform_admin"), TenantID: PlatformTenantID,
		Email: "admin@platform.example", Name: "Platform administrator", Role: "platform_admin"})
}

// UserID is the stable identity (also the Keycloak subject) of a demo user.
func UserID(tenantName, role string) uuid.UUID { return ID("user", tenantName+"/"+role) }
