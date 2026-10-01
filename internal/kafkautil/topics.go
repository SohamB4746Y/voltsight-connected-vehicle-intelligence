// Package kafkautil defines and creates the platform's Kafka topics.
package kafkautil

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Topic names.
const (
	Telemetry     = "telemetry.v1"
	TelemetryDLQ  = "telemetry.dlq.v1"
	ChargerStatus = "charger.status.v1"
	Alerts        = "alerts.v1"
	Erasure       = "privacy.erasure.v1"
)

// Spec describes one topic.
type Spec struct {
	Name       string
	Partitions int32
	Configs    map[string]string
}

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

// Specs are the designed topics (see docs/architecture): telemetry is keyed by VIN with 64 partitions
// and 72 h retention; charger status is compacted; DLQ and alerts keep 14 days.
func Specs() []Spec {
	return []Spec{
		{Telemetry, 64, map[string]string{"retention.ms": ms(72 * time.Hour), "compression.type": "producer", "min.insync.replicas": "1"}},
		{TelemetryDLQ, 12, map[string]string{"retention.ms": ms(14 * 24 * time.Hour), "compression.type": "producer"}},
		{ChargerStatus, 6, map[string]string{"cleanup.policy": "compact", "min.cleanable.dirty.ratio": "0.1", "segment.ms": ms(time.Hour)}},
		{Alerts, 12, map[string]string{"retention.ms": ms(14 * 24 * time.Hour)}},
		{Erasure, 3, map[string]string{"retention.ms": ms(30 * 24 * time.Hour)}},
	}
}

// Ensure creates any missing topics with the given replication factor and returns the names created.
func Ensure(ctx context.Context, brokers []string, rf int16, specs []Spec) ([]string, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	existing, err := adm.ListTopics(ctx)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	var created []string
	for _, s := range specs {
		if existing.Has(s.Name) {
			continue
		}
		cfg := map[string]*string{}
		for k, v := range s.Configs {
			v := v
			cfg[k] = &v
		}
		_, err := adm.CreateTopic(ctx, s.Partitions, rf, cfg, s.Name)
		switch {
		case err == nil:
			created = append(created, s.Name)
		case errors.Is(err, kerr.TopicAlreadyExists): // created by someone else meanwhile: fine, not ours
		default:
			return created, fmt.Errorf("create %s: %w", s.Name, err)
		}
	}
	return created, nil
}
