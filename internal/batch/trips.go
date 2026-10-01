package batch

import (
	"math"
	"time"
)

// Trip is a contiguous drive.
type Trip struct {
	VIN                                string
	Start, End                         time.Time
	DistanceKm                         float64
	SoCDrop                            float64 // percentage points
	StartLat, StartLon, EndLat, EndLon float64
}

// TripSegmenter splits a vehicle's samples into trips with hysteresis: moving starts a trip as soon as speed
// exceeds MovingKmh, and the trip ends only after the vehicle has stayed below it for MinDwell. Short stops
// (a red light, a delivery drop of a minute) therefore do not split a trip, and GPS speed noise at standstill
// does not create phantom trips. Feed samples of ONE vehicle in time order.
type TripSegmenter struct {
	MovingKmh float64       // default 3
	MinDwell  time.Duration // default 90 s
	MinKm     float64       // trips shorter than this are dropped as noise (default 0.2)

	cur     *Trip
	startOd float64
	soc0    float64
	stopAt  time.Time
	lastMov Sample
	out     []Trip
}

func (g *TripSegmenter) defaults() {
	if g.MovingKmh == 0 {
		g.MovingKmh = 3
	}
	if g.MinDwell == 0 {
		g.MinDwell = 90 * time.Second
	}
	if g.MinKm == 0 {
		g.MinKm = 0.2
	}
}

// Feed consumes a sample.
func (g *TripSegmenter) Feed(s Sample) {
	g.defaults()
	moving := s.SpeedKmh > g.MovingKmh && !s.Charging
	switch {
	case moving && g.cur == nil:
		g.cur = &Trip{VIN: s.VIN, Start: s.TS, StartLat: s.Lat, StartLon: s.Lon}
		g.startOd, g.soc0 = s.OdoKm, s.SoC
		g.lastMov, g.stopAt = s, time.Time{}
	case moving:
		g.lastMov, g.stopAt = s, time.Time{}
	case g.cur != nil:
		if g.stopAt.IsZero() {
			g.stopAt = s.TS
		}
		if s.TS.Sub(g.stopAt) >= g.MinDwell || s.Charging {
			g.finish()
		}
	}
}

func (g *TripSegmenter) finish() {
	t := g.cur
	g.cur = nil
	t.End = g.lastMov.TS
	t.EndLat, t.EndLon = g.lastMov.Lat, g.lastMov.Lon
	t.DistanceKm = g.lastMov.OdoKm - g.startOd
	t.SoCDrop = g.soc0 - g.lastMov.SoC
	if t.DistanceKm >= g.MinKm {
		g.out = append(g.out, *t)
	}
}

// Finish closes an open trip and returns all trips of the vehicle.
func (g *TripSegmenter) Finish() []Trip {
	if g.cur != nil {
		g.finish()
	}
	out := g.out
	g.out = nil
	return out
}

// DouglasPeucker simplifies a polyline (lat/lon) to within epsilonM metres: O(n log n) on average.
func DouglasPeucker(pts [][2]float64, epsilonM float64) [][2]float64 {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	var rec func(lo, hi int)
	rec = func(lo, hi int) {
		maxD, idx := 0.0, -1
		for i := lo + 1; i < hi; i++ {
			if d := perpM(pts[i], pts[lo], pts[hi]); d > maxD {
				maxD, idx = d, i
			}
		}
		if idx >= 0 && maxD > epsilonM {
			keep[idx] = true
			rec(lo, idx)
			rec(idx, hi)
		}
	}
	rec(0, len(pts)-1)
	var out [][2]float64
	for i, k := range keep {
		if k {
			out = append(out, pts[i])
		}
	}
	return out
}

// perpM is the distance in metres from p to the segment a-b (equirectangular projection).
func perpM(p, a, b [2]float64) float64 {
	const m = 111_320.0
	cos := math.Cos(a[0] * math.Pi / 180)
	px, py := (p[1]-a[1])*m*cos, (p[0]-a[0])*m
	bx, by := (b[1]-a[1])*m*cos, (b[0]-a[0])*m
	l2 := bx*bx + by*by
	if l2 == 0 {
		return math.Hypot(px, py)
	}
	t := math.Max(0, math.Min(1, (px*bx+py*by)/l2))
	return math.Hypot(px-t*bx, py-t*by)
}
