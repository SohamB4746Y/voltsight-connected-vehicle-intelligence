// Package sim is the VoltSight fleet simulator: a deterministic, sharded, physics-based generator of
// connected-EV telemetry for up to 100,000+ vehicles, with realistic noise and delivery faults
// (duplicates, out-of-order, outage/flush bursts, malformed payloads, schema rollout).
package sim

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Format selects the wire encoding of telemetry messages.
type Format string

const (
	// FormatProto emits the normalised protobuf TelemetryEvent (what the ingest gateway forwards).
	FormatProto Format = "proto"
	// FormatOEMJSON emits each vehicle's raw OEM-cloud JSON dialect (A or B) for the gateway to normalise.
	FormatOEMJSON Format = "oem-json"
)

// Kind distinguishes the topic a message is destined for.
type Kind uint8

const (
	KindTelemetry     Kind = iota // telemetry.v1
	KindChargerStatus             // charger.status.v1
)

// Message is one encoded event. Payload is only valid until the sink's Emit returns.
type Message struct {
	Kind    Kind
	Key     string    // VIN (telemetry) or charger id; used as the Kafka key
	Dialect byte      // 'A', 'B' for OEM JSON, 0 for protobuf
	Tenant  uuid.UUID // the connector (tenant x OEM) this telemetry travels through
	OEM     string
	Payload []byte
}

// Sink receives batches from the engine. Emit may be called concurrently from several shards and must
// not retain the batch or its payloads after returning.
type Sink interface {
	Emit(shard int, tick int, batch []Message) error
}

// Config controls a simulation run. Zero values select the documented defaults.
type Config struct {
	Seed      uint64 // behaviour seed
	WorldSeed uint64 // identity seed; default 20260925 = the database seed, so VINs/tenants match
	Vehicles  int    // default 100,000
	Duration  int    // simulated seconds to run
	SimStart  time.Time
	StartTOD  int // local time of day at simulation start, seconds after midnight; default 05:30
	// TimeBase, when set, is the timestamp of simulated second 0 (use time.Now() for live runs so event
	// times are current); otherwise timestamps are SimStart + StartTOD.
	TimeBase time.Time

	Format   Format
	Shards   int     // default GOMAXPROCS
	RealTime bool    // pace ticks to the wall clock
	Speedup  float64 // simulated seconds per wall second when RealTime; default 1

	DupRate       float64 // probability an event is delivered twice
	OOORate       float64 // probability an event is delayed (arrives out of order)
	OOOMaxDelay   int     // seconds, default 60
	MalformedRate float64 // probability a payload is corrupted

	BurstAt, BurstDuration, BurstMult int // telematics sample-rate burst: Mult samples/s/vehicle during the window

	OutageAt, OutageDuration int     // connectivity outage window (sim seconds)
	OutageFraction           float64 // share of vehicles that buffer during the outage and flush at recovery

	SchemaV2At int // sim second at which OEM-A vehicles switch to schema v2 (0 = never)

	ChargerOutageAt, ChargerOutageDuration int
	ChargerOutageFraction                  float64

	LowSoCStartFraction   float64 // share of vehicles that start with a low battery; default 0.08
	UnawareDriverFraction float64 // share of drivers that ignore low-battery warnings; default 0.15
	FaultFraction         float64 // share of vehicles that develop a battery DTC; default 0.02

	TruthDir string // write ground-truth files here when non-empty
}

func (c *Config) defaults() {
	if c.WorldSeed == 0 {
		c.WorldSeed = 20260925
	}
	if c.Vehicles <= 0 {
		c.Vehicles = 100_000
	}
	if c.Format == "" {
		c.Format = FormatProto
	}
	if c.Speedup <= 0 {
		c.Speedup = 1
	}
	if c.OOOMaxDelay <= 0 {
		c.OOOMaxDelay = 60
	}
	if c.StartTOD == 0 {
		c.StartTOD = 5*3600 + 30*60
	}
	if c.SimStart.IsZero() {
		c.SimStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	}
	if c.LowSoCStartFraction == 0 {
		c.LowSoCStartFraction = 0.08
	}
	if c.UnawareDriverFraction == 0 {
		c.UnawareDriverFraction = 0.15
	}
	if c.FaultFraction == 0 {
		c.FaultFraction = 0.02
	}
	if c.BurstMult < 1 {
		c.BurstMult = 1
	}
}

// Stats are the run counters (safe for concurrent update).
type Stats struct {
	Ticks            atomic.Int64
	Generated        atomic.Int64 // distinct telemetry samples produced
	Emitted          atomic.Int64 // telemetry messages handed to the sink (includes duplicates)
	Bytes            atomic.Int64
	Duplicates       atomic.Int64
	OutOfOrder       atomic.Int64
	Malformed        atomic.Int64
	OutageBuffered   atomic.Int64
	OutageFlushed    atomic.Int64
	ChargerEvents    atomic.Int64
	Strandings       atomic.Int64
	Trips            atomic.Int64
	ChargeSessions   atomic.Int64
	PeakTickMessages atomic.Int64
}

// Snapshot is a plain-value copy of Stats for reporting.
type Snapshot struct {
	Ticks, Generated, Emitted, Bytes, Duplicates, OutOfOrder, Malformed, OutageBuffered, OutageFlushed int64
	ChargerEvents, Strandings, Trips, ChargeSessions, PeakTickMessages                                 int64
}

// Snapshot copies the counters.
func (s *Stats) Snapshot() Snapshot {
	return Snapshot{s.Ticks.Load(), s.Generated.Load(), s.Emitted.Load(), s.Bytes.Load(), s.Duplicates.Load(),
		s.OutOfOrder.Load(), s.Malformed.Load(), s.OutageBuffered.Load(), s.OutageFlushed.Load(),
		s.ChargerEvents.Load(), s.Strandings.Load(), s.Trips.Load(), s.ChargeSessions.Load(), s.PeakTickMessages.Load()}
}
