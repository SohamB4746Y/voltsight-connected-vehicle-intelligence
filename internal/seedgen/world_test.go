package seedgen

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"voltsight/internal/vin"
)

func mustGen(t *testing.T, cfg Config) *World {
	t.Helper()
	w, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestExactVehicleCountAndSplit(t *testing.T) {
	for _, n := range []int{1, 7, 1003, 100_000} {
		counts := TenantVehicleCount(n)
		sum := 0
		for _, c := range counts {
			sum += c
		}
		if sum != n {
			t.Fatalf("n=%d split sums to %d", n, sum)
		}
	}
	w := mustGen(t, Config{Seed: 1}) // default size
	if len(w.Vehicles) != 100_000 {
		t.Fatalf("default vehicle count = %d, want 100000", len(w.Vehicles))
	}
}

func TestDeterminism(t *testing.T) {
	a := mustGen(t, Config{Seed: 42, Vehicles: 5000}).Manifest()
	b := mustGen(t, Config{Seed: 42, Vehicles: 5000}).Manifest()
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different manifests")
	}
	c := mustGen(t, Config{Seed: 43, Vehicles: 5000}).Manifest()
	if a.Overall == c.Overall {
		t.Fatal("different seeds produced identical manifests")
	}
	// ids are seed-independent by design (stable identities), data attributes are not
	if a.Digests["vehicle"] == c.Digests["vehicle"] {
		t.Fatal("vehicle digest should depend on the seed")
	}
}

func TestVINsValidAndUnique(t *testing.T) {
	w := mustGen(t, Config{Seed: 9, Vehicles: 20_000})
	seen := map[string]bool{}
	for _, v := range w.Vehicles {
		if err := vin.Check(v.VIN); err != nil {
			t.Fatalf("invalid VIN %s: %v", v.VIN, err)
		}
		if seen[v.VIN] {
			t.Fatalf("duplicate VIN %s", v.VIN)
		}
		seen[v.VIN] = true
	}
}

func TestReferentialAndTenantConsistency(t *testing.T) {
	w := mustGen(t, Config{Seed: 5, Vehicles: 12_345})
	fleet := map[uuid.UUID]Fleet{}
	for _, f := range w.Fleets {
		fleet[f.ID] = f
	}
	depot := map[uuid.UUID]Depot{}
	for _, d := range w.Depots {
		if fleet[d.FleetID].TenantID != d.TenantID {
			t.Fatalf("depot %s tenant differs from its fleet's tenant", d.Name)
		}
		depot[d.ID] = d
	}
	vehicles := map[string]Vehicle{}
	for _, v := range w.Vehicles {
		vehicles[v.VIN] = v
		if fleet[v.FleetID].TenantID != v.TenantID {
			t.Fatalf("vehicle %s fleet belongs to another tenant", v.VIN)
		}
		d := depot[v.HomeDepotID]
		if d.FleetID != v.FleetID {
			t.Fatalf("vehicle %s home depot not in its fleet", v.VIN)
		}
		if v.CommissionedAt.Hour() != 0 || v.CommissionedAt.Location() != time.UTC {
			t.Fatalf("commissioned_at must be a UTC date")
		}
	}
	drivers := map[uuid.UUID]Driver{}
	pseud := map[uuid.UUID]map[string]bool{}
	for _, d := range w.Drivers {
		drivers[d.ID] = d
		if pseud[d.TenantID] == nil {
			pseud[d.TenantID] = map[string]bool{}
		}
		if pseud[d.TenantID][d.Pseudonym] {
			t.Fatalf("duplicate pseudonym within tenant")
		}
		pseud[d.TenantID][d.Pseudonym] = true
	}
	type win struct{ from, to time.Time }
	byVIN := map[string][]win{}
	for _, a := range w.Assignments {
		v := vehicles[a.VIN]
		if drivers[a.DriverID].TenantID != v.TenantID || a.TenantID != v.TenantID {
			t.Fatalf("assignment crosses tenants for %s", a.VIN)
		}
		end := a.ValidFrom.AddDate(1000, 0, 0)
		if a.ValidTo != nil {
			end = *a.ValidTo
			if !end.After(a.ValidFrom) {
				t.Fatalf("empty assignment window")
			}
		}
		for _, o := range byVIN[a.VIN] {
			if a.ValidFrom.Before(o.to) && o.from.Before(end) {
				t.Fatalf("overlapping assignments for %s", a.VIN)
			}
		}
		byVIN[a.VIN] = append(byVIN[a.VIN], win{a.ValidFrom, end})
	}
	if len(w.Drivers) != len(w.Vehicles)+(len(w.Vehicles)+9)/10 {
		t.Fatalf("driver count %d unexpected", len(w.Drivers))
	}
}

