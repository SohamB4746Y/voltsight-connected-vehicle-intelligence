// Package rtp is the real-time processor: it consumes normalised telemetry, de-duplicates it with an exact
// per-vehicle window, keeps the latest state in Redis and hands new events to the detection engine.
package rtp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/dedup"
	"voltsight/internal/state"
)

// Processor receives each event that advanced a vehicle (verdict New) together with its up-to-date state.
// It runs on the worker's single goroutine and must not block.
type Processor interface {
	OnEvent(ev *telemetryv1.TelemetryEvent, v *state.Entry, recvNow time.Time)
}

// Flusher is optionally implemented by a Processor that publishes asynchronously: Flush must make everything
// published so far durable. The worker calls it before committing offsets, so a failed flush means the batch is
// reprocessed instead of its alerts being lost.
type Flusher interface {
	Flush(ctx context.Context) error
}

// Config configures a worker.
type Config struct {
	Brokers    []string
	Topic      string
	Group      string
	MaxPoll    int  // records per batch, default 20,000
	StartAtEnd bool // for a group with no committed offset: skip the backlog (default: start at the earliest retained offset so nothing is lost)
	Processor  Processor
	Logger     func(format string, args ...any)
}

// Stats are the worker counters (also exported as Prometheus metrics).
type Stats struct {
	Records, New, Late, Duplicate, Stale, StaleSeenBefore, DecodeErrors, Batches atomic.Int64
	Lag                                                                          atomic.Int64
}

// Worker is the real-time processor.
type Worker struct {
	cfg   Config
	cl    *kgo.Client
	store *state.Store
	Stats Stats

	cache                     map[string]*state.Entry // VIN -> entry for the partitions this worker owns
	partOf                    map[string]int32
	bloom                     *dedup.Rotating
	dtcTop                    *dedup.TopK
	topMu                     sync.Mutex
	reg                       *prometheus.Registry
	mNew, mLate, mDup, mStale prometheus.Counter
	mLatency                  prometheus.Histogram
	mLag                      prometheus.Gauge
	closed                    atomic.Bool
}

// New creates a worker (the Kafka client joins the consumer group on the first poll).
func New(cfg Config, store *state.Store) (*Worker, error) {
	if cfg.MaxPoll == 0 {
		cfg.MaxPoll = 20_000
	}
	if cfg.Logger == nil {
		cfg.Logger = func(string, ...any) {}
	}
	w := &Worker{cfg: cfg, store: store, cache: map[string]*state.Entry{}, partOf: map[string]int32{},
		bloom: dedup.NewRotating(2_000_000, 0.01), dtcTop: dedup.NewTopK(10, 0.0005, 0.01)}
	w.reg = prometheus.NewRegistry()
	proc := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rtp_events_total", Help: "Events by verdict."}, []string{"verdict"})
	w.mNew, w.mLate, w.mDup, w.mStale = proc.WithLabelValues("new"), proc.WithLabelValues("late"),
		proc.WithLabelValues("duplicate"), proc.WithLabelValues("stale")
	w.mLatency = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "rtp_ingest_to_process_seconds", Help: "Gateway receive to worker processing (sampled).",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10}})
	w.mLag = prometheus.NewGauge(prometheus.GaugeOpts{Name: "rtp_consumer_lag_records", Help: "Records behind the log end across assigned partitions."})
	w.reg.MustRegister(proc, w.mLatency, w.mLag)

	// A group with no committed offset starts at the earliest retained offset. "End" is resolved when partitions are
	// assigned, which can be after producers started: events produced in between would be skipped, and the group
	// would still report zero lag (live finding, evidence/G4/status.md).
	reset := kgo.NewOffset().AtStart()
	if cfg.StartAtEnd {
		reset = kgo.NewOffset().AtEnd()
	}
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...), kgo.ClientID("vs-rtp"), kgo.ConsumerGroup(cfg.Group), kgo.ConsumeTopics(cfg.Topic),
		kgo.ConsumeResetOffset(reset), kgo.DisableAutoCommit(), kgo.BlockRebalanceOnPoll(),
		kgo.FetchMaxBytes(64<<20), kgo.FetchMaxPartitionBytes(8<<20),
		kgo.OnPartitionsRevoked(func(_ context.Context, _ *kgo.Client, revoked map[string][]int32) { w.drop(revoked[cfg.Topic]) }),
		kgo.OnPartitionsLost(func(_ context.Context, _ *kgo.Client, lost map[string][]int32) { w.drop(lost[cfg.Topic]) }),
	)
	if err != nil {
		return nil, err
	}
	w.cl = cl
	return w, nil
}

// drop forgets cached state of partitions that moved away (their state was flushed before the last commit).
func (w *Worker) drop(parts []int32) {
	gone := map[int32]bool{}
	for _, p := range parts {
		gone[p] = true
	}
	for vin, p := range w.partOf {
		if gone[p] {
			delete(w.cache, vin)
			delete(w.partOf, vin)
		}
	}
}

