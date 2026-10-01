// Package batch holds the historical analytics: battery state-of-health from charging sessions, trip
// segmentation from noisy GPS/speed, and clustering of stops at unapproved locations. Each is a pure
// streaming function over time-ordered samples, so the same code runs over ClickHouse rows in production and
// over the simulator in tests (where the ground truth is known).
package batch

import (
	"math"
	"sort"
	"time"
)

// Sample is the subset of a telemetry row the analytics need.
type Sample struct {
	VIN      string
	TS       time.Time
	SoC      float64 // %
	SpeedKmh float64
	Lat, Lon float64
	OdoKm    float64
	PackV    float64
	PackA    float64 // negative while charging
	Charging bool
	Plugged  bool
}

// ChargeEfficiency is the assumed share of grid energy that ends up in the battery (charger + battery
// losses). A real deployment would calibrate it per charger type; the estimate scales linearly with it.
const ChargeEfficiency = 0.93

// Session is one charging session.
type Session struct {
	VIN         string
	Start, End  time.Time
	SoC0, SoC1  float64
	GridKWh     float64
	CapacityKWh float64 // implied usable capacity
	Usable      bool
}

// SoHEstimator turns charging samples into capacity estimates. Feed samples of ONE vehicle in time order.
type SoHEstimator struct {
	MinSoCRise float64       // sessions that raise SoC by less than this are too noisy to use (default 15 points)
	MaxGap     time.Duration // a gap longer than this ends a session (default 30 s)

	cur      *Session
	last     Sample
	sessions []Session
}

func (e *SoHEstimator) defaults() {
	if e.MinSoCRise == 0 {
		e.MinSoCRise = 15
	}
	if e.MaxGap == 0 {
		e.MaxGap = 30 * time.Second
	}
}

// Feed consumes a sample.
func (e *SoHEstimator) Feed(s Sample) {
	e.defaults()
	if !s.Charging {
		e.close()
		e.last = s
		return
	}
	if e.cur != nil && s.TS.Sub(e.last.TS) > e.MaxGap {
		e.close()
	}
	if e.cur == nil {
		e.cur = &Session{VIN: s.VIN, Start: s.TS, SoC0: s.SoC}
	} else {
		dt := s.TS.Sub(e.last.TS).Seconds()
		if dt > 0 && dt <= e.MaxGap.Seconds() {
			e.cur.GridKWh += math.Abs(s.PackV*s.PackA) / 1000 * dt / 3600
		}
	}
	e.cur.End, e.cur.SoC1 = s.TS, s.SoC
	e.last = s
}

func (e *SoHEstimator) close() {
	if e.cur == nil {
		return
	}
	c := *e.cur
	e.cur = nil
	if rise := c.SoC1 - c.SoC0; rise >= e.MinSoCRise && c.GridKWh > 0 {
		c.CapacityKWh = ChargeEfficiency * c.GridKWh / (rise / 100)
		c.Usable = true
	}
	e.sessions = append(e.sessions, c)
}

// Finish closes the open session and returns the vehicle's capacity estimate: the median over usable
// sessions (robust against a session with a bad sensor). ok is false without any usable session.
func (e *SoHEstimator) Finish() (capacityKWh float64, usableSessions int, ok bool) {
	e.close()
	var caps []float64
	for _, s := range e.sessions {
		if s.Usable {
			caps = append(caps, s.CapacityKWh)
		}
	}
	e.sessions = nil
	if len(caps) == 0 {
		return 0, 0, false
	}
	sort.Float64s(caps)
	n := len(caps)
	if n%2 == 1 {
		return caps[n/2], n, true
	}
	return (caps[n/2-1] + caps[n/2]) / 2, n, true
}
