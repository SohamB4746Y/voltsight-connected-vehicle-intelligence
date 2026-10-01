package sim

import (
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used

	"github.com/google/uuid"

	"voltsight/internal/roadnet"
)

// Vehicle modes.
const (
	mParked uint8 = iota
	mDriving
	mDwell
	mToCharger
	mCharging
	mReturn
	mStranded
)

var dtcCodes = [...]string{"", "P0A7F", "P0A0D", "P0AA6"} // battery degradation / pack faults

type vehicle struct {
	idx     int32
	vin     string
	tenant  uuid.UUID
	depotID uuid.UUID
	city    int8
	model   int16
	dialect byte
	r, ir   *rand.Rand

	mode  uint8
	until int32 // dwell end (sim second)

	node, next, dest int32
	prog, edgeLen    float32 // metres along the current edge / its length
	edgeClass        uint8
	rowFirst         bool
	homeNode         int32
	speed, heading   float32 // km/h, degrees

	energy, capKWh, soh float32 // kWh in the pack, true usable capacity, true state of health (hidden)
	odo                 float64
	seq                 uint64

	shiftStart, shiftEnd int32
	tripsLeft            int8
	style, payloadKg     float32
	unaware, forgot      bool
	faultCode            uint8
	faultAt              int32

	chargerIdx    int32
	chargeTarget  float32
	chargeStartE  float32
	chargeStartT  int32
	chargeKW      float32
	tripStartT    int32
	tripStartNode int32
	tripKm        float32
	tripKWh       float32
	prevFrac      float32
	drawKW        float32 // traction power drawn during the last second
	nextEvt       uint8
	pendingEvt    uint8
	skewMs        int16
	phaseMs       int16
	outageMember  bool
}

func (v *vehicle) frac() float32 { return v.energy / v.capKWh }

// kwhPerKm is the true (hidden) energy use: speed profile x ambient temperature (HVAC) x payload x driver style.
func (w *world) kwhPerKm(v *vehicle, speedKmh, ambient float32) float32 {
	base := w.models[v.model].kwhPerKm
	s := float64(speedKmh)
	profile := float32((0.7 + 0.0035*s + 0.000045*s*s) / 0.9875)
	temp := 1 + 0.008*float32(math.Abs(float64(ambient)-22))
	return base * profile * temp * (1 + v.payloadKg/4000) * v.style
}