// Run processes batches until ctx ends. State is flushed to Redis before offsets are committed, so a
// crash can only cause re-processing (harmless: sequence guards make every state update idempotent).
func (w *Worker) Run(ctx context.Context) error {
	for ctx.Err() == nil && !w.closed.Load() {
		fetches := w.cl.PollRecords(ctx, w.cfg.MaxPoll)
		if fetches.IsClientClosed() {
			return nil
		}
		if err := ctx.Err(); err != nil {
			w.cl.AllowRebalance()
			return nil
		}
		fetches.EachError(func(t string, p int32, err error) {
			if !errors.Is(err, context.Canceled) {
				w.cfg.Logger("fetch error %s/%d: %v", t, p, err)
			}
		})
		if fetches.NumRecords() == 0 {
			w.cl.AllowRebalance()
			continue
		}
		if err := w.processBatch(ctx, fetches); err != nil {
			w.cl.AllowRebalance()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		w.cl.AllowRebalance()
	}
	return nil
}

func (w *Worker) processBatch(ctx context.Context, fetches kgo.Fetches) error {
	type rec struct {
		ev   *telemetryv1.TelemetryEvent
		part int32
	}
	var recs []rec
	var lag int64
	var need []state.Ref
	seenNeed := map[string]bool{}
	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		if n := len(p.Records); n > 0 {
			lag += p.HighWatermark - (p.Records[n-1].Offset + 1)
		}
		for _, r := range p.Records {
			var ev telemetryv1.TelemetryEvent
			if err := proto.Unmarshal(r.Value, &ev); err != nil || ev.Vin == "" || ev.TenantId == "" {
				w.Stats.DecodeErrors.Add(1)
				continue
			}
			recs = append(recs, rec{&ev, p.Partition})
			if _, ok := w.cache[ev.Vin]; !ok && !seenNeed[ev.Vin] {
				seenNeed[ev.Vin] = true
				need = append(need, state.Ref{Tenant: ev.TenantId, VIN: ev.Vin})
			}
		}
	})
	if len(need) > 0 {
		loaded, err := w.store.Load(ctx, need)
		if err != nil {
			return err
		}
		part := map[string]int32{}
		for _, r := range recs {
			part[r.ev.Vin] = r.part
		}
		for vin, e := range loaded {
			w.cache[vin], w.partOf[vin] = e, part[vin]
		}
	}

	now := time.Now()
	var dirty []*state.Entry
	for i, r := range recs {
		e := w.cache[r.ev.Vin]
		verdict := e.Window.Observe(r.ev.Seq)
		w.Stats.Records.Add(1)
		switch verdict {
		case dedup.New:
			w.Stats.New.Add(1)
			w.mNew.Inc()
			if b, err := proto.Marshal(r.ev); err == nil {
				e.Latest = b
			}
			if !e.Dirty {
				e.Dirty = true
				dirty = append(dirty, e)
			}
			for _, c := range r.ev.Dtc {
				w.topMu.Lock()
				w.dtcTop.Add(c)
				w.topMu.Unlock()
			}
			if i%64 == 0 && r.ev.RecvTs != nil {
				w.mLatency.Observe(now.Sub(r.ev.RecvTs.AsTime()).Seconds())
			}
			if w.cfg.Processor != nil {
				w.cfg.Processor.OnEvent(r.ev, e, now)
			}
		case dedup.Late:
			w.Stats.Late.Add(1)
			w.mLate.Inc()
			if !e.Dirty { // the window changed: persist it
				e.Dirty = true
				dirty = append(dirty, e)
			}
		case dedup.Duplicate:
			w.Stats.Duplicate.Add(1)
			w.mDup.Inc()
		case dedup.Stale:
			// older than the exact window can tell: the Bloom filter tells "probably seen" vs "new"
			h := dedup.HashString(fmt.Sprintf("%s:%d", r.ev.Vin, r.ev.Seq))
			if w.bloom.Has(h) {
				w.Stats.StaleSeenBefore.Add(1)
			} else {
				w.bloom.Add(h)
			}
			w.Stats.Stale.Add(1)
			w.mStale.Inc()
		}
	}
	if f, ok := w.cfg.Processor.(Flusher); ok {
		if err := f.Flush(ctx); err != nil {
			return fmt.Errorf("processor flush: %w", err)
		}
	}
	if err := w.store.Save(ctx, dirty); err != nil {
		return err
	}
	if err := w.cl.CommitUncommittedOffsets(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	w.Stats.Batches.Add(1)
	w.Stats.Lag.Store(lag)
	w.mLag.Set(float64(lag))
	return nil
}

// Kill closes the client without committing, simulating a crash.
func (w *Worker) Kill() {
	w.closed.Store(true)
	w.cl.Close()
}

// Close leaves the group cleanly.
func (w *Worker) Close() {
	w.closed.Store(true)
	w.cl.Close()
}

// TopDTCs returns the heaviest diagnostic codes seen (Count-Min estimates).
func (w *Worker) TopDTCs() []dedup.Item {
	w.topMu.Lock()
	defer w.topMu.Unlock()
	return w.dtcTop.Top()
}

// Registry exposes the worker's Prometheus registry so that a Processor can register its own metrics.
func (w *Worker) Registry() *prometheus.Registry { return w.reg }

// AdminHandler serves /healthz, /metrics and /topk/dtc.
func (w *Worker) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(http.StatusOK) })
	mux.Handle("GET /metrics", promhttp.HandlerFor(w.reg, promhttp.HandlerOpts{}))
	return mux
}
