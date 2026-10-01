package sim

import (
	"runtime"
	"sync"
	"sync/atomic"
)

func defaultShards() int {
	n := runtime.GOMAXPROCS(0)
	if n < 1 {
		n = 1
	}
	return n
}

// NullSink discards messages and keeps an order-independent digest of everything delivered, which makes
// determinism checkable at 100K vehicles without storing the stream.
type NullSink struct {
	Messages  atomic.Int64
	Telemetry atomic.Int64
	Charger   atomic.Int64
	digest    atomic.Uint64
}

func fnv(p []byte, seed uint64) uint64 {
	h := uint64(1469598103934665603) ^ seed*0x9e3779b97f4a7c15
	for _, b := range p {
		h = (h ^ uint64(b)) * 1099511628211
	}
	return h
}

// Emit implements Sink.
func (s *NullSink) Emit(_ int, tick int, batch []Message) error {
	var d uint64
	for i := range batch {
		d += fnv(batch[i].Payload, uint64(tick)<<2|uint64(batch[i].Kind))
		if batch[i].Kind == KindTelemetry {
			s.Telemetry.Add(1)
		} else {
			s.Charger.Add(1)
		}
	}
	s.Messages.Add(int64(len(batch)))
	s.digest.Add(d)
	return nil
}

// Digest returns the order-independent digest of all (tick, kind, payload) deliveries so far.
func (s *NullSink) Digest() uint64 { return s.digest.Load() }

// Delivery is a copied message with its delivery tick, for tests.
type Delivery struct {
	Tick int
	Message
}

// CollectSink stores copies of every message (small runs only).
type CollectSink struct {
	mu   sync.Mutex
	Msgs []Delivery
}

// Emit implements Sink.
func (s *CollectSink) Emit(_ int, tick int, batch []Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range batch {
		m.Payload = append([]byte(nil), m.Payload...)
		s.Msgs = append(s.Msgs, Delivery{tick, m})
	}
	return nil
}