// Regression: the first full-size load failed with a duplicate charger primary key because depot
// names repeated across fleets. Every identity-bearing entity must be unique at production size.
func TestIdentitiesUniqueAtFullSize(t *testing.T) {
	w := mustGen(t, Config{Seed: 20260925})
	seen := map[string]string{}
	add := func(kind, id string) {
		if prev, dup := seen[id]; dup {
			t.Fatalf("%s id %s collides with %s", kind, id, prev)
		}
		seen[id] = kind
	}
	for _, f := range w.Fleets {
		add("fleet", f.ID.String())
	}
	depotNames := map[string]bool{}
	for _, d := range w.Depots {
		add("depot", d.ID.String())
		if depotNames[d.TenantID.String()+d.Name] {
			t.Fatalf("depot name %q repeated within tenant", d.Name)
		}
		depotNames[d.TenantID.String()+d.Name] = true
	}
	for _, c := range w.Chargers {
		add("charger", c.ID.String())
	}
	for _, d := range w.Drivers {
		add("driver", d.ID.String())
	}
}

func TestChargerInvariants(t *testing.T) {
	w := mustGen(t, Config{Seed: 3, Vehicles: 3000})
	depots := map[uuid.UUID]Depot{}
	for _, d := range w.Depots {
		depots[d.ID] = d
	}
	public := 0
	for _, c := range w.Chargers {
		if c.TenantID == nil {
			public++
			if c.DepotID != nil {
				t.Fatal("public charger with a depot")
			}
			continue
		}
		if c.DepotID == nil || depots[*c.DepotID].TenantID != *c.TenantID {
			t.Fatalf("private charger %s inconsistent", c.Name)
		}
		if len(c.Geohash) != 7 {
			t.Fatalf("geohash precision")
		}
	}
	if public != 600 {
		t.Fatalf("public chargers = %d, want 600", public)
	}
}

func TestEntitlementsAndUsers(t *testing.T) {
	w := mustGen(t, Config{Seed: 1, Vehicles: 100})
	copilot := map[string]bool{}
	for _, pf := range w.PlanFeatures {
		if pf.Feature == "copilot" {
			copilot[pf.Plan] = true
		}
	}
	if copilot["starter"] || copilot["pro"] || !copilot["enterprise"] {
		t.Fatal("copilot must be enterprise-only (used by entitlement tests)")
	}
	if len(w.Users) != 2*len(DemoRoles)+1 {
		t.Fatalf("users = %d", len(w.Users))
	}
	roles := map[string]bool{}
	for _, r := range w.Roles {
		roles[r] = true
	}
	for _, u := range w.Users {
		if !roles[u.Role] {
			t.Fatalf("user role %s not in roles", u.Role)
		}
	}
	if w.Tenants[len(w.Tenants)-1].ID != PlatformTenantID {
		t.Fatal("platform tenant must be last")
	}
}

// The committed manifest (db/seed/manifest.json) is what `make verify-seed` checks a loaded database
// against; it must be exactly what the generator produces for the default seed and size.
func TestCommittedManifestMatchesGenerator(t *testing.T) {
	raw, err := os.ReadFile("../../db/seed/manifest.json")
	if err != nil {
		t.Fatalf("manifest missing; run `go run ./cmd/vsdb manifest`: %v", err)
	}
	var committed Manifest
	if err := json.Unmarshal(raw, &committed); err != nil {
		t.Fatal(err)
	}
	w := mustGen(t, Config{Seed: 20260925, Vehicles: 100_000})
	if got := w.Manifest(); !reflect.DeepEqual(got, committed) {
		t.Fatalf("db/seed/manifest.json is stale (generator v%s); regenerate with `go run ./cmd/vsdb manifest`\ncommitted overall=%s\ngenerated overall=%s",
			GeneratorVersion, committed.Overall, got.Overall)
	}
}

func TestDigestIsOrderIndependent(t *testing.T) {
	if Digest([]string{"b", "a", "c"}) != Digest([]string{"c", "b", "a"}) {
		t.Fatal("digest must not depend on input order")
	}
	if Digest([]string{"a"}) == Digest([]string{"b"}) {
		t.Fatal("digest must depend on content")
	}
}
