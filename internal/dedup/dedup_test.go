package dedup

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

// G4.1 property test: the window agrees with a reference implementation on every verdict.
func TestWindowAgreesWithReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 40; trial++ {
		var w Window
		seen := map[uint64]bool{}
		var max uint64
		// a stream with jitter (reordering), duplicates and occasional big jumps
		base := uint64(1 + r.IntN(1000))
		for i := 0; i < 5000; i++ {
			var seq uint64
			switch r.IntN(10) {
			case 0: // duplicate of something recent
				seq = base - uint64(r.IntN(40))
			case 1: // far in the past
				seq = uint64(1 + r.IntN(int(base)))
			case 2: // jump ahead
				base += uint64(r.IntN(500))
				seq = base
			default:
				base += uint64(r.IntN(3))
				seq = base - uint64(r.IntN(30))
			}
			if seq == 0 {
				seq = 1
			}
			var want Verdict
			switch {
			case max == 0 || seq > max:
				want = New
			case max-seq >= WindowBits:
				want = Stale
			case seen[seq]:
				want = Duplicate
			default:
				want = Late
			}
			if got := w.Observe(seq); got != want {
				t.Fatalf("trial %d step %d seq %d (max %d): got %v want %v", trial, i, seq, max, got, want)
			}
			if want == New || want == Late {
				seen[seq] = true
			}
			if seq > max {
				max = seq
			}
		}
		if w.High != max {
			t.Fatalf("High = %d, want %d", w.High, max)
		}
	}
}

func TestWindowEdgeCases(t *testing.T) {
	var w Window
	if w.Observe(0) != Stale || w.High != 0 {
		t.Fatal("seq 0 is invalid and must not be recorded")
	}
	if w.Observe(10) != New || w.Observe(10) != Duplicate || w.Observe(9) != Late || w.Observe(9) != Duplicate {
		t.Fatal("basic sequence of verdicts wrong")
	}
	// exactly at the window edge
	var e Window
	e.Observe(1000)
	if e.Observe(1000-(WindowBits-1)) != Late || e.Observe(1000-WindowBits) != Stale {
		t.Fatal("window edge off by one")
	}
	// a jump larger than the window clears the memory
	e.Observe(5000)
	if e.Count() != 1 || e.Observe(1000) != Stale {
		t.Fatalf("after a big jump the window must hold only the new high (count %d)", e.Count())
	}
	// multi-word shift keeps older bits
	var s Window
	s.Observe(100)
	s.Observe(101)
	s.Observe(170) // shift by 69 = one word + 5 bits
	if s.Observe(100) != Duplicate || s.Observe(101) != Duplicate || s.Observe(150) != Late {
		t.Fatal("bits lost or invented by a multi-word shift")
	}
}

func TestWindowMarshalRoundTrip(t *testing.T) {
	var w Window
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 500; i++ {
		w.Observe(uint64(1000 + r.IntN(300)))
	}
	var back Window
	if !back.Unmarshal(w.Marshal()) || back != w {
		t.Fatal("marshal round trip changed the window")
	}
	if back.Unmarshal([]byte{1, 2, 3}) {
		t.Fatal("short input accepted")
	}
	if Duplicate.String() != "duplicate" || New.String() != "new" {
		t.Fatal("verdict names")
	}
}

// G4.2 Bloom filter: no false negatives, measured FP rate close to the design rate.
func TestBloomFalsePositiveRateAndNoFalseNegatives(t *testing.T) {
	const n, p = 200_000, 0.01
	b := NewBloom(n, p)
	for i := 0; i < n; i++ {
		b.Add(HashString(fmt.Sprintf("1HGCM82633A%06d:%d", i%100000, i)))
	}
	for i := 0; i < n; i++ {
		if !b.Has(HashString(fmt.Sprintf("1HGCM82633A%06d:%d", i%100000, i))) {
			t.Fatalf("false negative for item %d", i)
		}
	}
	fp := 0
	const probes = 400_000
	for i := 0; i < probes; i++ {
		if b.Has(HashString(fmt.Sprintf("absent-%d", i))) {
			fp++
		}
	}
	rate := float64(fp) / probes
	theory := math.Pow(1-math.Exp(-float64(b.Hashes())*float64(n)/float64(b.Bits())), float64(b.Hashes()))
	t.Logf("MEASURED Bloom: n=%d m=%d bits (%.1f bits/item) k=%d fill=%.2f; false-positive rate %.4f (design %.3f, theory %.4f)",
		n, b.Bits(), float64(b.Bits())/n, b.Hashes(), b.FillRatio(), rate, p, theory)
	if rate < p/2 || rate > p*2 {
		t.Fatalf("false-positive rate %.4f is far from the design rate %.3f", rate, p)
	}
	if math.Abs(rate-theory) > 0.004 {
		t.Fatalf("measured %.4f disagrees with theory %.4f", rate, theory)
	}
}

