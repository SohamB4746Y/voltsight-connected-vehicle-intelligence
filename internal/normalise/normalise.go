package normalise

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/vin"
)

// Error is a rejection with the DLQ class it should be filed under.
type Error struct {
	Class dlqv1.ErrorClass
	Msg   string
}

func (e *Error) Error() string { return e.Msg }

func bad(class dlqv1.ErrorClass, format string, a ...any) *Error {
	return &Error{Class: class, Msg: fmt.Sprintf(format, a...)}
}

// ClassOf returns the DLQ class of an error returned by this package (MALFORMED for anything else).
func ClassOf(err error) dlqv1.ErrorClass {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return dlqv1.ErrorClass_ERROR_CLASS_MALFORMED
}

// Limits bound what a plausible event looks like.
type Limits struct {
	MaxAge, MaxFuture time.Duration
	MaxSpeedKmh       float32
}

// DefaultLimits are used when a zero Limits is passed to Validate.
var DefaultLimits = Limits{MaxAge: 7 * 24 * time.Hour, MaxFuture: 5 * time.Minute, MaxSpeedKmh: 300}

var chargeByName = map[string]telemetryv1.ChargeState{
	"NONE": telemetryv1.ChargeState_CHARGE_STATE_NONE, "PLUGGED": telemetryv1.ChargeState_CHARGE_STATE_PLUGGED,
	"CHARGING": telemetryv1.ChargeState_CHARGE_STATE_CHARGING,
}

var evCodesB = map[string]telemetryv1.Event{
	"": telemetryv1.Event_EVENT_UNSPECIFIED, "HB": telemetryv1.Event_HARSH_BRAKE, "HA": telemetryv1.Event_HARSH_ACCEL,
	"OS": telemetryv1.Event_OVERSPEED, "IGN1": telemetryv1.Event_IGNITION_ON, "IGN0": telemetryv1.Event_IGNITION_OFF,
	"PLUGIN": telemetryv1.Event_PLUG_IN, "PLUGOUT": telemetryv1.Event_PLUG_OUT, "CHGDONE": telemetryv1.Event_CHARGE_COMPLETE,
	"LOWSOC": telemetryv1.Event_LOW_SOC, "THERM": telemetryv1.Event_THERMAL_WARNING,
}

func eventByName(name string) (telemetryv1.Event, bool) {
	if name == "" {
		return telemetryv1.Event_EVENT_UNSPECIFIED, true
	}
	v, ok := telemetryv1.Event_value[name]
	return telemetryv1.Event(v), ok
}

type aMsg struct {
	Vehicle struct {
		VIN *string `json:"vin"`
	} `json:"vehicle"`
	Ts  *string `json:"ts"`
	Pos struct {
		Lat *float64 `json:"lat"`
		Lon *float64 `json:"lon"`
		Hdg float64  `json:"hdg"`
	} `json:"pos"`
	Spd struct {
		Kmh *float64 `json:"kmh"` // schema v1
		Kph *float64 `json:"kph"` // schema v2
	} `json:"spd"`
	Batt struct {
		Soc   *float64 `json:"soc"` // 0..1
		TempC float64  `json:"temp_c"`
		V     float64  `json:"v"`
		A     float64  `json:"a"`
		OdoKm float64  `json:"odo_km"`
	} `json:"batt"`
	Diag struct {
		Dtc []string `json:"dtc"`
		Evt string   `json:"evt"`
	} `json:"diag"`
	Chg struct {
		State   string `json:"state"`
		Charger string `json:"charger"`
	} `json:"chg"`
	AmbC   float64 `json:"amb_c"`
	Seq    *uint64 `json:"seq"`
	Schema uint32  `json:"schema"`
}

// ParseOEMA decodes OEM A's nested snake_case dialect (schema v1 and v2).
func ParseOEMA(line []byte) (*telemetryv1.TelemetryEvent, error) {
	var m aMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_MALFORMED, "oem A: %v", err)
	}
	if m.Schema != 1 && m.Schema != 2 {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A: unsupported schema version %d", m.Schema)
	}
	if m.Vehicle.VIN == nil || m.Ts == nil || m.Pos.Lat == nil || m.Pos.Lon == nil || m.Batt.Soc == nil || m.Seq == nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A: missing required field (vin, ts, pos, batt.soc, seq)")
	}
	speed := m.Spd.Kmh
	if m.Schema >= 2 {
		speed = m.Spd.Kph
	}
	if speed == nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A schema %d: speed missing", m.Schema)
	}
	ts, err := time.Parse(time.RFC3339Nano, *m.Ts)
	if err != nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A: bad timestamp: %v", err)
	}
	evt, ok := eventByName(m.Diag.Evt)
	if !ok {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A: unknown event %q", m.Diag.Evt)
	}
	cs, ok := chargeByName[m.Chg.State]
	if !ok {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem A: unknown charge state %q", m.Chg.State)
	}
	return &telemetryv1.TelemetryEvent{
		Vin: *m.Vehicle.VIN, Ts: timestamppb.New(ts), Lat: *m.Pos.Lat, Lon: *m.Pos.Lon, SpeedKmh: float32(*speed),
		SocPct: float32(*m.Batt.Soc * 100), OdoKm: m.Batt.OdoKm, Dtc: m.Diag.Dtc, Evt: evt, Seq: *m.Seq,
		SchemaVer: m.Schema, HeadingDeg: float32(m.Pos.Hdg), PackTempC: float32(m.Batt.TempC), AmbientTempC: float32(m.AmbC),
		PackVoltageV: float32(m.Batt.V), PackCurrentA: float32(m.Batt.A), ChargeState: cs, ChargerId: m.Chg.Charger,
	}, nil
}

