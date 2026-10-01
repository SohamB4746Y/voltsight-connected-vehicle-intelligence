package rangerisk

import (
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
)

// Estimator turns the latest telemetry into a usable-range estimate in kilometres.
type Estimator interface {
	Name() string // recorded as model_version on every alert
	RangeKm(vs *VehicleState, ref VehicleRef, ev *telemetryv1.TelemetryEvent) float64
}

// VehicleState is the per-vehicle learning and alerting state kept by the processor.
type VehicleState struct {
	// consumption learning: %SoC per km over segments of at least minSegmentKm
	segOdo, segSoc float64
	haveSeg        bool
	pctPerKm       float64
	kmTrained      float64

	level   uint8 // current alert level: 0 ok, 1 low, 2 critical
	pending uint8 // consecutive evaluations pointing away from level
	// last alert window emitted per rule (unix seconds), for the once-per-window rule
	lastCritical, lastLow int64
}

const (
	minSegmentKm = 3.0
	ewmaAlpha    = 0.3
	// PctPerKmFloor guards against divide-by-zero for a vehicle that is barely moving.
	pctPerKmFloor = 0.02
)

// observe updates consumption learning with a telemetry sample.
func (vs *VehicleState) observe(ev *telemetryv1.TelemetryEvent) {
	if ev.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_PLUGGED || ev.ChargeState == telemetryv1.ChargeState_CHARGE_STATE_CHARGING {
		vs.haveSeg = false
		return
	}
	if !vs.haveSeg {
		vs.segOdo, vs.segSoc, vs.haveSeg = ev.OdoKm, float64(ev.SocPct), true
		return
	}
	d := ev.OdoKm - vs.segOdo
	if d < 0 { // odometer moved backwards: a corrupt sample or a swap; start over
		vs.segOdo, vs.segSoc = ev.OdoKm, float64(ev.SocPct)
		return
	}
	if d < minSegmentKm {
		return
	}
	drop := vs.segSoc - float64(ev.SocPct)
	if drop >= 0 {
		rate := drop / d
		if vs.kmTrained == 0 {
			vs.pctPerKm = rate
		} else {
			vs.pctPerKm = (1-ewmaAlpha)*vs.pctPerKm + ewmaAlpha*rate
		}
		vs.kmTrained += d
	}
	vs.segOdo, vs.segSoc = ev.OdoKm, float64(ev.SocPct)
}

// Baseline is the deliberately naive reference estimator: catalogue capacity and catalogue consumption. It
// knows nothing about battery ageing, temperature, payload or driving style, so it overestimates range on
// worn batteries and heavy loads. Every smarter estimator must beat it (G6.9, G8).
type Baseline struct{}

// Name implements Estimator.
func (Baseline) Name() string { return "baseline-catalogue-v1" }

// RangeKm implements Estimator.
func (Baseline) RangeKm(_ *VehicleState, ref VehicleRef, ev *telemetryv1.TelemetryEvent) float64 {
	if ref.KWhPerKm <= 0 {
		return 0
	}
	return float64(ref.NominalKWh) * float64(ev.SocPct) / 100 / float64(ref.KWhPerKm)
}

// EWMA learns each vehicle's own %SoC-per-km from its recent driving, which absorbs the battery's true
// capacity, ageing, load and driver style without knowing any of them. Until a vehicle has driven MinTrainedKm
// it falls back to the baseline.
type EWMA struct{ MinTrainedKm float64 }

// Name implements Estimator.
func (EWMA) Name() string { return "ewma-soc-per-km-v1" }

// RangeKm implements Estimator.
func (e EWMA) RangeKm(vs *VehicleState, ref VehicleRef, ev *telemetryv1.TelemetryEvent) float64 {
	min := e.MinTrainedKm
	if min == 0 {
		min = 6
	}
	if vs.kmTrained < min {
		return Baseline{}.RangeKm(vs, ref, ev)
	}
	rate := vs.pctPerKm
	if rate < pctPerKmFloor {
		rate = pctPerKmFloor
	}
	return float64(ev.SocPct) / rate
}
