package normalise

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
)

// G3.1 exhaustive: every valid textual code survives raw-encode then raw-decode.
func TestDTCRoundTripForEveryValidCode(t *testing.T) {
	count := 0
	for _, letter := range "PCBU" {
		for d := 0; d < 4; d++ {
			for h := 0; h < 0x1000; h++ {
				code := string(letter) + string(rune('0'+d)) + strings.ToUpper(hex3(h))
				raw, err := EncodeDTCRaw(code)
				if err != nil {
					t.Fatalf("%s: %v", code, err)
				}
				back, err := DecodeDTCRaw(raw)
				if err != nil || back != code {
					t.Fatalf("round trip %s -> %s -> %s (%v)", code, raw, back, err)
				}
				if !ValidDTC(code) {
					t.Fatalf("%s should be valid", code)
				}
				count++
			}
		}
	}
	if count != 4*4*4096 {
		t.Fatalf("covered %d codes", count)
	}
}

func hex3(v int) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[v>>8&15], digits[v>>4&15], digits[v&15]})
}

func TestDTCKnownCodesAndRejections(t *testing.T) {
	known := map[string]string{"0301": "P0301", "0A7F": "P0A7F", "C123": "U0123", "4100": "C0100", "8100": "B0100", "1234": "P1234", "3FFF": "P3FFF"}
	for raw, want := range known {
		got, err := DecodeDTCRaw(raw)
		if err != nil || got != want {
			t.Errorf("DecodeDTCRaw(%s) = %s, %v; want %s", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "030", "03011", "03G1", "zzzz", "0x01"} {
		if _, err := DecodeDTCRaw(raw); !errors.Is(err, ErrBadDTC) {
			t.Errorf("DecodeDTCRaw(%q) must fail with ErrBadDTC, got %v", raw, err)
		}
	}
	for _, code := range []string{"P0301", "U3FFF", "B0000"} {
		if !ValidDTC(code) {
			t.Errorf("%s should be valid", code)
		}
	}
	for _, code := range []string{"", "p0301", "X0301", "P4301", "P030", "P03011", "P03G1", "0301", "P 301"} {
		if ValidDTC(code) {
			t.Errorf("%q should be invalid", code)
		}
		if _, err := EncodeDTCRaw(code); !errors.Is(err, ErrBadDTC) {
			t.Errorf("EncodeDTCRaw(%q) must fail", code)
		}
	}
	list, err := ParseDTCList("0301" + "0A7F" + "C123")
	if err != nil || len(list) != 3 || list[2] != "U0123" {
		t.Fatalf("list: %v %v", list, err)
	}
	if l, err := ParseDTCList(""); err != nil || l != nil {
		t.Fatalf("empty list: %v %v", l, err)
	}
	if _, err := ParseDTCList("0301 0A"); err == nil {
		t.Fatal("ragged list accepted")
	}
}

const (
	vinOK = "1HGCM82633A004352"
	msA1  = `{"vehicle":{"vin":"1HGCM82633A004352"},"ts":"2026-10-01T05:30:02.120Z","pos":{"lat":21.1702,"lon":72.8311,"hdg":91.5},"spd":{"kmh":64.2},"batt":{"soc":0.41,"temp_c":33.5,"v":358.2,"a":-42.5,"odo_km":18234.7},"diag":{"dtc":["P0A7F"],"evt":"HARSH_BRAKE"},"chg":{"state":"CHARGING","charger":"PUB-1"},"amb_c":29.5,"seq":88412,"schema":1}`
	msA2  = `{"vehicle":{"vin":"1HGCM82633A004352"},"ts":"2026-10-01T05:30:02.120Z","pos":{"lat":21.1702,"lon":72.8311,"hdg":91.5},"spd":{"kph":64.2},"batt":{"soc":0.41,"temp_c":33.5,"v":358.2,"a":-42.5,"odo_km":18234.7,"soh_hint":null},"diag":{"dtc":["P0A7F"],"evt":"HARSH_BRAKE"},"chg":{"state":"CHARGING","charger":"PUB-1"},"amb_c":29.5,"seq":88412,"schema":2}`
	msB   = `{"VIN":"1HGCM82633A004352","time":1790832602120,"latitude":21.1702,"longitude":72.8311,"heading":91.5,"speedKmH":64.2,"socPct":41,"packTempC":33.5,"packV":358.2,"packA":-42.5,"odometerKm":18234.7,"dtcRaw":"0A7F","event":"HB","plug":2,"chargerId":"PUB-1","ambientC":29.5,"seq":88412,"v":1}`
)

// G3.2 both dialects (and the schema-v2 rename) describe the same observation identically.
func TestDialectsNormaliseToTheSameEvent(t *testing.T) {
	a1, err := ParseOEMA([]byte(msA1))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := ParseOEMA([]byte(msA2))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseOEMB([]byte(msB))
	if err != nil {
		t.Fatal(err)
	}
	if a1.SchemaVer != 1 || a2.SchemaVer != 2 || b.SchemaVer != 1 {
		t.Fatalf("schema versions: %d %d %d", a1.SchemaVer, a2.SchemaVer, b.SchemaVer)
	}
	for _, e := range []*telemetryv1.TelemetryEvent{a1, a2, b} {
		e.SchemaVer = 0
		e.Oem = "X"
	}
	if !proto.Equal(a1, a2) {
		t.Fatalf("schema v1 and v2 of OEM A differ:\n%v\n%v", a1, a2)
	}
	if !proto.Equal(a1, b) {
		t.Fatalf("OEM A and B differ for the same observation:\n%v\n%v", a1, b)
	}
	want := time.Date(2026, 10, 1, 5, 30, 2, 120_000_000, time.UTC)
	if !a1.Ts.AsTime().Equal(want) || a1.SocPct != 41 || a1.Evt != telemetryv1.Event_HARSH_BRAKE ||
		a1.ChargeState != telemetryv1.ChargeState_CHARGE_STATE_CHARGING || len(a1.Dtc) != 1 || a1.Dtc[0] != "P0A7F" {
		t.Fatalf("normalised content wrong: %v", a1)
	}
}

func TestDialectRejections(t *testing.T) {
	cases := map[string]struct {
		parse func([]byte) (*telemetryv1.TelemetryEvent, error)
		in    string
		class dlqv1.ErrorClass
	}{
		"A not json":       {ParseOEMA, `{"vehicle":`, dlqv1.ErrorClass_ERROR_CLASS_MALFORMED},
		"A truncated":      {ParseOEMA, msA1[:len(msA1)/2], dlqv1.ErrorClass_ERROR_CLASS_MALFORMED},
		"A unknown schema": {ParseOEMA, strings.Replace(msA1, `"schema":1`, `"schema":3`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"A missing vin":    {ParseOEMA, strings.Replace(msA1, `"vehicle":{"vin":"1HGCM82633A004352"},`, ``, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"A v2 without kph": {ParseOEMA, strings.Replace(msA2, `"kph"`, `"kmh"`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"A bad timestamp":  {ParseOEMA, strings.Replace(msA1, `2026-10-01T05:30:02.120Z`, `yesterday`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"A unknown event":  {ParseOEMA, strings.Replace(msA1, `HARSH_BRAKE`, `EXPLODED`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"A unknown charge": {ParseOEMA, strings.Replace(msA1, `"CHARGING"`, `"HOVERING"`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B not json":       {ParseOEMB, `not json`, dlqv1.ErrorClass_ERROR_CLASS_MALFORMED},
		"B wrong schema":   {ParseOEMB, strings.Replace(msB, `"v":1`, `"v":9`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B missing soc":    {ParseOEMB, strings.Replace(msB, `"socPct":41,`, ``, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B bad dtc":        {ParseOEMB, strings.Replace(msB, `"0A7F"`, `"0A7"`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B unknown event":  {ParseOEMB, strings.Replace(msB, `"HB"`, `"BOOM"`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B bad plug":       {ParseOEMB, strings.Replace(msB, `"plug":2`, `"plug":7`, 1), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"B type confusion": {ParseOEMB, strings.Replace(msB, `"socPct":41`, `"socPct":"41"`, 1), dlqv1.ErrorClass_ERROR_CLASS_MALFORMED},
	}
	for name, c := range cases {
		_, err := c.parse([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if got := ClassOf(err); got != c.class {
			t.Errorf("%s: class %v, want %v (%v)", name, got, c.class, err)
		}
	}
	if ClassOf(errors.New("other")) != dlqv1.ErrorClass_ERROR_CLASS_MALFORMED {
		t.Error("foreign errors must default to MALFORMED")
	}
}

func goodEvent(now time.Time) *telemetryv1.TelemetryEvent {
	return &telemetryv1.TelemetryEvent{Vin: vinOK, Ts: timestamppb.New(now.Add(-time.Second)), Lat: 13, Lon: 80, SpeedKmh: 40,
		SocPct: 55, OdoKm: 100, Seq: 5, Oem: "AURORA", SchemaVer: 1, Dtc: []string{"P0A7F"}}
}

func TestValidate(t *testing.T) {
	now := time.Now()
	if err := Validate(goodEvent(now), now, Limits{}); err != nil {
		t.Fatalf("good event rejected: %v", err)
	}
	mut := func(f func(*telemetryv1.TelemetryEvent)) *telemetryv1.TelemetryEvent {
		e := goodEvent(now)
		f(e)
		return e
	}
	cases := map[string]struct {
		e     *telemetryv1.TelemetryEvent
		class dlqv1.ErrorClass
	}{
		"bad check digit": {mut(func(e *telemetryv1.TelemetryEvent) { e.Vin = "1HGCM82633A004353" }), dlqv1.ErrorClass_ERROR_CLASS_VIN_INVALID},
		"I in vin":        {mut(func(e *telemetryv1.TelemetryEvent) { e.Vin = "1HGCM82I33A004352" }), dlqv1.ErrorClass_ERROR_CLASS_VIN_INVALID},
		"no timestamp":    {mut(func(e *telemetryv1.TelemetryEvent) { e.Ts = nil }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"future":          {mut(func(e *telemetryv1.TelemetryEvent) { e.Ts = timestamppb.New(now.Add(time.Hour)) }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"too old":         {mut(func(e *telemetryv1.TelemetryEvent) { e.Ts = timestamppb.New(now.Add(-30 * 24 * time.Hour)) }), dlqv1.ErrorClass_ERROR_CLASS_TOO_LATE},
		"seq zero":        {mut(func(e *telemetryv1.TelemetryEvent) { e.Seq = 0 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"no oem":          {mut(func(e *telemetryv1.TelemetryEvent) { e.Oem = "" }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"no schema":       {mut(func(e *telemetryv1.TelemetryEvent) { e.SchemaVer = 0 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"lat 91":          {mut(func(e *telemetryv1.TelemetryEvent) { e.Lat = 91 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"lon NaN":         {mut(func(e *telemetryv1.TelemetryEvent) { e.Lon = nan() }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"soc 101":         {mut(func(e *telemetryv1.TelemetryEvent) { e.SocPct = 101 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"soc negative":    {mut(func(e *telemetryv1.TelemetryEvent) { e.SocPct = -1 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"speed 400":       {mut(func(e *telemetryv1.TelemetryEvent) { e.SpeedKmh = 400 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"odo negative":    {mut(func(e *telemetryv1.TelemetryEvent) { e.OdoKm = -5 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"heading 400":     {mut(func(e *telemetryv1.TelemetryEvent) { e.HeadingDeg = 400 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"unknown event":   {mut(func(e *telemetryv1.TelemetryEvent) { e.Evt = 99 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"unknown charge":  {mut(func(e *telemetryv1.TelemetryEvent) { e.ChargeState = 9 }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
		"bad dtc":         {mut(func(e *telemetryv1.TelemetryEvent) { e.Dtc = []string{"P0A7F", "ZZZZZ"} }), dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID},
	}
	for name, c := range cases {
		err := Validate(c.e, now, Limits{})
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if got := ClassOf(err); got != c.class {
			t.Errorf("%s: class %v want %v (%v)", name, got, c.class, err)
		}
	}
}

func nan() float64 { z := 0.0; return z / z }
