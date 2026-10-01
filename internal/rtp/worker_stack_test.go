//go:build stack

package rtp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"

	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/dedup"
	"voltsight/internal/dotenv"
	"voltsight/internal/state"
	"voltsight/internal/testkit"
)

func redisClient(t *testing.T) *redis.Client {
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD")})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { rdb.Close() })
	return rdb
}

func cleanTenant(t *testing.T, rdb *redis.Client, tenant string) {
	t.Cleanup(func() {
		ctx := context.Background()
		iter := rdb.Scan(ctx, 0, "t:"+tenant+":v:*", 1000).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
	})
}

// stateHash fingerprints every vehicle's stored state (latest event, highest sequence, dedup window).
func stateHash(t *testing.T, rdb *redis.Client, tenant string, vins []string) string {
	ctx := context.Background()
	sorted := append([]string(nil), vins...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, v := range sorted {
		m, err := rdb.HGetAll(ctx, state.Key(tenant, v)).Result()
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(v))
		h.Write([]byte(m["seq"]))
		h.Write([]byte(m["win"]))
		h.Write([]byte(m["ev"]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func runWorker(t *testing.T, cfg Config, rdb *redis.Client) (*Worker, context.CancelFunc, *sync.WaitGroup) {
	w, err := New(cfg, &state.Store{R: rdb})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := w.Run(ctx); err != nil {
			t.Errorf("worker: %v", err)
		}
	}()
	return w, cancel, &wg
}

func checkState(t *testing.T, rdb *redis.Client, sc *testkit.Scenario) {
	t.Helper()
	ctx := context.Background()
	for _, v := range sc.VINs {
		m, err := rdb.HGetAll(ctx, state.Key(sc.Tenant, v)).Result()
		if err != nil || len(m) == 0 {
			t.Fatalf("no state for %s: %v", v, err)
		}
		var ev telemetryv1.TelemetryEvent
		if err := proto.Unmarshal([]byte(m["ev"]), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Seq != sc.High[v] || m["seq"] == "" {
			t.Fatalf("%s: latest event seq %d, want the highest seen %d", v, ev.Seq, sc.High[v])
		}
		var w dedup.Window
		if !w.Unmarshal([]byte(m["win"])) || w.High != sc.High[v] {
			t.Fatalf("%s: stored window high %d, want %d", v, w.High, sc.High[v])
		}
	}
}

// G4.4 correctness with duplicates, reordering and stale replays against a reference model.
func TestWorkerDedupAndLatestState(t *testing.T) {
	rdb := redisClient(t)
	sc := testkit.NewScenario(7, 150, 400, 0.05, 0.08, 0.01)
	cleanTenant(t, rdb, sc.Tenant)
	topic := testkit.Topic(t, "t4.rtp", 6)
	testkit.Produce(t, topic, sc.Events)

	group := "g-" + uuid.NewString()[:8]
	w, cancel, wg := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: group, MaxPoll: 3000}, rdb)
	testkit.WaitDrained(t, group, topic, 60*time.Second)
	cancel()
	wg.Wait()
	w.Close()

	st := &w.Stats
	if st.Records.Load() != int64(len(sc.Events)) || st.DecodeErrors.Load() != 0 {
		t.Fatalf("processed %d of %d events (%d decode errors)", st.Records.Load(), len(sc.Events), st.DecodeErrors.Load())
	}
	got := map[dedup.Verdict]int{dedup.New: int(st.New.Load()), dedup.Late: int(st.Late.Load()), dedup.Duplicate: int(st.Duplicate.Load()), dedup.Stale: int(st.Stale.Load())}
	for v, want := range sc.Expected {
		if got[v] != want {
			t.Errorf("verdict %v: worker %d, reference %d", v, got[v], want)
		}
	}
	if sc.Expected[dedup.Duplicate] == 0 || sc.Expected[dedup.Late] == 0 || sc.Expected[dedup.Stale] == 0 {
		t.Fatalf("scenario must exercise every verdict: %v", sc.Expected)
	}
	checkState(t, rdb, sc)
	t.Logf("verdicts over %d events: new=%d late=%d duplicate=%d stale=%d (stale seen before by Bloom: %d)",
		len(sc.Events), got[dedup.New], got[dedup.Late], got[dedup.Duplicate], got[dedup.Stale], st.StaleSeenBefore.Load())
}

// G4.5 replay rebuilds identical state from the log.
func TestReplayRebuildsIdenticalState(t *testing.T) {
	rdb := redisClient(t)
	sc := testkit.NewScenario(11, 120, 300, 0.05, 0.08, 0.01)
	cleanTenant(t, rdb, sc.Tenant)
	topic := testkit.Topic(t, "t4.replay", 4)
	testkit.Produce(t, topic, sc.Events)

	run := func(group string) string {
		w, cancel, wg := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: group}, rdb)
		testkit.WaitDrained(t, group, topic, 60*time.Second)
		cancel()
		wg.Wait()
		w.Close()
		return stateHash(t, rdb, sc.Tenant, sc.VINs)
	}
	first := run("g1-" + uuid.NewString()[:8])
	checkState(t, rdb, sc)

	ctx := context.Background()
	for _, v := range sc.VINs { // lose all state, as after a Redis loss
		rdb.Del(ctx, state.Key(sc.Tenant, v))
	}
	if stateHash(t, rdb, sc.Tenant, sc.VINs) == first {
		t.Fatal("state was not actually wiped")
	}
	second := run("g2-" + uuid.NewString()[:8]) // a brand-new group replays from the earliest offset
	if first != second {
		t.Fatalf("replay produced different state:\n first  %s\n second %s", first, second)
	}
	checkState(t, rdb, sc)
}

