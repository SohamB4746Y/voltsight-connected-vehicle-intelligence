// Package alerts is the alert service: it consumes alerts.v1, persists each alert idempotently in PostgreSQL
// (unique key vin+rule+window_start) and fans it out on Redis pub/sub for live dashboards.
package alerts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	"voltsight/internal/kafkautil"
)

// Channel is the Redis pub/sub channel of a tenant's alerts.
func Channel(tenant string) string { return "alerts:" + tenant }

// Config configures the service.
type Config struct {
	Brokers    []string
	Group      string
	Topic      string
	StartAtEnd bool
	// LatencyLog, when set, receives one JSON line per persisted alert with the receive->persist latency.
	LatencyLog string
}

// Stats are the service counters.
type Stats struct {
	Consumed, Inserted, Duplicates, Published, DecodeErrors, Failed atomic.Int64
}

// Service is the alert service.
type Service struct {
	cfg   Config
	cl    *kgo.Client
	pool  *pgxpool.Pool
	rdb   redis.UniversalClient
	Stats Stats

	logMu sync.Mutex
	logW  *bufio.Writer
	logF  *os.File
}

// New connects the service. pool must be connected as the voltsight_alerts role.
func New(cfg Config, pool *pgxpool.Pool, rdb redis.UniversalClient) (*Service, error) {
	if cfg.Group == "" {
		cfg.Group = "alert-svc"
	}
	if cfg.Topic == "" {
		cfg.Topic = kafkautil.Alerts
	}
	reset := kgo.NewOffset().AtStart()
	if cfg.StartAtEnd {
		reset = kgo.NewOffset().AtEnd()
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(cfg.Brokers...), kgo.ClientID("vs-alerts"), kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topic), kgo.ConsumeResetOffset(reset), kgo.DisableAutoCommit())
	if err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg, cl: cl, pool: pool, rdb: rdb}
	if cfg.LatencyLog != "" {
		f, err := os.Create(cfg.LatencyLog)
		if err != nil {
			cl.Close()
			return nil, err
		}
		s.logF, s.logW = f, bufio.NewWriter(f)
	}
	return s, nil
}

// Close releases the connections.
func (s *Service) Close() {
	s.cl.Close()
	if s.logW != nil {
		s.logMu.Lock()
		_ = s.logW.Flush()
		_ = s.logF.Close()
		s.logMu.Unlock()
	}
}

// Run processes alerts until ctx ends. Offsets are committed only after PostgreSQL has the rows, so a crash
// re-delivers; the unique key turns redelivery into a no-op.
func (s *Service) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		fetches := s.cl.PollRecords(ctx, 500)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return nil
		}
		if fetches.NumRecords() == 0 {
			continue
		}
		var evs []*alertv1.AlertEvent
		fetches.EachRecord(func(r *kgo.Record) {
			var a alertv1.AlertEvent
			if err := proto.Unmarshal(r.Value, &a); err != nil || a.Vin == "" || a.TenantId == "" || a.WindowStart == nil {
				s.Stats.DecodeErrors.Add(1)
				return
			}
			evs = append(evs, &a)
		})
		s.Stats.Consumed.Add(int64(fetches.NumRecords()))
		if err := retry(ctx, func() error { return s.persist(ctx, evs) }); err != nil {
			s.Stats.Failed.Add(1)
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("persist: %w", err)
		}
		if err := retry(ctx, func() error { return s.cl.CommitUncommittedOffsets(ctx) }); err != nil && ctx.Err() == nil {
			return fmt.Errorf("commit: %w", err)
		}
	}
	return nil
}

const insertSQL = `
INSERT INTO alert (tenant_id, vin, rule, severity, window_start, detected_at, source_event_ts, source_recv_ts, evidence)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (vin, rule, window_start) DO UPDATE SET detected_at = alert.detected_at
RETURNING id, (xmax = 0) AS inserted`

