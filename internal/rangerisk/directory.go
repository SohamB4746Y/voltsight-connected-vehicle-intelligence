package rangerisk

import (
	"fmt"

	"voltsight/internal/roadnet"
	"voltsight/internal/seedgen"
)

// VehicleRef is the static reference data the estimator needs for a vehicle.
type VehicleRef struct {
	City       int
	NominalKWh float32 // catalogue battery capacity (new)
	KWhPerKm   float32 // catalogue (WLTP-like) consumption
}

// Directory maps VIN to its reference data.
type Directory map[string]VehicleRef

// FromWorld derives the directory and the charger list from the deterministic world, which is exactly what
// db/seed loads into PostgreSQL (the seed digests are verified against the database at G1). The hot path
// therefore needs no database round trip.
func FromWorld(w *seedgen.World, graphs []*roadnet.Graph) (Directory, []Charger, error) {
	cities := seedgen.Cities()
	cityOf := map[string]int{}
	for i, c := range cities {
		cityOf[c.Name] = i
	}
	depotCity := map[string]int{}
	for _, d := range w.Depots {
		ci, ok := cityOf[d.City]
		if !ok {
			return nil, nil, fmt.Errorf("depot %s in unknown city %q", d.Name, d.City)
		}
		depotCity[d.ID.String()] = ci
	}
	models := map[int16]seedgen.Model{}
	for _, m := range w.Models {
		models[m.ID] = m
	}
	dir := make(Directory, len(w.Vehicles))
	for _, v := range w.Vehicles {
		m := models[v.ModelID]
		dir[v.VIN] = VehicleRef{City: depotCity[v.HomeDepotID.String()], NominalKWh: float32(m.BatteryKWh), KWhPerKm: float32(m.KWhPer100 / 100)}
	}
	chargers := make([]Charger, len(w.Chargers))
	for i, c := range w.Chargers {
		city := int(c.TariffZone) - 1
		ch := Charger{ID: c.ID.String(), City: city, Node: graphs[city].NearestNode(c.Lat, c.Lon), Available: true}
		if c.TenantID != nil {
			ch.Tenant = c.TenantID.String()
		}
		chargers[i] = ch
	}
	return dir, chargers, nil
}
