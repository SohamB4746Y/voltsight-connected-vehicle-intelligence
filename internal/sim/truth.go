package sim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// truth writes the ground-truth files used to evaluate detections and models later: what actually
// happened in the simulated world, independent of what telemetry reported.
type truth struct {
	mu      sync.Mutex
	files   []*os.File
	trips   *bufio.Writer
	charges *bufio.Writer
	strands *bufio.Writer
}

func newTruth(dir string) (*truth, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	t := &truth{}
	open := func(name string) (*bufio.Writer, error) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		t.files = append(t.files, f)
		return bufio.NewWriterSize(f, 1<<20), nil
	}
	var err error
	if t.trips, err = open("trips.ndjson"); err != nil {
		return nil, err
	}
	if t.charges, err = open("charge_sessions.ndjson"); err != nil {
		return nil, err
	}
	if t.strands, err = open("strandings.ndjson"); err != nil {
		return nil, err
	}
	return t, nil
}

// write appends one JSON line; a nil truth (no TruthDir) discards everything.
func (t *truth) write(pick func(*truth) *bufio.Writer, v any) {
	if t == nil {
		return
	}
	b, _ := json.Marshal(v)
	t.mu.Lock()
	w := pick(t)
	w.Write(b) //nolint:errcheck // buffered; flush errors are reported by close
	w.WriteByte('\n')
	t.mu.Unlock()
}

func (t *truth) trip(v any)   { t.write(func(t *truth) *bufio.Writer { return t.trips }, v) }
func (t *truth) charge(v any) { t.write(func(t *truth) *bufio.Writer { return t.charges }, v) }
func (t *truth) strand(v any) { t.write(func(t *truth) *bufio.Writer { return t.strands }, v) }

func (t *truth) close() error {
	if t == nil {
		return nil
	}
	var first error
	for _, w := range []*bufio.Writer{t.trips, t.charges, t.strands} {
		if err := w.Flush(); err != nil && first == nil {
			first = err
		}
	}
	for _, f := range t.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// writeVehicles dumps the hidden per-vehicle parameters (true capacity, driver style...).
func (w *world) writeVehicleTruth(dir string) error {
	if dir == "" {
		return nil
	}
	f, err := os.Create(filepath.Join(dir, "vehicles.csv"))
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprintln(bw, "vin,tenant_id,model_id,true_soh,true_capacity_kwh,nominal_capacity_kwh,driver_style,payload_kg,unaware_driver,forgot_to_charge,fault_code,fault_at_s,shift_start_s")
	for i := range w.vehicles {
		v := &w.vehicles[i]
		fmt.Fprintf(bw, "%s,%s,%d,%.4f,%.3f,%.1f,%.3f,%.0f,%t,%t,%d,%d,%d\n", v.vin, v.tenant, v.model+1, v.soh, v.capKWh,
			w.models[v.model].batteryKWh, v.style, v.payloadKg, v.unaware, v.forgot, v.faultCode, v.faultAt, v.shiftStart)
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