func (w *world) pickDestination(v *vehicle) {
	g := w.graphs[v.city]
	r, c := g.RC(v.node)
	blocks := int(2000/gridSpacingM + v.r.Float64()*(16000/gridSpacingM))
	dr, dc := v.r.IntN(2*blocks+1)-blocks, v.r.IntN(2*blocks+1)-blocks
	nr, nc := clampInt(r+dr, 0, g.N-1), clampInt(c+dc, 0, g.N-1)
	v.dest = g.ID(nr, nc)
	v.rowFirst = v.r.IntN(2) == 0
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// nextHop picks the next node on the Manhattan route toward dest (rows first or columns first).
func (v *vehicle) nextHop(g *roadnet.Graph) int32 {
	r, c := g.RC(v.node)
	dr, dc := g.RC(v.dest)
	moveRow := func() bool {
		if r == dr {
			return false
		}
		if dr > r {
			r++
		} else {
			r--
		}
		return true
	}
	moveCol := func() bool {
		if c == dc {
			return false
		}
		if dc > c {
			c++
		} else {
			c--
		}
		return true
	}
	if v.rowFirst {
		if !moveRow() {
			moveCol()
		}
	} else if !moveCol() {
		moveRow()
	}
	return g.ID(r, c)
}

func (w *world) startTrip(v *vehicle, now int) {
	w.pickDestination(v)
	v.mode = mDriving
	v.tripStartT, v.tripStartNode, v.tripKm, v.tripKWh = int32(now), v.node, 0, 0
}

func (w *world) startCharging(v *vehicle, now int, chargerIdx int32, target float32) {
	v.mode, v.chargerIdx, v.chargeTarget = mCharging, chargerIdx, target
	v.chargeStartE, v.chargeStartT = v.energy, int32(now)
	v.chargeKW = w.chargers[chargerIdx].kw
	v.speed = 0
	v.pendingEvt = evPlugIn
}

func (w *world) depotCharger(v *vehicle) int32 {
	cs := w.depotChargers[v.depotID]
	if len(cs) == 0 {
		return -1
	}
	return cs[int(v.idx)%len(cs)]
}

func (w *world) finishCharging(v *vehicle, now int) {
	c := &w.chargers[v.chargerIdx]
	w.truth.charge(map[string]any{"vin": v.vin, "charger": c.id, "start_s": v.chargeStartT, "end_s": now,
		"energy_start_kwh": v.chargeStartE, "energy_end_kwh": v.energy, "capacity_kwh": v.capKWh, "kwh": v.energy - v.chargeStartE})
	w.stats.ChargeSessions.Add(1)
	v.pendingEvt, v.nextEvt = evChargeComplete, evPlugOut
	v.chargerIdx = -1
	// back to work if the shift is still running; otherwise drive home (or park if already there)
	switch {
	case int32(now) < v.shiftEnd && v.tripsLeft > 0:
		w.startTrip(v, now)
	case v.node != v.homeNode:
		v.mode, v.dest = mReturn, v.homeNode
		v.tripStartT, v.tripStartNode, v.tripKm, v.tripKWh = int32(now), v.node, 0, 0
	default:
		v.mode = mParked
	}
	v.pendingEvt = evChargeComplete
}

func (w *world) arrive(v *vehicle, now int) {
	w.truth.trip(map[string]any{"vin": v.vin, "start_s": v.tripStartT, "end_s": now, "from": v.tripStartNode, "to": v.node,
		"km": v.tripKm, "kwh": v.tripKWh, "city": v.city})
	w.stats.Trips.Add(1)
	v.speed = 0
	switch v.mode {
	case mDriving:
		v.mode, v.until = mDwell, int32(now)+int32(120+v.r.IntN(360))
	case mReturn:
		v.mode = mParked
		v.pendingEvt = evIgnitionOff
	case mToCharger:
		w.startCharging(v, now, v.chargerIdx, 0.85)
	}
}

func (w *world) strand(v *vehicle, now int, g *roadnet.Graph) {
	lat, lon := float64(g.Lat[v.node]), float64(g.Lon[v.node])
	_, dist := w.nearestAvailableCharger(v, lat, lon)
	w.truth.strand(map[string]any{"vin": v.vin, "tenant_id": v.tenant, "t_s": now, "node": v.node, "lat": lat, "lon": lon,
		"nearest_available_charger_m": dist, "was_heading_to_charger": v.mode == mToCharger, "true_soh": v.soh,
		"unaware_driver": v.unaware})
	w.stats.Strandings.Add(1)
	v.mode, v.speed, v.energy = mStranded, 0, 0
}

// step advances one vehicle by one simulated second and fills the telemetry sample.
func (w *world) step(v *vehicle, now int, s *sample) {
	g := w.graphs[v.city]
	amb := w.amb[v.city]
	v.pendingEvt = 0
	if v.nextEvt != 0 {
		v.pendingEvt, v.nextEvt = v.nextEvt, 0
	}
	idle := float32(0.35 / 3600) // aux load kWh per second
	var powerKW float32          // + discharge, - charge (for the pack current)

	switch v.mode {
	case mParked:
		v.speed = 0
		v.energy -= idle
		switch {
		case int32(now) >= v.shiftStart && v.tripsLeft > 0 && int32(now) < v.shiftEnd && v.frac() > 0.1:
			w.startTrip(v, now)
			v.pendingEvt = evIgnitionOn
		case v.frac() < 0.95 && (!v.forgot || v.frac() <= 0.1) &&
			(int32(now) < v.shiftStart-900 || v.tripsLeft <= 0 || int32(now) >= v.shiftEnd || v.frac() <= 0.1):
			// plug in at the depot: before the shift, after it, or when too empty to leave
			// (drivers who "forgot" only plug in when the battery is nearly flat)
			if ci := w.depotCharger(v); ci >= 0 {
				w.startCharging(v, now, ci, 0.97)
			}
		}
	case mDwell:
		v.speed = 0
		v.energy -= idle
		if int32(now) >= v.until {
			v.tripsLeft--
			if v.tripsLeft <= 0 || int32(now) >= v.shiftEnd {
				v.dest, v.mode = v.homeNode, mReturn
				v.tripStartT, v.tripStartNode, v.tripKm, v.tripKWh = int32(now), v.node, 0, 0
			} else {
				w.startTrip(v, now)
			}
		}
	case mCharging:
		v.speed = 0
		frac := v.frac()
		taper := float32(1)
		if frac > 0.8 {
			taper = 1 - (frac-0.8)/0.2*0.85
		}
		p := minF(v.chargeKW, w.models[v.model].maxDCkW) * taper
		v.energy += p * 0.93 / 3600
		powerKW = -p
		if v.frac() >= v.chargeTarget || v.energy >= v.capKWh {
			w.finishCharging(v, now)
		}
	case mStranded:
		v.speed = 0
	default: // mDriving, mReturn, mToCharger
		v.drive(w, g, now, amb)
		powerKW = v.drawKW
	}

	if v.mode != mStranded && v.energy <= 0 {
		w.strand(v, now, g)
	}

	s.vin, s.oem = v.vin, w.models[v.model].oem
	frac := v.frac()
	if v.pendingEvt == 0 && v.prevFrac >= 0.2 && frac < 0.2 {
		v.pendingEvt = evLowSoC
	}
	if v.pendingEvt == 0 && v.mode == mDriving && v.r.Float64() < 0.0012 {
		v.pendingEvt = evHarshBrake
		v.speed *= 0.4
	}
	if v.pendingEvt == 0 && v.faultCode != 0 && int32(now) >= v.faultAt && v.r.Float64() < 0.002 {
		v.pendingEvt = evThermalWarning
	}
	v.prevFrac = frac
	s.evt = v.pendingEvt
	s.lat, s.lon = w.position(v, g)
	// GPS error: ~3 m noise, and a rare multipath jump of 200-500 m
	const mPerDeg = 111_320.0
	s.lat += v.r.NormFloat64() * 3 / mPerDeg
	s.lon += v.r.NormFloat64() * 3 / (mPerDeg * math.Cos(s.lat*math.Pi/180))
	if v.r.Float64() < 0.0005 {
		s.lat += (200 + 300*v.r.Float64()) / mPerDeg
	}
	s.speedKmh = v.speed
	s.heading = v.heading
	s.odoKm = v.odo
	s.ambientC = amb
	s.packTempC = amb + 8 + minF(14, absF(powerKW)/8)
	volts := 330 + 70*frac
	s.packV = volts
	s.packA = powerKW * 1000 / volts
	s.dtc = ""
	if v.faultCode != 0 && int32(now) >= v.faultAt {
		s.dtc = dtcCodes[v.faultCode]
	}
	switch {
	case v.mode == mCharging:
		s.chargeState, s.chargerID = chargeCharging, w.chargers[v.chargerIdx].id
	default:
		s.chargeState, s.chargerID = chargeNone, ""
	}
	// measurement: reported SoC has sensor noise and 0.1 % resolution
	soc := float64(frac)*100 + v.r.NormFloat64()*0.15
	s.socPct = float32(math.Round(math.Max(0, math.Min(100, soc))*10) / 10)
}

func (v *vehicle) drive(w *world, g *roadnet.Graph, now int, amb float32) {
	// low-battery decision every 5 s (aware drivers act at 22 %, unaware ones at 7 %)
	if v.mode != mToCharger && now%5 == 0 {
		thr := float32(0.22)
		if v.unaware {
			thr = 0.07
		}
		if v.frac() < thr {
			lat, lon := w.position(v, g)
			if ci, _ := w.nearestAvailableCharger(v, lat, lon); ci >= 0 {
				v.mode, v.chargerIdx, v.dest = mToCharger, ci, w.chargers[ci].node
				v.rowFirst = v.r.IntN(2) == 0
				v.tripStartT, v.tripStartNode, v.tripKm, v.tripKWh = int32(now), v.node, 0, 0
			}
		}
	}
	if v.next < 0 && v.node != v.dest {
		v.next = v.nextHop(g)
		v.edgeLen, v.edgeClass, _ = edge(g, v.node, v.next)
		v.prog = 0
	}
	target := roadnet.SpeedKmh(v.edgeClass) * float32(0.55+0.45*v.r.Float64())
	v.speed += (target - v.speed) * 0.35
	dist := v.speed / 3.6 // metres this second
	travelled := float32(0)
	for dist > 0 && v.node != v.dest {
		rem := v.edgeLen - v.prog
		if dist < rem {
			v.prog += dist
			travelled += dist
			dist = 0
			break
		}
		dist -= rem
		travelled += rem
		v.heading = bearing(g, v.node, v.next)
		v.node, v.next, v.prog = v.next, -1, 0
		if v.node == v.dest {
			break
		}
		v.next = v.nextHop(g)
		v.edgeLen, v.edgeClass, _ = edge(g, v.node, v.next)
	}
	km := travelled / 1000
	use := w.kwhPerKm(v, v.speed, amb)*km + 0.35/3600
	v.energy -= use
	v.odo += float64(km)
	v.tripKm += km
	v.tripKWh += use
	v.drawKW = use * 3600
	if v.node == v.dest {
		w.arrive(v, now)
	}
}

func edge(g *roadnet.Graph, a, b int32) (float32, uint8, bool) { return g.EdgeBetween(a, b) }

func bearing(g *roadnet.Graph, a, b int32) float32 {
	if b < 0 {
		return 0
	}
	dy := float64(g.Lat[b] - g.Lat[a])
	dx := float64(g.Lon[b]-g.Lon[a]) * math.Cos(float64(g.Lat[a])*math.Pi/180)
	deg := math.Atan2(dx, dy) * 180 / math.Pi
	if deg < 0 {
		deg += 360
	}
	return float32(deg)
}

// position interpolates along the current edge and adds GPS noise (rare jumps included).
func (w *world) position(v *vehicle, g *roadnet.Graph) (float64, float64) {
	lat, lon := float64(g.Lat[v.node]), float64(g.Lon[v.node])
	if v.next >= 0 && v.edgeLen > 0 {
		f := float64(v.prog / v.edgeLen)
		lat += (float64(g.Lat[v.next]) - lat) * f
		lon += (float64(g.Lon[v.next]) - lon) * f
	}
	return lat, lon
}

func minF(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func absF(a float32) float32 {
	if a < 0 {
		return -a
	}
	return a
}
