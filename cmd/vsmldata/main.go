// Command vsmldata builds the training/evaluation table for the consumption model from a simulated fleet:
// one row per trip with features that a deployed system could observe (catalogue data, time, the vehicle's
// own earlier trips as reconstructed from telemetry) and the simulator's true energy as the label.
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/batch"
	"voltsight/internal/seedgen"
	"voltsight/internal/sim"
)

type sink struct {
	mu   []sync.Mutex
	segs []map[string]*batch.TripSegmenter
}

func (s *sink) Emit(shard int, _ int, b []sim.Message) error {
	if shard < 0 {
		return nil
	}
	s.mu[shard].Lock()
	defer s.mu[shard].Unlock()
	for _, m := range b {
		if m.Kind != sim.KindTelemetry {
			continue
		}
		var ev telemetryv1.TelemetryEvent
		if proto.Unmarshal(m.Payload, &ev) != nil {
			continue
		}
		g := s.segs[shard][ev.Vin]
		if g == nil {
			g = &batch.TripSegmenter{}
			s.segs[shard][ev.Vin] = g
		}
		cs := ev.ChargeState
		g.Feed(batch.Sample{VIN: ev.Vin, TS: ev.Ts.AsTime(), SoC: float64(ev.SocPct), SpeedKmh: float64(ev.SpeedKmh), Lat: ev.Lat, Lon: ev.Lon,
			OdoKm: ev.OdoKm, Charging: cs == telemetryv1.ChargeState_CHARGE_STATE_CHARGING, Plugged: cs == telemetryv1.ChargeState_CHARGE_STATE_PLUGGED})
	}
	return nil
}

func main() {
	var (
		vehicles = flag.Int("vehicles", 2500, "fleet size")
		hours    = flag.Float64("hours", 9, "simulated hours")
		seed     = flag.Uint64("seed", 7, "behaviour seed")
		out      = flag.String("out", "ml/data/trips.csv", "output CSV")
		truth    = flag.String("truth-dir", "tmp/ml-truth", "ground-truth directory")
	)
	flag.Parse()
	const shards = 8
	s := &sink{mu: make([]sync.Mutex, shards), segs: make([]map[string]*batch.TripSegmenter, shards)}
	for i := range s.segs {
		s.segs[i] = map[string]*batch.TripSegmenter{}
	}
	cfg := sim.Config{Seed: *seed, Vehicles: *vehicles, Duration: int(*hours * 3600), StartTOD: 5*3600 + 30*60, Shards: shards, TruthDir: *truth,
		LowSoCStartFraction: 0.15}
	var st sim.Stats
	must(sim.Run(context.Background(), cfg, s, &st))
	origin := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(cfg.StartTOD) * time.Second)

	sw, err := seedgen.Generate(seedgen.Config{Seed: 20260925, Vehicles: *vehicles})
	must(err)
	models := map[int16]seedgen.Model{}
	for _, m := range sw.Models {
		models[m.ID] = m
	}
	type vinfo struct {
		model int16
		age   float64
		city  string
	}
	info := map[string]vinfo{}
	depotCity := map[string]string{}
	for _, d := range sw.Depots {
		depotCity[d.ID.String()] = d.City
	}
	for _, v := range sw.Vehicles {
		info[v.VIN] = vinfo{v.ModelID, 2026.75 - float64(v.CommissionedAt.Year()) - float64(v.CommissionedAt.YearDay())/365, depotCity[v.HomeDepotID.String()]}
	}

	// truth trips
	type tt struct {
		VIN   string  `json:"vin"`
		Start int     `json:"start_s"`
		End   int     `json:"end_s"`
		Km    float64 `json:"km"`
		KWh   float64 `json:"kwh"`
	}
	truthTrips := map[string][]tt{}
	f, err := os.Open(*truth + "/trips.ndjson")
	must(err)
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	for dec.More() {
		var t tt
		if dec.Decode(&t) != nil {
			break
		}
		truthTrips[t.VIN] = append(truthTrips[t.VIN], t)
	}
	f.Close()

	must(os.MkdirAll("ml/data", 0o755))
	w, err := os.Create(*out)
	must(err)
	cw := csv.NewWriter(w)
	must(cw.Write([]string{"vin", "model_id", "nominal_kwh", "wltp_kwh_km", "age_years", "city", "start_hour", "km", "obs_kwh_km", "prior_mean", "prior_last", "n_prior", "true_kwh_km", "start_s"}))
	rows := 0
	for sh := range s.segs {
		for vin, g := range s.segs[sh] {
			m := models[info[vin].model]
			nominal, wltp := m.BatteryKWh, m.KWhPer100/100
			trips := g.Finish()
			sort.Slice(trips, func(i, j int) bool { return trips[i].Start.Before(trips[j].Start) })
			var priorSum float64
			var nPrior int
			last := 0.0
			for _, tr := range trips {
				if tr.DistanceKm < 1.0 {
					continue
				}
				obs := tr.SoCDrop / 100 * nominal / tr.DistanceKm
				// the true energy of the simulator trip that overlaps this one the most
				var best *tt
				var bestOv time.Duration
				for i := range truthTrips[vin] {
					x := &truthTrips[vin][i]
					lo, hi := maxT(origin.Add(time.Duration(x.Start)*time.Second), tr.Start), minT(origin.Add(time.Duration(x.End)*time.Second), tr.End)
					if ov := hi.Sub(lo); ov > bestOv {
						best, bestOv = x, ov
					}
				}
				if best == nil || best.Km < 1 || bestOv < (tr.End.Sub(tr.Start))/2 {
					continue
				}
				pm := wltp
				if nPrior > 0 {
					pm = priorSum / float64(nPrior)
				}
				pl := wltp
				if nPrior > 0 {
					pl = last
				}
				must(cw.Write([]string{vin, strconv.Itoa(int(info[vin].model)), f2(nominal), f4(wltp), f2(info[vin].age), info[vin].city, strconv.Itoa(tr.Start.Hour()),
					f2(tr.DistanceKm), f4(obs), f4(pm), f4(pl), strconv.Itoa(nPrior), f4(best.KWh / best.Km), strconv.Itoa(int(tr.Start.Sub(origin).Seconds()))}))
				rows++
				priorSum += obs
				nPrior++
				last = obs
			}
		}
	}
	cw.Flush()
	must(cw.Error())
	must(w.Close())
	fmt.Printf("wrote %d trips from %d vehicles to %s\n", rows, *vehicles, *out)
}

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
func f4(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

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

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsmldata:", err)
		os.Exit(1)
	}
}
