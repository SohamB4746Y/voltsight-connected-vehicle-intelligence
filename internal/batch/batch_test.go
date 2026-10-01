package batch

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/sim"
)

// collect runs the simulator in-process and gathers per-vehicle sample streams.
type collector struct {
	mu  sync.Mutex
	vin map[string][]Sample
}

func (c *collector) Emit(_ int, _ int, batch []sim.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range batch {
		if m.Kind != sim.KindTelemetry {
			continue
		}
		var ev telemetryv1.TelemetryEvent
		if proto.Unmarshal(m.Payload, &ev) != nil {
			continue
		}
		cs := ev.ChargeState
		c.vin[ev.Vin] = append(c.vin[ev.Vin], Sample{VIN: ev.Vin, TS: ev.Ts.AsTime(), SoC: float64(ev.SocPct), SpeedKmh: float64(ev.SpeedKmh),
			Lat: ev.Lat, Lon: ev.Lon, OdoKm: ev.OdoKm, PackV: float64(ev.PackVoltageV), PackA: float64(ev.PackCurrentA),
			Charging: cs == telemetryv1.ChargeState_CHARGE_STATE_CHARGING, Plugged: cs == telemetryv1.ChargeState_CHARGE_STATE_PLUGGED})
	}
	return nil
}

func runSim(t *testing.T, vehicles, hours int) (*collector, string) {
	dir := t.TempDir()
	c := &collector{vin: map[string][]Sample{}}
	var st sim.Stats
	cfg := sim.Config{Seed: 5, Vehicles: vehicles, Duration: hours * 3600, StartTOD: 5*3600 + 30*60, Shards: 4, TruthDir: dir, LowSoCStartFraction: 0.3}
	if err := sim.Run(context.Background(), cfg, c, &st); err != nil {
		t.Fatal(err)
	}
	for v := range c.vin {
		s := c.vin[v]
		sort.Slice(s, func(i, j int) bool { return s[i].TS.Before(s[j].TS) })
	}
	return c, dir
}

// AL / T: the SoH estimator recovers each battery's true usable capacity from its charging sessions.
func TestSoHRecoversTrueCapacity(t *testing.T) {
	c, dir := runSim(t, 400, 6)
	f, _ := os.Open(filepath.Join(dir, "vehicles.csv"))
	rows, _ := csv.NewReader(f).ReadAll()
	truth := map[string]float64{}
	for _, r := range rows[1:] {
		truth[r[0]], _ = strconv.ParseFloat(r[4], 64)
	}
	var errs []float64
	var withEstimate int
	for vin, samples := range c.vin {
		var e SoHEstimator
		for _, s := range samples {
			e.Feed(s)
		}
		if capKWh, _, ok := e.Finish(); ok {
			withEstimate++
			errs = append(errs, math.Abs(capKWh-truth[vin])/truth[vin])
		}
	}
	if withEstimate < 30 {
		t.Fatalf("only %d vehicles had a usable charging session", withEstimate)
	}
	sort.Float64s(errs)
	mape := 0.0
	for _, e := range errs {
		mape += e
	}
	mape /= float64(len(errs))
	p90 := errs[int(0.9*float64(len(errs)-1))]
	t.Logf("MEASURED (simulated fleet): capacity estimated for %d/%d vehicles; MAPE %.2f%%, p90 error %.2f%% (assumed charge efficiency %.2f)", withEstimate, len(c.vin), mape*100, p90*100, ChargeEfficiency)
	if mape > 0.05 {
		t.Fatalf("capacity MAPE %.1f%% too high", mape*100)
	}
}

type truthTrip struct {
	VIN   string  `json:"vin"`
	Start int     `json:"start_s"`
	End   int     `json:"end_s"`
	Km    float64 `json:"km"`
}

