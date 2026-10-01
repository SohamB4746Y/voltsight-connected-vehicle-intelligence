// Package testkit holds helpers shared by the integration tests that need a real Kafka: topic lifecycle,
// synthetic telemetry with duplicates / reordering / stale events, and waiting for a consumer group to drain.
package testkit

import (
	"context"
	"fmt"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/dedup"
	"voltsight/internal/vin"
)

// Brokers is the compose stack's Kafka (host listener).
var Brokers = []string{"127.0.0.1:29092"}

// Admin returns an admin client that is closed with the test.
func Admin(t testing.TB) *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(Brokers...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

// Topic creates a uniquely named topic with the given partitions and deletes it with the test.
func Topic(t testing.TB, prefix string, partitions int32) string {
	name := fmt.Sprintf("%s.%s", prefix, uuid.NewString()[:8])
	adm := Admin(t)
	if _, err := adm.CreateTopic(context.Background(), partitions, 1, nil, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adm.DeleteTopics(context.Background(), name) })
	return name
}

// Scenario is a synthetic telemetry stream and what a correct consumer must conclude from it.
type Scenario struct {
	Tenant   string
	VINs     []string
	Events   []*telemetryv1.TelemetryEvent // in delivery order
	Expected map[dedup.Verdict]int         // reference verdict totals
	High     map[string]uint64             // highest seq per VIN
	Distinct int                           // distinct (vin, seq) pairs
}

// NewScenario builds vins x perVIN events with local reordering (ooo), duplicates and a few stale replays.
func NewScenario(seed uint64, vins, perVIN int, dupRate, oooRate, staleRate float64) *Scenario {
	r := rand.New(rand.NewPCG(seed, 99))
	s := &Scenario{Tenant: uuid.NewString(), Expected: map[dedup.Verdict]int{}, High: map[string]uint64{}}
	perVin := make([][]*telemetryv1.TelemetryEvent, vins)
	distinct := map[string]bool{}
	now := time.Now()
	for i := 0; i < vins; i++ {
		v, err := vin.Build(fmt.Sprintf("1HGCM826x3A%06d", seed%1000*1000+uint64(i)))
		if err != nil {
			panic(err)
		}
		s.VINs = append(s.VINs, v)
		seqs := make([]uint64, perVIN)
		for j := range seqs {
			seqs[j] = uint64(j + 1)
		}
		for j := 0; j+5 < perVIN; j++ { // local reordering
			if r.Float64() < oooRate {
				k := j + 1 + r.IntN(4)
				seqs[j], seqs[k] = seqs[k], seqs[j]
			}
		}
		var order []uint64
		for j, q := range seqs {
			order = append(order, q)
			if r.Float64() < dupRate {
				order = append(order, q)
			}
			if j > 300 && r.Float64() < staleRate {
				order = append(order, uint64(1+r.IntN(j-280))) // far older than the exact window
			}
		}
		for _, q := range order {
			distinct[fmt.Sprintf("%s/%d", v, q)] = true
			perVin[i] = append(perVin[i], &telemetryv1.TelemetryEvent{
				Vin: v, Ts: timestamppb.New(now.Add(time.Duration(q) * time.Second)), Lat: 13.08, Lon: 80.27, SpeedKmh: 30,
				SocPct: 50, OdoKm: float64(q), Seq: q, Oem: "AURORA", SchemaVer: 1, TenantId: s.Tenant, RecvTs: timestamppb.New(now),
			})
		}
		var w dedup.Window
		for _, e := range perVin[i] {
			s.Expected[w.Observe(e.Seq)]++
		}
		s.High[v] = w.High
	}
	s.Distinct = len(distinct)
	// merge the vehicles' sequences randomly, keeping each vehicle's own order
	idx := make([]int, vins)
	remaining := 0
	for _, p := range perVin {
		remaining += len(p)
	}
	for remaining > 0 {
		i := r.IntN(vins)
		if idx[i] >= len(perVin[i]) {
			continue
		}
		s.Events = append(s.Events, perVin[i][idx[i]])
		idx[i]++
		remaining--
	}
	return s
}

// Produce writes the events to the topic keyed by VIN, in order, and waits for acknowledgement.
func Produce(t testing.TB, topic string, events []*telemetryv1.TelemetryEvent) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(Brokers...), kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchCompression(kgo.Lz4Compression()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	recs := make([]*kgo.Record, len(events))
	for i, e := range events {
		b, err := proto.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		recs[i] = &kgo.Record{Topic: topic, Key: []byte(e.Vin), Value: b}
	}
	if err := cl.ProduceSync(context.Background(), recs...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

// WaitDrained blocks until the group has committed every record currently in the topic.
func WaitDrained(t testing.TB, group, topic string, timeout time.Duration) {
	t.Helper()
	adm := Admin(t)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ends, err1 := adm.ListEndOffsets(ctx, topic)
		committed, err2 := adm.FetchOffsets(ctx, group)
		cancel()
		if err1 == nil && err2 == nil {
			drained := true
			ends.Each(func(o kadm.ListedOffset) {
				if o.Offset == 0 {
					return
				}
				c, ok := committed.Lookup(topic, o.Partition)
				if !ok || c.At < o.Offset {
					drained = false
				}
			})
			if drained {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("group %s did not drain topic %s within %s", group, topic, timeout)
}
