package sim

import (
	"fmt"
	"math"
	// Waiver (evidence/G2): seeded PRNG for deterministic synthetic data; not security-sensitive.
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"sync/atomic"

	"github.com/google/uuid"

	"voltsight/internal/roadnet"
	"voltsight/internal/seedgen"
)

const (
	gridN        = 220 // nodes per side: 48,400 nodes per city
	gridSpacingM = 150.0
)

// CityGraphs builds the road network of every city of the world for a behaviour seed. The simulator drives on
// these graphs and the range-risk engine computes distances on them, so both must call this with the same seed.
func CityGraphs(seed uint64) []*roadnet.Graph {
	cities := seedgen.Cities()
	out := make([]*roadnet.Graph, len(cities))
	for i, c := range cities {
		out[i] = roadnet.Generate(c.Name, c.Lat, c.Lon, gridN, gridSpacingM, seed^uint64(i+1)*0x9e3779b9)
	}
	return out
}

type modelInfo struct {
	oem        string
	dialect    byte // 'A' or 'B'
	batteryKWh float32
	kwhPerKm   float32 // WLTP-like baseline consumption
	maxDCkW    float32
}

type chargerInfo struct {
	id       string
	city     int
	node     int32
	kw       float32
	tenant   uuid.UUID
	isPublic bool
	avail    atomic.Uint32 // 1 = available
}

// world is the immutable physical setup plus the per-tick shared state (ambient temperature, charger
// availability) that is only written between ticks.
type world struct {
	cfg           Config
	seed          *seedgen.World
	cities        []seedgen.City
	graphs        []*roadnet.Graph
	models        []modelInfo
	chargers      []chargerInfo
	cityChargers  [][]int32
	depotChargers map[uuid.UUID][]int32
	vehicles      []vehicle
	amb           []float32 // ambient temperature per city for the current tick
	truth         *truth
	stats         *Stats
}