// AL4: trip segmentation of noisy samples against the simulator's trips (F1 on matched trips).
func TestTripSegmentationAgainstGroundTruth(t *testing.T) {
	c, dir := runSim(t, 300, 4)
	origin := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(5*time.Hour + 30*time.Minute)
	f, _ := os.Open(filepath.Join(dir, "trips.ndjson"))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	truth := map[string][]truthTrip{}
	n := 0
	for sc.Scan() {
		var tt truthTrip
		if json.Unmarshal(sc.Bytes(), &tt) == nil && tt.Km >= 0.5 {
			truth[tt.VIN] = append(truth[tt.VIN], tt)
			n++
		}
	}
	var tp, found int
	for vin, samples := range c.vin {
		var g TripSegmenter
		for _, s := range samples {
			g.Feed(s)
		}
		trips := g.Finish()
		for _, tr := range trips {
			if tr.DistanceKm >= 0.5 {
				found++
			}
		}
		for _, tt := range truth[vin] {
			ts, te := origin.Add(time.Duration(tt.Start)*time.Second), origin.Add(time.Duration(tt.End)*time.Second)
			for _, tr := range trips {
				// matched when the intervals overlap by at least half of the true trip
				lo, hi := maxT(ts, tr.Start), minT(te, tr.End)
				if hi.Sub(lo) >= te.Sub(ts)/2 {
					tp++
					break
				}
			}
		}
	}
	prec, rec := float64(tp)/float64(found), float64(tp)/float64(n)
	f1 := 2 * prec * rec / (prec + rec)
	t.Logf("MEASURED (simulated fleet): %d true trips (>=0.5 km), %d detected; precision %.3f recall %.3f F1 %.3f", n, found, prec, rec, f1)
	if f1 < 0.8 {
		t.Fatalf("F1 %.2f below 0.8", f1)
	}
}

func maxT(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minT(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// AL2: clustering finds planted unapproved stop locations and ignores approved depots and scattered noise.
func TestClusterStopsFindsPlantedClusters(t *testing.T) {
	var stops []Stop
	add := func(vin string, lat, lon float64, n int) {
		for i := 0; i < n; i++ {
			stops = append(stops, Stop{VIN: vin + strconv.Itoa(i%7), Lat: lat + float64(i%5)*0.0002, Lon: lon + float64(i%3)*0.0002})
		}
	}
	add("A", 13.0500, 80.2500, 40) // a planted unapproved cluster spanning neighbouring cells
	add("B", 12.9700, 77.5900, 25) // another
	add("D", 13.1000, 80.3000, 60) // an approved depot
	for i := 0; i < 30; i++ {      // scattered one-off stops
		stops = append(stops, Stop{VIN: "N" + strconv.Itoa(i), Lat: 11 + float64(i)*0.05, Lon: 79 + float64(i)*0.05})
	}
	approved := map[string]bool{}
	for _, s := range stops {
		if s.VIN[0] == 'D' {
			approved[geoEncode(s.Lat, s.Lon, 7)] = true
		}
	}
	got := ClusterStops(stops, 7, 3, approved)
	if len(got) != 2 {
		t.Fatalf("found %d clusters, want the 2 planted ones: %+v", len(got), got)
	}
	if got[0].Stops != 40 || got[1].Stops != 25 {
		t.Fatalf("cluster sizes %d, %d", got[0].Stops, got[1].Stops)
	}
}

func TestDouglasPeuckerKeepsShapeAndEnds(t *testing.T) {
	var pts [][2]float64
	for i := 0; i <= 100; i++ { // an L-shaped route with 1 m noise
		if i <= 50 {
			pts = append(pts, [2]float64{13 + float64(i)*0.0001, 80 + 0.000005*float64(i%2)})
		} else {
			pts = append(pts, [2]float64{13.005, 80 + float64(i-50)*0.0001})
		}
	}
	out := DouglasPeucker(pts, 10)
	if len(out) < 3 || len(out) > 6 {
		t.Fatalf("simplified to %d points", len(out))
	}
	if out[0] != pts[0] || out[len(out)-1] != pts[len(pts)-1] {
		t.Fatal("end points must be kept")
	}
}
