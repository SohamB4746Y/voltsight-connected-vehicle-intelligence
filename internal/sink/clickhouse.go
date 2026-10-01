// Package sink persists the telemetry stream to ClickHouse (warm tier) in large batches.
package sink

import (
	"context"
	"fmt"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
)

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// DDL returns the CREATE TABLE statement for the raw telemetry table.
//
// ReplacingMergeTree collapses rows with the same sorting key (tenant, vin, ts, seq), which is exactly the
// event identity, so duplicates delivered by at-least-once paths disappear on merge (and immediately with
// FINAL). Partitioning is by event day; the key leads with tenant then vin so that per-vehicle range scans
// touch few granules. Kafka keys are VIN hashes (uniform), so inserts do not concentrate on one shard key.
func DDL(table string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
  tenant_id     UUID,
  vin           FixedString(17),
  ts            DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
  recv_ts       DateTime64(3, 'UTC') CODEC(DoubleDelta, ZSTD(1)),
  seq           UInt64 CODEC(Delta, ZSTD(1)),
  lat           Float64 CODEC(Gorilla, ZSTD(1)),
  lon           Float64 CODEC(Gorilla, ZSTD(1)),
  speed_kmh     Float32 CODEC(Gorilla, ZSTD(1)),
  soc_pct       Float32 CODEC(Gorilla, ZSTD(1)),
  odo_km        Float64 CODEC(Gorilla, ZSTD(1)),
  heading_deg   Float32 CODEC(ZSTD(1)),
  pack_temp_c   Float32 CODEC(Gorilla, ZSTD(1)),
  ambient_temp_c Float32 CODEC(Gorilla, ZSTD(1)),
  pack_voltage_v Float32 CODEC(Gorilla, ZSTD(1)),
  pack_current_a Float32 CODEC(Gorilla, ZSTD(1)),
  charge_state  Enum8('UNSPECIFIED' = 0, 'NONE' = 1, 'PLUGGED' = 2, 'CHARGING' = 3),
  charger_id    LowCardinality(String),
  evt           Enum8('UNSPECIFIED' = 0, 'HARSH_BRAKE' = 1, 'HARSH_ACCEL' = 2, 'OVERSPEED' = 3, 'IGNITION_ON' = 4,
                      'IGNITION_OFF' = 5, 'PLUG_IN' = 6, 'PLUG_OUT' = 7, 'CHARGE_COMPLETE' = 8, 'LOW_SOC' = 9,
                      'THERMAL_WARNING' = 10),
  oem           LowCardinality(String),
  schema_ver    UInt8,
  dtc           Array(LowCardinality(String))
) ENGINE = ReplacingMergeTree
PARTITION BY toYYYYMMDD(ts)
ORDER BY (tenant_id, vin, ts, seq)
TTL toDateTime(ts) + INTERVAL 30 DAY DELETE`, table)
}

// Stats are the sink counters.
type Stats struct {
	Records, Inserted, DecodeErrors, Batches, InsertNanos atomic.Int64
	Lag                                                   atomic.Int64
}

// Config configures the sink.
type Config struct {
	Brokers            []string
	Topic              string
	Group              string
	Table              string
	CH                 *clickhouse.Options
	MaxPoll            int // records per insert, default 50,000
	StartFromBeginning bool
}

// Sink consumes the topic and inserts into ClickHouse.
type Sink struct {
	cfg   Config
	cl    *kgo.Client
	conn  driver.Conn
	Stats Stats
}

// Open connects to ClickHouse and Kafka and ensures the table exists.
func Open(ctx context.Context, cfg Config) (*Sink, error) {
	if !identRe.MatchString(cfg.Table) {
		return nil, fmt.Errorf("invalid table name %q", cfg.Table)
	}
	if cfg.MaxPoll == 0 {
		cfg.MaxPoll = 50_000
	}
	conn, err := clickhouse.Open(cfg.CH)
	if err != nil {
		return nil, err
	}
	if err := conn.Exec(ctx, DDL(cfg.Table)); err != nil {
		return nil, fmt.Errorf("create table: %w", err)
	}
	reset := kgo.NewOffset().AtEnd()
	if cfg.StartFromBeginning {
		reset = kgo.NewOffset().AtStart()
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.ClientID("vs-sink"), kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topic), kgo.ConsumeResetOffset(reset), kgo.DisableAutoCommit(),
		kgo.FetchMaxBytes(64<<20), kgo.FetchMaxPartitionBytes(8<<20))
	if err != nil {
		return nil, err
	}
	return &Sink{cfg: cfg, cl: cl, conn: conn}, nil
}

// Close releases the connections.
func (s *Sink) Close() {
	s.cl.Close()
	_ = s.conn.Close()
}

// Run inserts batches until ctx ends. Offsets are committed only after ClickHouse acknowledged the insert.
func (s *Sink) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		fetches := s.cl.PollRecords(ctx, s.cfg.MaxPoll)
		if fetches.IsClientClosed() {
			return nil
		}
		if ctx.Err() != nil {
			return nil
		}
		if fetches.NumRecords() == 0 {
			continue
		}
		if err := s.insert(ctx, fetches); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s *Sink) insert(ctx context.Context, fetches kgo.Fetches) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO "+s.cfg.Table)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	var n, lag int64
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if k := len(p.Records); k > 0 {
			lag += p.HighWatermark - (p.Records[k-1].Offset + 1)
		}
		for _, r := range p.Records {
			var e telemetryv1.TelemetryEvent
			if err := proto.Unmarshal(r.Value, &e); err != nil {
				s.Stats.DecodeErrors.Add(1)
				continue
			}
			tenant, perr := uuid.Parse(e.TenantId)
			if perr != nil || len(e.Vin) != 17 {
				s.Stats.DecodeErrors.Add(1)
				continue
			}
			recv := time.Time{}
			if e.RecvTs != nil {
				recv = e.RecvTs.AsTime()
			}
			if aerr := batch.Append(tenant, e.Vin, e.Ts.AsTime(), recv, e.Seq, e.Lat, e.Lon, e.SpeedKmh, e.SocPct, e.OdoKm,
				e.HeadingDeg, e.PackTempC, e.AmbientTempC, e.PackVoltageV, e.PackCurrentA, e.ChargeState.String()[len("CHARGE_STATE_"):],
				e.ChargerId, evName(e.Evt), e.Oem, uint8(e.SchemaVer), nonNil(e.Dtc)); aerr != nil {
				err = aerr
				return
			}
			n++
		}
	})
	if err != nil {
		_ = batch.Abort()
		return fmt.Errorf("append: %w", err)
	}
	t0 := time.Now()
	if err := batch.Send(); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	s.Stats.InsertNanos.Add(time.Since(t0).Nanoseconds())
	if err := s.cl.CommitUncommittedOffsets(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	s.Stats.Records.Add(int64(fetches.NumRecords()))
	s.Stats.Inserted.Add(n)
	s.Stats.Batches.Add(1)
	s.Stats.Lag.Store(lag)
	return nil
}

// evName maps the protobuf zero value (EVENT_UNSPECIFIED) onto the ClickHouse enum's UNSPECIFIED.
func evName(e telemetryv1.Event) string {
	if e == telemetryv1.Event_EVENT_UNSPECIFIED {
		return "UNSPECIFIED"
	}
	return e.String()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
