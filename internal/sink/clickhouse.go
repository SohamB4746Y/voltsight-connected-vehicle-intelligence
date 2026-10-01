// Package sink persists the telemetry stream to ClickHouse (warm tier) in large batches.
package sink

import (
	"context"
	"errors"
	"fmt"
	// Waiver: retry jitter only; not security-sensitive.
	"math/rand" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
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
	Retries                                               atomic.Int64 // failed insert/commit attempts that were retried
}

// Config configures the sink.
type Config struct {
	Brokers      []string
	Topic        string
	Group        string
	Table        string
	CH           *clickhouse.Options
	MaxPoll      int           // records per insert, default 50,000
	StartAtEnd   bool          // group without committed offset skips the backlog (default: start at the earliest offset)
	MaxRetryTime time.Duration // keep retrying a failing insert this long before giving up (default 10 min)
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
	if cfg.MaxRetryTime == 0 {
		cfg.MaxRetryTime = 10 * time.Minute
	}
	conn, err := clickhouse.Open(cfg.CH)
	if err != nil {
		return nil, err
	}
	if err := conn.Exec(ctx, DDL(cfg.Table)); err != nil {
		return nil, fmt.Errorf("create table: %w", err)
	}
	// Earliest by default: "end" is resolved at partition assignment, so records produced before the group joined
	// would be skipped while the group still reports zero lag.
	reset := kgo.NewOffset().AtStart()
	if cfg.StartAtEnd {
		reset = kgo.NewOffset().AtEnd()
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

// appendError marks a failure to build the batch (a data/schema problem). Retrying cannot help, so it is fatal.
type appendError struct{ error }

func (s *Sink) insert(ctx context.Context, fetches kgo.Fetches) error {
	var n, decodeErrs, lag int64
	err := s.retry(ctx, "insert", func() error {
		var err error
		n, decodeErrs, lag, err = s.tryInsert(ctx, fetches)
		return err
	})
	if err != nil {
		return err
	}
	// The data is durable in ClickHouse. Only now may the offsets advance (at-least-once; replays collapse on
	// the event identity).
	if err := s.retry(ctx, "commit", func() error { return s.cl.CommitUncommittedOffsets(ctx) }); err != nil {
		return err
	}
	s.Stats.DecodeErrors.Add(decodeErrs)
	s.Stats.Records.Add(int64(fetches.NumRecords()))
	s.Stats.Inserted.Add(n)
	s.Stats.Batches.Add(1)
	s.Stats.Lag.Store(lag)
	return nil
}

// retry runs op, retrying with capped exponential back-off and jitter for up to MaxRetryTime. A ClickHouse
// that is restarting, over its memory limit or refusing parts must slow the sink down (Kafka absorbs the
// backlog), never kill it or lose records: offsets are not committed until op succeeds.
func (s *Sink) retry(ctx context.Context, what string, op func() error) error {
	deadline := time.Now().Add(s.cfg.MaxRetryTime)
	delay := 250 * time.Millisecond
	for {
		err := op()
		if err == nil {
			return nil
		}
		var ae appendError
		if errors.As(err, &ae) || ctx.Err() != nil || time.Now().After(deadline) {
			return fmt.Errorf("%s: %w", what, err)
		}
		s.Stats.Retries.Add(1)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, err)
		case <-time.After(delay/2 + time.Duration(rand.Int63n(int64(delay/2)+1))):
		}
		if delay *= 2; delay > 15*time.Second {
			delay = 15 * time.Second
		}
	}
}

func (s *Sink) tryInsert(ctx context.Context, fetches kgo.Fetches) (n, decodeErrs, lag int64, err error) {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO "+s.cfg.Table)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("prepare batch: %w", err)
	}
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if k := len(p.Records); k > 0 {
			lag += p.HighWatermark - (p.Records[k-1].Offset + 1)
		}
		for _, r := range p.Records {
			var e telemetryv1.TelemetryEvent
			if perr := proto.Unmarshal(r.Value, &e); perr != nil {
				decodeErrs++
				continue
			}
			tenant, perr := uuid.Parse(e.TenantId)
			if perr != nil || len(e.Vin) != 17 {
				decodeErrs++
				continue
			}
			recv := time.Time{}
			if e.RecvTs != nil {
				recv = e.RecvTs.AsTime()
			}
			if aerr := batch.Append(tenant, e.Vin, e.Ts.AsTime(), recv, e.Seq, e.Lat, e.Lon, e.SpeedKmh, e.SocPct, e.OdoKm,
				e.HeadingDeg, e.PackTempC, e.AmbientTempC, e.PackVoltageV, e.PackCurrentA, e.ChargeState.String()[len("CHARGE_STATE_"):],
				e.ChargerId, evName(e.Evt), e.Oem, uint8(e.SchemaVer), nonNil(e.Dtc)); aerr != nil {
				err = appendError{aerr}
				return
			}
			n++
		}
	})
	if err != nil {
		_ = batch.Abort()
		return 0, 0, 0, fmt.Errorf("append: %w", err)
	}
	t0 := time.Now()
	if err := batch.Send(); err != nil {
		return 0, 0, 0, fmt.Errorf("send: %w", err)
	}
	s.Stats.InsertNanos.Add(time.Since(t0).Nanoseconds())
	return n, decodeErrs, lag, nil
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