// G4.6 a worker killed mid-stream (no clean shutdown) loses nothing: a restart in the same group converges
// to exactly the state of an uninterrupted run.
func TestCrashMidStreamLosesNothing(t *testing.T) {
	rdb := redisClient(t)
	sc := testkit.NewScenario(13, 150, 400, 0.05, 0.08, 0.01)
	cleanTenant(t, rdb, sc.Tenant)
	topic := testkit.Topic(t, "t4.crash", 6)
	testkit.Produce(t, topic, sc.Events)

	group := "g-" + uuid.NewString()[:8]
	a, cancelA, wgA := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: group, MaxPoll: 1500}, rdb)
	deadline := time.Now().Add(30 * time.Second)
	for a.Stats.Records.Load() < int64(len(sc.Events))/4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	processedBeforeCrash := a.Stats.Records.Load()
	a.Kill() // crash: no final flush, no leave-group
	cancelA()
	wgA.Wait()
	if processedBeforeCrash >= int64(len(sc.Events)) {
		t.Skip("worker finished before it could be killed; scenario too small for this machine")
	}

	b, cancelB, wgB := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: group, MaxPoll: 1500}, rdb)
	testkit.WaitDrained(t, group, topic, 90*time.Second)
	cancelB()
	wgB.Wait()
	b.Close()

	checkState(t, rdb, sc)
	crashed := stateHash(t, rdb, sc.Tenant, sc.VINs)

	// the reference: the same stream processed once, uninterrupted, by a fresh group
	for _, v := range sc.VINs {
		rdb.Del(context.Background(), state.Key(sc.Tenant, v))
	}
	g2 := "ref-" + uuid.NewString()[:8]
	c, cancelC, wgC := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: g2}, rdb)
	testkit.WaitDrained(t, g2, topic, 90*time.Second)
	cancelC()
	wgC.Wait()
	c.Close()
	if ref := stateHash(t, rdb, sc.Tenant, sc.VINs); ref != crashed {
		t.Fatalf("state after crash+restart differs from the uninterrupted run:\n crashed %s\n clean   %s", crashed, ref)
	}
	t.Logf("worker killed after %d of %d events; restart reprocessed from the committed offsets; final state identical to a clean run (%d processed by the replacement)",
		processedBeforeCrash, len(sc.Events), b.Stats.Records.Load())
}

// The top-K of diagnostic codes comes from a Count-Min sketch fed by the worker.
func TestWorkerTracksTopDTCs(t *testing.T) {
	rdb := redisClient(t)
	sc := testkit.NewScenario(17, 50, 120, 0, 0, 0)
	cleanTenant(t, rdb, sc.Tenant)
	for i, e := range sc.Events {
		switch {
		case i%3 == 0:
			e.Dtc = []string{"P0A7F"}
		case i%7 == 0:
			e.Dtc = []string{"P0A0D"}
		case i%97 == 0:
			e.Dtc = []string{"P0AA6"}
		}
	}
	topic := testkit.Topic(t, "t4.dtc", 3)
	testkit.Produce(t, topic, sc.Events)
	group := "g-" + uuid.NewString()[:8]
	w, cancel, wg := runWorker(t, Config{Brokers: testkit.Brokers, Topic: topic, Group: group}, rdb)
	testkit.WaitDrained(t, group, topic, 60*time.Second)
	cancel()
	wg.Wait()
	w.Close()
	top := w.TopDTCs()
	if len(top) < 3 || top[0].Key != "P0A7F" || top[1].Key != "P0A0D" {
		t.Fatalf("top DTCs = %v", top)
	}
}