func TestRotatingBloomBoundsMemoryAndKeepsRecentHistory(t *testing.T) {
	r := NewRotating(10_000, 0.01)
	for i := 0; i < 15_000; i++ {
		r.Add(uint64(i)*7919 + 13)
	}
	for i := 5_000; i < 15_000; i++ { // the two most recent generations are still remembered
		if !r.Has(uint64(i)*7919 + 13) {
			t.Fatalf("recent item %d forgotten after one rotation", i)
		}
	}
	for i := 15_000; i < 40_000; i++ {
		r.Add(uint64(i)*7919 + 13)
	}
	forgot := 0
	for i := 0; i < 10_000; i++ {
		if !r.Has(uint64(i)*7919 + 13) {
			forgot++
		}
	}
	if forgot < 9_000 {
		t.Fatalf("only %d of 10000 old items were aged out: memory is not bounded", forgot)
	}
}

// G4.3 Count-Min: never under-counts, error within eps*N for almost every key, top-K finds the heavy hitters.
func TestCountMinBoundsAndTopK(t *testing.T) {
	const eps, delta = 0.0005, 0.01
	cm := NewCountMin(eps, delta)
	r := rand.New(rand.NewPCG(5, 6))
	zipf := rand.NewZipf(r, 1.2, 4, 50_000)
	truth := map[uint64]uint32{}
	const n = 1_000_000
	topk := NewTopK(10, eps, delta)
	for i := 0; i < n; i++ {
		k := zipf.Uint64()
		truth[k]++
		cm.Add(HashString(fmt.Sprint("dtc-", k)))
		topk.Add(fmt.Sprint("dtc-", k))
	}
	bad, over := 0, 0
	for k, c := range truth {
		est := cm.Estimate(HashString(fmt.Sprint("dtc-", k)))
		if est < c {
			t.Fatalf("under-count for key %d: estimate %d < true %d", k, est, c)
		}
		if float64(est-c) > eps*n {
			bad++
		}
		over += int(est - c)
	}
	if frac := float64(bad) / float64(len(truth)); frac > delta*2 {
		t.Fatalf("%.3f of keys exceed the eps*N error bound (allowed about %.3f)", frac, delta)
	}
	t.Logf("MEASURED Count-Min: %d keys, %d events, width %d x depth %d; mean over-estimate %.2f; keys over eps*N: %d",
		len(truth), n, cm.width, len(cm.rows), float64(over)/float64(len(truth)), bad)

	type kv struct {
		k uint64
		c uint32
	}
	var all []kv
	for k, c := range truth {
		all = append(all, kv{k, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].c > all[j].c })
	got := map[string]bool{}
	for _, it := range topk.Top() {
		got[it.Key] = true
	}
	hit := 0
	for _, x := range all[:10] {
		if got[fmt.Sprint("dtc-", x.k)] {
			hit++
		}
	}
	if hit < 9 {
		t.Fatalf("top-10 recall %d/10", hit)
	}
	top := topk.Top()
	for i := 1; i < len(top); i++ {
		if top[i].Count > top[i-1].Count {
			t.Fatal("Top() must be ordered by descending count")
		}
	}
	if cm.Total() != n {
		t.Fatalf("total %d", cm.Total())
	}
}

func BenchmarkWindowObserve(b *testing.B) {
	var w Window
	for i := 0; i < b.N; i++ {
		w.Observe(uint64(i/3 + 1))
	}
}

func BenchmarkBloomAddHas(b *testing.B) {
	bl := NewBloom(1_000_000, 0.01)
	for i := 0; i < b.N; i++ {
		h := uint64(i) * 0x9e3779b97f4a7c15
		bl.Add(h)
		bl.Has(h)
	}
}
