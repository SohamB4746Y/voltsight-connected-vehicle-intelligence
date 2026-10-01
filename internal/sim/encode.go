package sim

import (
	"math"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Event codes mirror voltsight.telemetry.v1.Event.
const (
	evNone           = 0
	evHarshBrake     = 1
	evHarshAccel     = 2
	evOverspeed      = 3
	evIgnitionOn     = 4
	evIgnitionOff    = 5
	evPlugIn         = 6
	evPlugOut        = 7
	evChargeComplete = 8
	evLowSoC         = 9
	evThermalWarning = 10
	chargeNone       = 1
	chargePlugged    = 2
	chargeCharging   = 3
)

var evNames = [...]string{"", "HARSH_BRAKE", "HARSH_ACCEL", "OVERSPEED", "IGNITION_ON", "IGNITION_OFF", "PLUG_IN",
	"PLUG_OUT", "CHARGE_COMPLETE", "LOW_SOC", "THERMAL_WARNING"}

// OEM B event codes.
var evCodesB = [...]string{"", "HB", "HA", "OS", "IGN1", "IGN0", "PLUGIN", "PLUGOUT", "CHGDONE", "LOWSOC", "THERM"}

var chargeNames = [...]string{"", "NONE", "PLUGGED", "CHARGING"}

// sample is one telemetry observation before encoding.
type sample struct {
	vin          string
	oem          string
	schemaVer    uint32
	tsMs         int64
	lat, lon     float64
	speedKmh     float32
	socPct       float32
	odoKm        float64
	dtc          string // single active code ("" = none), e.g. "P0A7F"
	evt          uint8
	seq          uint64
	heading      float32
	packTempC    float32
	ambientC     float32
	packV, packA float32
	chargeState  uint8
	chargerID    string
}

func appendTimestamp(b []byte, num protowire.Number, ms int64) []byte {
	sec, nanos := ms/1000, (ms%1000)*1_000_000
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(sec))
	if nanos != 0 {
		inner = protowire.AppendTag(inner, 2, protowire.VarintType)
		inner = protowire.AppendVarint(inner, uint64(nanos))
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, inner)
}

func appendF32(b []byte, num protowire.Number, f float32) []byte {
	if f == 0 {
		return b // proto3 omits defaults
	}
	b = protowire.AppendTag(b, num, protowire.Fixed32Type)
	return protowire.AppendFixed32(b, math.Float32bits(f))
}

func appendF64(b []byte, num protowire.Number, f float64) []byte {
	if f == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, math.Float64bits(f))
}

