package rangerisk

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	alertv1 "voltsight/gen/voltsight/alert/v1"
	chargerv1 "voltsight/gen/voltsight/charger/v1"
	"voltsight/internal/kafkautil"
)

// KafkaPublisher publishes alerts to alerts.v1 keyed by tenant|vin (all alerts of one vehicle stay ordered).
// Producing is asynchronous; Flush waits for the broker to acknowledge everything (acks=all, idempotent).
type KafkaPublisher struct {
	cl     *kgo.Client
	Failed atomic.Int64
	Sent   atomic.Int64
}

// NewKafkaPublisher connects a producer.
func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("vs-risk"), kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(2*time.Millisecond), kgo.ProducerBatchCompression(kgo.ZstdCompression()))
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{cl: cl}, nil
}

// Publish implements Publisher.
func (p *KafkaPublisher) Publish(a *alertv1.AlertEvent) {
	b, err := proto.Marshal(a)
	if err != nil {
		p.Failed.Add(1)
		return
	}
	p.cl.Produce(context.Background(), &kgo.Record{Topic: kafkautil.Alerts, Key: []byte(a.TenantId + "|" + a.Vin), Value: b},
		func(_ *kgo.Record, err error) {
			if err != nil {
				p.Failed.Add(1)
				return
			}
			p.Sent.Add(1)
		})
}

// Flush implements Publisher. A failed alert makes the batch fail, so the worker does not commit offsets and the
// batch is reprocessed (alert keys are idempotent).
func (p *KafkaPublisher) Flush(ctx context.Context) error {
	before := p.Failed.Load()
	if err := p.cl.Flush(ctx); err != nil {
		return err
	}
	if p.Failed.Load() != before {
		return fmt.Errorf("alert publish failed (%d)", p.Failed.Load()-before)
	}
	return nil
}

// Close releases the producer.
func (p *KafkaPublisher) Close() { p.cl.Close() }

// StatusFeed applies charger.status.v1 to an Engine. It reads the whole compacted topic without a consumer
// group (every worker instance needs every status), from the beginning, so a restart rebuilds the overlay.
type StatusFeed struct {
	cl      *kgo.Client
	eng     *Engine
	Applied atomic.Int64 // status events that changed the overlay
	Seen    atomic.Int64
	Logger  func(format string, args ...any)
}

// NewStatusFeed creates the feed.
func NewStatusFeed(brokers []string, eng *Engine) (*StatusFeed, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("vs-risk-status"), kgo.ConsumeTopics(kafkautil.ChargerStatus),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		return nil, err
	}
	return &StatusFeed{cl: cl, eng: eng, Logger: func(string, ...any) {}}, nil
}

// Run applies status events until ctx ends.
func (f *StatusFeed) Run(ctx context.Context) {
	for ctx.Err() == nil {
		fetches := f.cl.PollFetches(ctx)
		if fetches.IsClientClosed() {
			return
		}
		fetches.EachRecord(func(r *kgo.Record) {
			var ev chargerv1.ChargerStatusEvent
			if err := proto.Unmarshal(r.Value, &ev); err != nil {
				f.Logger("status: bad record: %v", err)
				return
			}
			f.Seen.Add(1)
			// OCCUPIED chargers are still usable (a vehicle may have to wait); only OUT_OF_SERVICE removes one
			if f.eng.SetAvailable(ev.ChargerId, ev.Status != chargerv1.ChargerStatus_CHARGER_STATUS_OUT_OF_SERVICE) {
				f.Applied.Add(1)
			}
		})
	}
}

// Close releases the consumer.
func (f *StatusFeed) Close() { f.cl.Close() }