type pushed struct {
	ID         string          `json:"id"`
	Tenant     string          `json:"tenant_id"`
	VIN        string          `json:"vin"`
	Rule       string          `json:"rule"`
	Severity   string          `json:"severity"`
	WindowFrom time.Time       `json:"window_start"`
	DetectedAt time.Time       `json:"detected_at"`
	SocPct     float32         `json:"soc_pct"`
	MarginKm   float32         `json:"margin_km"`
	Charger    string          `json:"nearest_charger_id,omitempty"`
	Evidence   json.RawMessage `json:"evidence"`
}

func (s *Service) persist(ctx context.Context, evs []*alertv1.AlertEvent) error {
	if len(evs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, a := range evs {
		ev, err := a.Evidence.MarshalJSON()
		if err != nil {
			ev = []byte("{}")
		}
		recv := (*time.Time)(nil)
		if a.SourceRecvTs != nil {
			t := a.SourceRecvTs.AsTime()
			recv = &t
		}
		var src *time.Time
		if a.SourceEventTs != nil {
			t := a.SourceEventTs.AsTime()
			src = &t
		}
		b.Queue(insertSQL, a.TenantId, a.Vin, ruleName(a.Rule), sevName(a.Severity), a.WindowStart.AsTime(),
			a.DetectedAt.AsTime(), src, recv, ev)
	}
	res := s.pool.SendBatch(ctx, b)
	ids := make([]string, len(evs))
	for i := range evs {
		var inserted bool
		if err := res.QueryRow().Scan(&ids[i], &inserted); err != nil {
			_ = res.Close()
			return err
		}
		if inserted {
			s.Stats.Inserted.Add(1)
		} else {
			s.Stats.Duplicates.Add(1)
		}
	}
	if err := res.Close(); err != nil {
		return err
	}
	now := time.Now()
	pipe := s.rdb.Pipeline()
	for i, a := range evs {
		ev, _ := a.Evidence.MarshalJSON()
		msg, _ := json.Marshal(pushed{ID: ids[i], Tenant: a.TenantId, VIN: a.Vin, Rule: ruleName(a.Rule), Severity: sevName(a.Severity),
			WindowFrom: a.WindowStart.AsTime(), DetectedAt: a.DetectedAt.AsTime(), SocPct: a.SocPct, MarginKm: a.MarginKm,
			Charger: a.NearestChargerId, Evidence: ev})
		pipe.Publish(ctx, Channel(a.TenantId), msg)
		s.recordLatency(a, ids[i], now)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	s.Stats.Published.Add(int64(len(evs)))
	return nil
}

func (s *Service) recordLatency(a *alertv1.AlertEvent, id string, now time.Time) {
	if s.logW == nil || a.SourceRecvTs == nil {
		return
	}
	line, _ := json.Marshal(map[string]any{"id": id, "vin": a.Vin, "rule": ruleName(a.Rule),
		"recv_to_persist_ms":  now.Sub(a.SourceRecvTs.AsTime()).Milliseconds(),
		"recv_to_decision_ms": a.DetectedAt.AsTime().Sub(a.SourceRecvTs.AsTime()).Milliseconds(),
		"event_to_persist_ms": now.Sub(a.SourceEventTs.AsTime()).Milliseconds()})
	s.logMu.Lock()
	s.logW.Write(line) //nolint:errcheck // flushed on Close
	s.logW.WriteByte('\n')
	s.logMu.Unlock()
}

// the PostgreSQL CHECK constraints use the bare names
func ruleName(r alertv1.Rule) string {
	return trimPrefix(r.String(), "RULE_")
}

func sevName(v alertv1.Severity) string { return trimPrefix(v.String(), "SEVERITY_") }

func trimPrefix(s, p string) string {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

// retry runs op with capped exponential back-off for up to 10 minutes: PostgreSQL or Redis restarting must slow the
// service down (alerts stay in Kafka, offsets are committed only after persistence), not kill it. Persisting is
// idempotent (unique key), so repeating a partially applied batch is safe.
func retry(ctx context.Context, op func() error) error {
	deadline := time.Now().Add(10 * time.Minute)
	delay := 200 * time.Millisecond
	for {
		err := op()
		if err == nil || ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		if delay *= 2; delay > 10*time.Second {
			delay = 10 * time.Second
		}
	}
}
