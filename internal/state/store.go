// Package state is the Redis-backed per-vehicle state of the real-time path: the latest telemetry event,
// the highest sequence seen and the exact dedup window. Keys are prefixed by tenant.
package state

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	"voltsight/internal/dedup"
)

// Entry is one vehicle's state.
type Entry struct {
	Tenant string
	VIN    string
	Window dedup.Window
	Latest []byte // marshalled TelemetryEvent with the highest sequence
	Dirty  bool
}

// Key is the Redis key of a vehicle.
func Key(tenant, vin string) string { return "t:" + tenant + ":v:" + vin }

// Store reads and writes entries in pipelined batches.
type Store struct{ R redis.UniversalClient }

const chunk = 4000

// Load fetches the entries for the given (tenant, vin) pairs. Missing vehicles come back as fresh entries.
func (s *Store) Load(ctx context.Context, ids []Ref) (map[string]*Entry, error) {
	out := make(map[string]*Entry, len(ids))
	for lo := 0; lo < len(ids); lo += chunk {
		hi := lo + chunk
		if hi > len(ids) {
			hi = len(ids)
		}
		pipe := s.R.Pipeline()
		cmds := make([]*redis.SliceCmd, hi-lo)
		for i, id := range ids[lo:hi] {
			cmds[i] = pipe.HMGet(ctx, Key(id.Tenant, id.VIN), "win", "ev")
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return nil, fmt.Errorf("load state: %w", err)
		}
		for i, id := range ids[lo:hi] {
			e := &Entry{Tenant: id.Tenant, VIN: id.VIN}
			vals, err := cmds[i].Result()
			if err != nil && err != redis.Nil {
				return nil, err
			}
			if len(vals) == 2 {
				if w, ok := vals[0].(string); ok {
					e.Window.Unmarshal([]byte(w))
				}
				if ev, ok := vals[1].(string); ok {
					e.Latest = []byte(ev)
				}
			}
			out[id.VIN] = e
		}
	}
	return out, nil
}

// Ref names a vehicle.
type Ref struct{ Tenant, VIN string }

// Save writes the dirty entries and clears their dirty flag.
func (s *Store) Save(ctx context.Context, entries []*Entry) error {
	for lo := 0; lo < len(entries); lo += chunk {
		hi := lo + chunk
		if hi > len(entries) {
			hi = len(entries)
		}
		pipe := s.R.Pipeline()
		for _, e := range entries[lo:hi] {
			pipe.HSet(ctx, Key(e.Tenant, e.VIN), "win", e.Window.Marshal(), "ev", e.Latest, "seq", strconv.FormatUint(e.Window.High, 10))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		for _, e := range entries[lo:hi] {
			e.Dirty = false
		}
	}
	return nil
}
