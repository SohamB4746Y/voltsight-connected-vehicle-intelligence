// Package contract verifies the protobuf contracts: the problem statement's example event parses
// unchanged, every required telemetry field exists, and binary/JSON encodings round-trip.
package contract

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	chargerv1 "voltsight/gen/voltsight/charger/v1"
	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/vin"
)

// The illustrative event from the problem statement (section 8), verbatim.
const pdfExample = `{"vin":"1HGCM82633A004352","ts":"2026-09-25T10:15:02.120Z","lat":21.1702,"lon":72.8311,"speed_kmh":64.2,"soc_pct":41,"odo_km":18234.7,"dtc":["P0301"],"evt":"HARSH_BRAKE","seq":88412}`

func TestProblemStatementExampleParsesUnchanged(t *testing.T) {
	var e telemetryv1.TelemetryEvent
	if err := protojson.Unmarshal([]byte(pdfExample), &e); err != nil {
		t.Fatalf("the problem statement's example event does not parse: %v", err)
	}
	if e.Vin != "1HGCM82633A004352" || !vin.Valid(e.Vin) {
		t.Fatalf("vin = %q", e.Vin)
	}
	want := time.Date(2026, 9, 25, 10, 15, 2, 120_000_000, time.UTC)
	if !e.Ts.AsTime().Equal(want) {
		t.Fatalf("ts = %v, want %v", e.Ts.AsTime(), want)
	}
	if e.Lat != 21.1702 || e.Lon != 72.8311 || e.OdoKm != 18234.7 || e.Seq != 88412 {
		t.Fatalf("numeric fields wrong: %+v", &e)
	}
	if e.SpeedKmh != float32(64.2) || e.SocPct != 41 {
		t.Fatalf("speed/soc wrong: %v %v", e.SpeedKmh, e.SocPct)
	}
	if len(e.Dtc) != 1 || e.Dtc[0] != "P0301" || e.Evt != telemetryv1.Event_HARSH_BRAKE {
		t.Fatalf("dtc/evt wrong: %v %v", e.Dtc, e.Evt)
	}
	// strict: a typo in a field name must be rejected, not silently dropped
	var strict telemetryv1.TelemetryEvent
	if err := protojson.Unmarshal([]byte(`{"vin":"1HGCM82633A004352","speed_kph":1}`), &strict); err == nil {
		t.Fatal("unknown JSON field accepted")
	}
}

// Every field named in the problem statement exists with a sensible type.
func TestTelemetryContainsRequiredFields(t *testing.T) {
	d := (&telemetryv1.TelemetryEvent{}).ProtoReflect().Descriptor()
	required := map[string]protoreflect.Kind{
		"vin": protoreflect.StringKind, "lat": protoreflect.DoubleKind, "lon": protoreflect.DoubleKind,
		"speed_kmh": protoreflect.FloatKind, "soc_pct": protoreflect.FloatKind, "odo_km": protoreflect.DoubleKind,
		"dtc": protoreflect.StringKind, "evt": protoreflect.EnumKind, "seq": protoreflect.Uint64Kind,
		"ts": protoreflect.MessageKind,
	}
	for name, kind := range required {
		f := d.Fields().ByName(protoreflect.Name(name))
		if f == nil {
			t.Errorf("field %q missing from TelemetryEvent", name)
			continue
		}
		if f.Kind() != kind {
			t.Errorf("field %q has kind %v, want %v", name, f.Kind(), kind)
		}
	}
	if !d.Fields().ByName("dtc").IsList() {
		t.Error("dtc must be repeated")
	}
	// the gateway-owned fields exist so a device-supplied value can be overwritten
	for _, name := range []string{"tenant_id", "oem", "schema_ver", "recv_ts"} {
		if d.Fields().ByName(protoreflect.Name(name)) == nil {
			t.Errorf("gateway field %q missing", name)
		}
	}
}

func randomEvent(r *rand.Rand) *telemetryv1.TelemetryEvent {
	v, _ := vin.Build("1HGCM826x3A" + string([]byte{byte('0' + r.IntN(10)), byte('0' + r.IntN(10)), byte('0' + r.IntN(10)), byte('0' + r.IntN(10)), byte('0' + r.IntN(10)), byte('0' + r.IntN(10))}))
	dtc := make([]string, r.IntN(4))
	for i := range dtc {
		dtc[i] = "P0A" + string(rune('0'+r.IntN(10))) + string(rune('A'+r.IntN(6)))
	}
	return &telemetryv1.TelemetryEvent{
		Vin: v, Ts: timestamppb.New(time.Unix(1_700_000_000+r.Int64N(1e8), int64(r.IntN(1000))*1_000_000)),
		Lat: r.Float64()*180 - 90, Lon: r.Float64()*360 - 180, SpeedKmh: r.Float32() * 140, SocPct: r.Float32() * 100,
		OdoKm: r.Float64() * 300000, Dtc: dtc, Evt: telemetryv1.Event(r.IntN(11)), Seq: r.Uint64N(1 << 40),
		Oem: "AURORA", SchemaVer: uint32(1 + r.IntN(2)), RecvTs: timestamppb.Now(), HeadingDeg: r.Float32() * 360,
		PackTempC: r.Float32() * 60, AmbientTempC: r.Float32() * 45, PackVoltageV: 300 + r.Float32()*100,
		PackCurrentA: r.Float32()*400 - 200, ChargeState: telemetryv1.ChargeState(r.IntN(4)), ChargerId: "PUB-CHE-0001",
		TenantId: "b0813b89-210d-585f-9c4e-856440224c40",
	}
}

func TestBinaryAndJSONRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	for i := 0; i < 2000; i++ {
		e := randomEvent(r)
		b, err := proto.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var back telemetryv1.TelemetryEvent
		if err := proto.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(e, &back) {
			t.Fatalf("binary round trip changed the event:\n%v\n%v", e, &back)
		}
		j, err := protojson.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var viaJSON telemetryv1.TelemetryEvent
		if err := protojson.Unmarshal(j, &viaJSON); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(e, &viaJSON) {
			t.Fatalf("JSON round trip changed the event:\n%s", j)
		}
	}
}

func TestBatchRoundTripAndGarbageIsRejected(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	batch := &telemetryv1.TelemetryBatch{BatchId: "b-1"}
	for i := 0; i < 100; i++ {
		batch.Events = append(batch.Events, randomEvent(r))
	}
	b, _ := proto.Marshal(batch)
	var back telemetryv1.TelemetryBatch
	if err := proto.Unmarshal(b, &back); err != nil || !proto.Equal(batch, &back) {
		t.Fatalf("batch round trip failed: %v", err)
	}
	// truncated and random bytes must fail to decode or at least never panic
	for _, bad := range [][]byte{b[:len(b)/2], {0xff, 0xff, 0xff, 0xff, 0xff}, {0x0a, 0x80}} {
		var x telemetryv1.TelemetryBatch
		_ = proto.Unmarshal(bad, &x)
	}
	if err := proto.Unmarshal(b[:len(b)-3], &telemetryv1.TelemetryBatch{}); err == nil {
		t.Fatal("a truncated batch decoded without error")
	}
}

// Wire size is a measured fact used by the capacity analysis (docs/capacity.md), not a guess.
func TestWireSizeIsMeasuredAndWithinBudget(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	const n = 5000
	var protoTotal, jsonTotal int
	for i := 0; i < n; i++ {
		e := randomEvent(r)
		b, _ := proto.Marshal(e)
		j, _ := protojson.Marshal(e)
		protoTotal += len(b)
		jsonTotal += len(j)
	}
	avgProto, avgJSON := float64(protoTotal)/n, float64(jsonTotal)/n
	t.Logf("MEASURED over %d random events: protobuf %.1f B/event, protojson %.1f B/event", n, avgProto, avgJSON)
	if avgProto > 300 {
		t.Fatalf("protobuf event averages %.0f B, over the 300 B budget", avgProto)
	}
	if out := os.Getenv("VOLTSIGHT_EVIDENCE_DIR"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{
			"events_sampled": n, "protobuf_bytes_per_event": avgProto, "protojson_bytes_per_event": avgJSON,
			"method": "internal/contract TestWireSizeIsMeasuredAndWithinBudget, random events from a seeded PCG",
		}, "", "  ")
		if err := os.WriteFile(filepath.Join(out, "wire_size.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOtherContractsRoundTrip(t *testing.T) {
	ev, _ := structpb.NewStruct(map[string]any{"reason": "no available charger within range", "candidates": []any{"PUB-CHE-0001"}})
	alert := &alertv1.AlertEvent{
		TenantId: "t", Vin: "1HGCM82633A004352", Rule: alertv1.Rule_RULE_RANGE_CRITICAL, Severity: alertv1.Severity_SEVERITY_CRITICAL,
		WindowStart: timestamppb.Now(), DetectedAt: timestamppb.Now(), SourceEventTs: timestamppb.Now(), SourceRecvTs: timestamppb.Now(),
		SocPct: 7.5, UsableRangeKm: 12.25, DistanceToChargerKm: 30, MarginKm: -17.75, NearestChargerId: "c1",
		ModelVersion: "baseline-1", Evidence: ev, TraceId: "abc",
	}
	status := &chargerv1.ChargerStatusEvent{ChargerId: "c1", Status: chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE, Ts: timestamppb.Now(), Seq: 9, PowerKwAvailable: 0}
	dlq := &dlqv1.DlqRecord{Original: []byte{1, 2, 3}, ErrorClass: dlqv1.ErrorClass_ERROR_CLASS_VIN_INVALID, ErrorDetail: "bad check digit",
		SourceTopic: "telemetry.v1", SourcePartition: 3, SourceOffset: 42, Vin: "X", FailedAt: timestamppb.Now(), Attempts: 2}
	for _, m := range []proto.Message{alert, status, dlq} {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		back := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(m, back) {
			t.Fatalf("%T did not round trip: %v", m, err)
		}
	}
}

// Enum zero values are *_UNSPECIFIED so an absent field can never be mistaken for a real value.
func TestEnumZeroValuesAreUnspecified(t *testing.T) {
	for _, e := range []protoreflect.EnumType{
		telemetryv1.Event(0).Type(), telemetryv1.ChargeState(0).Type(), alertv1.Rule(0).Type(), alertv1.Severity(0).Type(),
		chargerv1.ChargerStatus(0).Type(), dlqv1.ErrorClass(0).Type(),
	} {
		zero := string(e.Descriptor().Values().ByNumber(0).Name())
		if len(zero) < 11 || zero[len(zero)-11:] != "UNSPECIFIED" {
			t.Errorf("%s zero value is %q", e.Descriptor().FullName(), zero)
		}
	}
}