type bMsg struct {
	VIN       *string  `json:"VIN"`
	Time      *int64   `json:"time"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Heading   float64  `json:"heading"`
	SpeedKmH  *float64 `json:"speedKmH"`
	SocPct    *float64 `json:"socPct"`
	PackTempC float64  `json:"packTempC"`
	PackV     float64  `json:"packV"`
	PackA     float64  `json:"packA"`
	Odometer  float64  `json:"odometerKm"`
	DtcRaw    string   `json:"dtcRaw"`
	Event     string   `json:"event"`
	Plug      *int     `json:"plug"`
	ChargerID string   `json:"chargerId"`
	AmbientC  float64  `json:"ambientC"`
	Seq       *uint64  `json:"seq"`
	V         uint32   `json:"v"`
}

// ParseOEMB decodes OEM B's flat camelCase dialect: integer percent SoC, epoch-ms time, raw hex DTCs.
func ParseOEMB(line []byte) (*telemetryv1.TelemetryEvent, error) {
	var m bMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_MALFORMED, "oem B: %v", err)
	}
	if m.V != 1 {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem B: unsupported schema version %d", m.V)
	}
	if m.VIN == nil || m.Time == nil || m.Latitude == nil || m.Longitude == nil || m.SpeedKmH == nil || m.SocPct == nil || m.Plug == nil || m.Seq == nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem B: missing required field")
	}
	evt, ok := evCodesB[m.Event]
	if !ok {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem B: unknown event code %q", m.Event)
	}
	if *m.Plug < 0 || *m.Plug > 2 {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem B: plug state %d", *m.Plug)
	}
	dtc, err := ParseDTCList(m.DtcRaw)
	if err != nil {
		return nil, bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem B: %v", err)
	}
	return &telemetryv1.TelemetryEvent{
		Vin: *m.VIN, Ts: timestamppb.New(time.UnixMilli(*m.Time).UTC()), Lat: *m.Latitude, Lon: *m.Longitude,
		SpeedKmh: float32(*m.SpeedKmH), SocPct: float32(*m.SocPct), OdoKm: m.Odometer, Dtc: dtc, Evt: evt, Seq: *m.Seq,
		SchemaVer: m.V, HeadingDeg: float32(m.Heading), PackTempC: float32(m.PackTempC), AmbientTempC: float32(m.AmbientC),
		PackVoltageV: float32(m.PackV), PackCurrentA: float32(m.PackA),
		ChargeState: telemetryv1.ChargeState(*m.Plug + 1), ChargerId: m.ChargerID,
	}, nil
}

// Validate checks the normalised event against the contract. It does not look at tenant ownership
// (that needs the vehicle directory and is done by the gateway).
func Validate(e *telemetryv1.TelemetryEvent, now time.Time, l Limits) error {
	if l == (Limits{}) {
		l = DefaultLimits
	}
	if err := vin.Check(e.Vin); err != nil {
		return bad(dlqv1.ErrorClass_ERROR_CLASS_VIN_INVALID, "%v", err)
	}
	if e.Ts == nil || e.Ts.CheckValid() != nil {
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "missing or invalid timestamp")
	}
	t := e.Ts.AsTime()
	switch {
	case t.After(now.Add(l.MaxFuture)):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "timestamp %s is in the future", t.Format(time.RFC3339))
	case t.Before(now.Add(-l.MaxAge)):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_TOO_LATE, "timestamp %s is older than %s", t.Format(time.RFC3339), l.MaxAge)
	}
	switch {
	case e.Seq == 0:
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "seq must be positive")
	case e.Oem == "" || e.SchemaVer == 0:
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "oem and schema_ver are required")
	case !(e.Lat >= -90 && e.Lat <= 90) || !(e.Lon >= -180 && e.Lon <= 180):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "coordinates out of range")
	case !(e.SocPct >= 0 && e.SocPct <= 100):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "soc_pct %v out of range", e.SocPct)
	case !(e.SpeedKmh >= 0 && e.SpeedKmh <= l.MaxSpeedKmh):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "speed_kmh %v out of range", e.SpeedKmh)
	case !(e.OdoKm >= 0):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "odo_km negative")
	case !(e.HeadingDeg >= 0 && e.HeadingDeg <= 360):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "heading out of range")
	case e.Evt < 0 || int(e.Evt) >= len(telemetryv1.Event_name):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "unknown event %d", e.Evt)
	case e.ChargeState < 0 || int(e.ChargeState) >= len(telemetryv1.ChargeState_name):
		return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "unknown charge state %d", e.ChargeState)
	}
	for _, c := range e.Dtc {
		if !ValidDTC(c) {
			return bad(dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, "invalid DTC %q", c)
		}
	}
	return nil
}