func newWorld(cfg Config, stats *Stats) (*world, error) {
	sw, err := seedgen.Generate(seedgen.Config{Seed: cfg.WorldSeed, Vehicles: cfg.Vehicles})
	if err != nil {
		return nil, err
	}
	w := &world{cfg: cfg, seed: sw, cities: seedgen.Cities(), stats: stats, depotChargers: map[uuid.UUID][]int32{}}
	cityByName := map[string]int{}
	for i, c := range w.cities {
		cityByName[c.Name] = i
	}
	w.graphs = CityGraphs(cfg.Seed)
	w.amb = make([]float32, len(w.cities))

	dialect := map[string]byte{}
	for _, o := range sw.OEMs {
		dialect[o.Code] = o.Dialect[0]
	}
	for _, m := range sw.Models {
		w.models = append(w.models, modelInfo{oem: m.OEM, dialect: dialect[m.OEM], batteryKWh: float32(m.BatteryKWh),
			kwhPerKm: float32(m.KWhPer100) / 100, maxDCkW: float32(m.MaxDCkW)})
	}

	w.cityChargers = make([][]int32, len(w.cities))
	w.chargers = make([]chargerInfo, len(sw.Chargers))
	for i, c := range sw.Chargers {
		city := int(c.TariffZone) - 1
		ci := &w.chargers[i]
		ci.id, ci.city, ci.kw, ci.isPublic = c.ID.String(), city, float32(c.PowerKW), c.TenantID == nil
		ci.node = w.graphs[city].NearestNode(c.Lat, c.Lon)
		ci.avail.Store(1)
		if c.TenantID != nil {
			ci.tenant = *c.TenantID
			w.depotChargers[*c.DepotID] = append(w.depotChargers[*c.DepotID], int32(i))
		}
		w.cityChargers[city] = append(w.cityChargers[city], int32(i))
	}

	depot := map[uuid.UUID]seedgen.Depot{}
	for _, d := range sw.Depots {
		depot[d.ID] = d
	}
	fleetOffset := func(f uuid.UUID) float64 { return (float64(f[0])/255 - 0.5) * 1800 } // +-15 min per fleet

	w.vehicles = make([]vehicle, len(sw.Vehicles))
	for i, sv := range sw.Vehicles {
		d := depot[sv.HomeDepotID]
		city, ok := cityByName[d.City]
		if !ok {
			return nil, fmt.Errorf("depot %s in unknown city %q", d.Name, d.City)
		}
		v := &w.vehicles[i]
		v.idx = int32(i)
		v.vin = sv.VIN
		v.tenant = sv.TenantID
		v.depotID = sv.HomeDepotID
		v.city = int8(city)
		v.model = int16(sv.ModelID - 1)
		v.r = rand.New(rand.NewPCG(cfg.Seed, uint64(i)*2+1))  // behaviour stream
		v.ir = rand.New(rand.NewPCG(cfg.Seed, uint64(i)*2+2)) // delivery-fault stream
		m := w.models[v.model]
		v.homeNode = w.graphs[city].NearestNode(d.Lat, d.Lon)
		v.node, v.next, v.dest = v.homeNode, -1, v.homeNode

		// hidden physics: state of health from age plus a few bad batteries, driver style, payload
		age := float64(2026-sv.CommissionedAt.Year()) + 0.5
		soh := 1 - age*0.025*(0.6+v.r.Float64())
		if v.r.Float64() < 0.03 {
			soh -= 0.08 + 0.1*v.r.Float64()
		}
		soh = math.Max(0.66, math.Min(1, soh))
		v.soh = float32(soh)
		v.capKWh = m.batteryKWh * v.soh
		v.style = 0.92 + 0.35*float32(v.r.Float64())
		v.payloadKg = float32(v.r.Float64() * 600)
		v.unaware = v.r.Float64() < cfg.UnawareDriverFraction
		v.forgot = v.r.Float64() < cfg.LowSoCStartFraction
		frac := 0.55 + 0.45*v.r.Float64()
		if v.forgot {
			frac = cfg.LowSoCStartMin + (cfg.LowSoCStartMax-cfg.LowSoCStartMin)*v.r.Float64() // left unplugged overnight
		}
		v.energy = float32(frac) * v.capKWh

		start := 21600 + fleetOffset(sv.FleetID) + v.r.NormFloat64()*1200 - float64(cfg.StartTOD)
		if start < 0 {
			start = v.r.Float64() * 300
		}
		v.shiftStart = int32(start)
		v.shiftEnd = v.shiftStart + int32(8*3600+v.r.IntN(2*3600))
		v.tripsLeft = int8(6 + v.r.IntN(7))
		if v.r.Float64() < cfg.FaultFraction {
			v.faultCode = uint8(1 + v.r.IntN(3))
			v.faultAt = int32(v.r.IntN(3 * 3600))
		}
		v.skewMs = int16(v.r.NormFloat64() * 150)
		v.phaseMs = int16(v.r.IntN(1000))
		v.seq = cfg.SeqBase + uint64(1000+v.r.IntN(9000))
		v.odo = 5000 + v.r.Float64()*60000
		v.outageMember = hash01(sv.VIN) < cfg.OutageFraction
		v.dialect = m.dialect
		v.prevFrac = float32(frac)
	}
	return w, nil
}

func hash01(s string) float64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	return float64(h>>11) / (1 << 53)
}

var cityBaseTemp = []float32{30, 24, 31} // Chennai, Bengaluru, Surat

func (w *world) updateAmbient(now int) {
	tod := float64((w.cfg.StartTOD + now) % 86400)
	diurnal := float32(4 * math.Sin(2*math.Pi*(tod-9*3600)/86400))
	for i := range w.amb {
		w.amb[i] = cityBaseTemp[i%len(cityBaseTemp)] + diurnal
	}
}

// nearestAvailableCharger returns the index of the closest available charger usable by the vehicle's
// tenant (public or its own depot chargers), or -1.
func (w *world) nearestAvailableCharger(v *vehicle, lat, lon float64) (int32, float64) {
	best, bestD := int32(-1), math.MaxFloat64
	g := w.graphs[v.city]
	for _, ci := range w.cityChargers[v.city] {
		c := &w.chargers[ci]
		if c.avail.Load() == 0 || (!c.isPublic && c.tenant != v.tenant) {
			continue
		}
		d := roadnet.DistM(lat, lon, float64(g.Lat[c.node]), float64(g.Lon[c.node]))
		if d < bestD {
			best, bestD = ci, d
		}
	}
	return best, bestD
}