func appendStr(b []byte, num protowire.Number, s string) []byte {
	if s == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func appendVar(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

// AppendProto appends the protobuf wire encoding of the sample as voltsight.telemetry.v1.TelemetryEvent
// (field numbers as in telemetry.proto; gateway-owned fields recv_ts and tenant_id are left unset).
func appendProto(b []byte, s *sample) []byte {
	b = appendStr(b, 1, s.vin)
	b = appendTimestamp(b, 2, s.tsMs)
	b = appendF64(b, 3, s.lat)
	b = appendF64(b, 4, s.lon)
	b = appendF32(b, 5, s.speedKmh)
	b = appendF32(b, 6, s.socPct)
	b = appendF64(b, 7, s.odoKm)
	b = appendStr(b, 8, s.dtc)
	b = appendVar(b, 9, uint64(s.evt))
	b = appendVar(b, 10, s.seq)
	b = appendStr(b, 11, s.oem)
	b = appendVar(b, 12, uint64(s.schemaVer))
	b = appendF32(b, 14, s.heading)
	b = appendF32(b, 15, s.packTempC)
	b = appendF32(b, 16, s.ambientC)
	b = appendF32(b, 17, s.packV)
	b = appendF32(b, 18, s.packA)
	b = appendVar(b, 19, uint64(s.chargeState))
	b = appendStr(b, 20, s.chargerID)
	return b
}

func appendQ(b []byte, s string) []byte { return strconv.AppendQuote(b, s) }

func appendF(b []byte, f float64, prec int) []byte { return strconv.AppendFloat(b, f, 'f', prec, 64) }

func rfc3339ms(b []byte, ms int64) []byte {
	b = append(b, '"')
	b = time.UnixMilli(ms).UTC().AppendFormat(b, "2006-01-02T15:04:05.000Z07:00")
	return append(b, '"')
}

// appendJSONA encodes OEM A's nested snake_case dialect. Schema v1 reports state of charge as a 0..1
// fraction and speed as "kmh"; schema v2 renames speed to "kph" and adds a state-of-health hint.
func appendJSONA(b []byte, s *sample) []byte {
	b = append(b, `{"vehicle":{"vin":`...)
	b = appendQ(b, s.vin)
	b = append(b, `},"ts":`...)
	b = rfc3339ms(b, s.tsMs)
	b = append(b, `,"pos":{"lat":`...)
	b = appendF(b, s.lat, 6)
	b = append(b, `,"lon":`...)
	b = appendF(b, s.lon, 6)
	b = append(b, `,"hdg":`...)
	b = appendF(b, float64(s.heading), 1)
	b = append(b, `},"spd":{`...)
	if s.schemaVer >= 2 {
		b = append(b, `"kph":`...)
	} else {
		b = append(b, `"kmh":`...)
	}
	b = appendF(b, float64(s.speedKmh), 1)
	b = append(b, `},"batt":{"soc":`...)
	b = appendF(b, float64(s.socPct)/100, 4)
	b = append(b, `,"temp_c":`...)
	b = appendF(b, float64(s.packTempC), 1)
	b = append(b, `,"v":`...)
	b = appendF(b, float64(s.packV), 1)
	b = append(b, `,"a":`...)
	b = appendF(b, float64(s.packA), 1)
	b = append(b, `,"odo_km":`...)
	b = appendF(b, s.odoKm, 1)
	if s.schemaVer >= 2 {
		b = append(b, `,"soh_hint":null`...)
	}
	b = append(b, `},"diag":{"dtc":[`...)
	if s.dtc != "" {
		b = appendQ(b, s.dtc)
	}
	b = append(b, `],"evt":`...)
	b = appendQ(b, evNames[s.evt])
	b = append(b, `},"chg":{"state":`...)
	b = appendQ(b, chargeNames[s.chargeState])
	b = append(b, `,"charger":`...)
	b = appendQ(b, s.chargerID)
	b = append(b, `},"amb_c":`...)
	b = appendF(b, float64(s.ambientC), 1)
	b = append(b, `,"seq":`...)
	b = strconv.AppendUint(b, s.seq, 10)
	b = append(b, `,"schema":`...)
	b = strconv.AppendUint(b, uint64(s.schemaVer), 10)
	return append(b, '}')
}

// appendJSONB encodes OEM B's flat camelCase dialect: integer percent state of charge, epoch-millisecond
// time, DTC as raw 4-hex-digit codes (without the leading letter, "0A7F" = P0A7F), short event codes.
func appendJSONB(b []byte, s *sample) []byte {
	b = append(b, `{"VIN":`...)
	b = appendQ(b, s.vin)
	b = append(b, `,"time":`...)
	b = strconv.AppendInt(b, s.tsMs, 10)
	b = append(b, `,"latitude":`...)
	b = appendF(b, s.lat, 6)
	b = append(b, `,"longitude":`...)
	b = appendF(b, s.lon, 6)
	b = append(b, `,"heading":`...)
	b = appendF(b, float64(s.heading), 1)
	b = append(b, `,"speedKmH":`...)
	b = appendF(b, float64(s.speedKmh), 1)
	b = append(b, `,"socPct":`...)
	b = strconv.AppendInt(b, int64(math.Round(float64(s.socPct))), 10)
	b = append(b, `,"packTempC":`...)
	b = appendF(b, float64(s.packTempC), 1)
	b = append(b, `,"packV":`...)
	b = appendF(b, float64(s.packV), 1)
	b = append(b, `,"packA":`...)
	b = appendF(b, float64(s.packA), 1)
	b = append(b, `,"odometerKm":`...)
	b = appendF(b, s.odoKm, 1)
	b = append(b, `,"dtcRaw":"`...)
	if len(s.dtc) == 5 {
		b = append(b, s.dtc[1:]...)
	}
	b = append(b, `","event":`...)
	b = appendQ(b, evCodesB[s.evt])
	b = append(b, `,"plug":`...)
	b = strconv.AppendUint(b, uint64(s.chargeState-1), 10)
	b = append(b, `,"chargerId":`...)
	b = appendQ(b, s.chargerID)
	b = append(b, `,"ambientC":`...)
	b = appendF(b, float64(s.ambientC), 1)
	b = append(b, `,"seq":`...)
	b = strconv.AppendUint(b, s.seq, 10)
	b = append(b, `,"v":`...)
	b = strconv.AppendUint(b, uint64(s.schemaVer), 10)
	return append(b, '}')
}

// appendChargerStatus encodes voltsight.charger.v1.ChargerStatusEvent.
func appendChargerStatus(b []byte, id string, status uint8, tsMs int64, seq uint64, powerKW float32) []byte {
	b = appendStr(b, 1, id)
	b = appendVar(b, 2, uint64(status))
	b = appendTimestamp(b, 3, tsMs)
	b = appendVar(b, 4, seq)
	return appendF32(b, 5, powerKW)
}
