package rangerisk

import (
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"testing"

	"voltsight/internal/roadnet"
)

// randGraph builds an undirected graph with random positive weights; p controls density, so low p gives
// disconnected components (unreachable nodes must stay Inf).
func randGraph(r *rand.Rand, n int, p float64) Adj {
	type e struct {
		to int32
		w  float32
	}
	adj := make([][]e, n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if r.Float64() < p {
				w := float32(10 + r.Float64()*990)
				adj[i] = append(adj[i], e{int32(j), w})
				adj[j] = append(adj[j], e{int32(i), w})
			}
		}
	}
	a := Adj{Start: make([]int32, n+1)}
	for i := 0; i < n; i++ {
		a.Start[i] = int32(len(a.To))
		for _, x := range adj[i] {
			a.To, a.W = append(a.To, x.to), append(a.W, x.w)
		}
	}
	a.Start[n] = int32(len(a.To))
	return a
}

// refDist is an independent O(V^2) Dijkstra (no heap) from one node.
func refDist(a Adj, src int32) []float64 {
	n := a.Nodes()
	d := make([]float64, n)
	done := make([]bool, n)
	for i := range d {
		d[i] = math.Inf(1)
	}
	d[src] = 0
	for {
		u, best := -1, math.Inf(1)
		for i := 0; i < n; i++ {
			if !done[i] && d[i] < best {
				u, best = i, d[i]
			}
		}
		if u < 0 {
			return d
		}
		done[u] = true
		for e := a.Start[u]; e < a.Start[u+1]; e++ {
			if nd := d[u] + float64(a.W[e]); nd < d[a.To[e]] {
				d[a.To[e]] = nd
			}
		}
	}
}

// checkAgainstReference verifies Dist is the minimum over the active sources and Near names a source that
// attains it.
func checkAgainstReference(t *testing.T, a Adj, o *Overlay, active map[int32]int32, ctx string) {
	t.Helper()
	per := map[int32][]float64{}
	for id, node := range active {
		per[id] = refDist(a, node)
	}
	for n := 0; n < a.Nodes(); n++ {
		best := math.Inf(1)
		for _, d := range per {
			best = math.Min(best, d[n])
		}
		got := float64(o.Dist[n])
		if math.IsInf(best, 1) {
			if !math.IsInf(got, 1) || o.Near[n] != -1 {
				t.Fatalf("%s: node %d unreachable but overlay says %v near %d", ctx, n, got, o.Near[n])
			}
			continue
		}
		if math.Abs(got-best) > 1e-3*(1+best) {
			t.Fatalf("%s: node %d distance %v, reference %v", ctx, n, got, best)
		}
		ref, ok := per[o.Near[n]]
		if !ok {
			t.Fatalf("%s: node %d points at inactive source %d", ctx, n, o.Near[n])
		}
		if math.Abs(ref[n]-best) > 1e-3*(1+best) {
			t.Fatalf("%s: node %d nearest source %d is at %v, best is %v", ctx, n, o.Near[n], ref[n], best)
		}
	}
}

// G6.1
func TestOverlayRebuildMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for trial := 0; trial < 120; trial++ {
		n := 5 + r.IntN(60)
		a := randGraph(r, n, []float64{0.02, 0.06, 0.15, 0.4}[trial%4])
		k := r.IntN(8) // 0 sources is valid: everything unreachable
		var srcs []Source
		active := map[int32]int32{}
		for i := 0; i < k; i++ {
			s := Source{Node: int32(r.IntN(n)), ID: int32(i)} // duplicates of a node = co-located chargers
			srcs = append(srcs, s)
			active[s.ID] = s.Node
		}
		o := NewOverlay(a)
		o.Rebuild(srcs)
		checkAgainstReference(t, a, o, active, "rebuild")
	}
}

// G6.2: random walks of status changes keep the incrementally maintained overlay equal to a full rebuild.
func TestOverlayIncrementalEqualsRebuild(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for trial := 0; trial < 60; trial++ {
		n := 10 + r.IntN(70)
		a := randGraph(r, n, []float64{0.04, 0.1, 0.3}[trial%3])
		o := NewOverlay(a)
		all := make([]Source, 3+r.IntN(10))
		for i := range all {
			all[i] = Source{Node: int32(r.IntN(n)), ID: int32(i)}
		}
		active := map[int32]int32{}
		for step := 0; step < 40; step++ {
			s := all[r.IntN(len(all))]
			if _, on := active[s.ID]; on {
				o.Remove(s.ID)
				delete(active, s.ID)
			} else {
				o.Add(s)
				active[s.ID] = s.Node
			}
			checkAgainstReference(t, a, o, active, "incremental")
			// and identical to a fresh rebuild
			fresh := NewOverlay(a)
			var srcs []Source
			for id, node := range active {
				srcs = append(srcs, Source{Node: node, ID: id})
			}
			fresh.Rebuild(srcs)
			for i := range fresh.Dist {
				if math.Abs(float64(fresh.Dist[i]-o.Dist[i])) > 1e-3 && !(math.IsInf(float64(fresh.Dist[i]), 1) && math.IsInf(float64(o.Dist[i]), 1)) {
					t.Fatalf("trial %d step %d: node %d incremental %v != rebuild %v", trial, step, i, o.Dist[i], fresh.Dist[i])
				}
			}
		}
	}
}

func TestOverlayRemoveUnknownAndDuplicateAddAreNoops(t *testing.T) {
	a := randGraph(rand.New(rand.NewPCG(5, 6)), 20, 0.3)
	o := NewOverlay(a)
	o.Add(Source{Node: 3, ID: 7})
	before := append([]float32(nil), o.Dist...)
	o.Add(Source{Node: 9, ID: 7}) // same id again: ignored
	o.Remove(99)
	for i := range before {
		if before[i] != o.Dist[i] {
			t.Fatal("no-op changed the overlay")
		}
	}
	if o.Sources() != 1 {
		t.Fatalf("sources = %d", o.Sources())
	}
}

// the real city graph: overlay distances equal an independent per-source Dijkstra for every node
func TestOverlayOnCityGraph(t *testing.T) {
	g := roadnet.Generate("t", 13.0, 80.2, 40, 150, 7)
	a := AdjOf(g)
	r := rand.New(rand.NewPCG(8, 9))
	o := NewOverlay(a)
	active := map[int32]int32{}
	var srcs []Source
	for i := 0; i < 12; i++ {
		s := Source{Node: int32(r.IntN(g.Nodes())), ID: int32(i)}
		srcs = append(srcs, s)
		active[s.ID] = s.Node
	}
	o.Rebuild(srcs)
	checkAgainstReference(t, a, o, active, "city")
	for _, id := range []int32{2, 5, 9} { // an outage of three chargers
		o.Remove(id)
		delete(active, id)
	}
	checkAgainstReference(t, a, o, active, "city after outage")
}

// G6.3
func TestAStarMatchesDijkstraAndExpandsLess(t *testing.T) {
	g := roadnet.Generate("t", 13.0, 80.2, 60, 150, 11)
	a := AdjOf(g)
	c := Coords{Lat: g.Lat, Lon: g.Lon}
	r := rand.New(rand.NewPCG(10, 11))
	var aExp, dExp int
	for i := 0; i < 80; i++ {
		src, dst := int32(r.IntN(g.Nodes())), int32(r.IntN(g.Nodes()))
		path, dist, exp, ok := AStar(a, c, src, dst)
		if !ok {
			t.Fatal("grid is connected")
		}
		ref := refDistFast(a, src, dst)
		if math.Abs(float64(dist)-ref) > 1e-2*(1+ref) {
			t.Fatalf("A* %v != Dijkstra %v for %d->%d", dist, ref, src, dst)
		}
		if path[0] != src || path[len(path)-1] != dst {
			t.Fatal("path endpoints")
		}
		var sum float64 // the returned path really has that length
		for k := 1; k < len(path); k++ {
			l, _, ok := g.EdgeBetween(path[k-1], path[k])
			if !ok {
				t.Fatal("path uses a non-edge")
			}
			sum += float64(l)
		}
		if math.Abs(sum-float64(dist)) > 1e-2*(1+sum) {
			t.Fatalf("path length %v != reported %v", sum, dist)
		}
		aExp += exp
		dExp += refExpanded(a, src, dst)
	}
	if aExp >= dExp {
		t.Fatalf("A* expanded %d nodes, Dijkstra %d: the heuristic is not helping", aExp, dExp)
	}
	t.Logf("MEASURED: A* expanded %d nodes vs Dijkstra %d over 80 random routes (%.1fx fewer)", aExp, dExp, float64(dExp)/float64(aExp))
}

func TestAStarUnreachable(t *testing.T) {
	a := randGraph(rand.New(rand.NewPCG(12, 13)), 30, 0.0) // no edges
	c := Coords{Lat: make([]float32, 30), Lon: make([]float32, 30)}
	if _, _, _, ok := AStar(a, c, 0, 5); ok {
		t.Fatal("found a route in an empty graph")
	}
	if p, d, _, ok := AStar(a, c, 4, 4); !ok || d != 0 || len(p) != 1 {
		t.Fatal("src == dst must be a zero-length route")
	}
}

func refDistFast(a Adj, src, dst int32) float64 { return refDist(a, src)[dst] }

// refExpanded counts nodes a plain Dijkstra settles before reaching dst.
func refExpanded(a Adj, src, dst int32) int {
	o := &Overlay{adj: a}
	n := a.Nodes()
	d := make([]float32, n)
	for i := range d {
		d[i] = Inf
	}
	d[src] = 0
	o.push(item{0, src})
	closed := make([]bool, n)
	cnt := 0
	for len(o.heap) > 0 {
		it := o.pop()
		if closed[it.node] {
			continue
		}
		closed[it.node] = true
		cnt++
		if it.node == dst {
			return cnt
		}
		for e := a.Start[it.node]; e < a.Start[it.node+1]; e++ {
			if nd := it.d + a.W[e]; nd < d[a.To[e]] {
				d[a.To[e]] = nd
				o.push(item{nd, a.To[e]})
			}
		}
	}
	return cnt
}

// G6.4 and the cost of the overlay maintenance at city scale (48,400 nodes, 300 chargers).
func cityOverlay(b *testing.B) (*Overlay, []Source) {
	g := roadnet.Generate("bench", 13.0, 80.2, 220, 150, 1)
	r := rand.New(rand.NewPCG(1, 1))
	srcs := make([]Source, 300)
	for i := range srcs {
		srcs[i] = Source{Node: int32(r.IntN(g.Nodes())), ID: int32(i)}
	}
	o := NewOverlay(AdjOf(g))
	o.Rebuild(srcs)
	return o, srcs
}

func BenchmarkLookup(b *testing.B) {
	o, _ := cityOverlay(b)
	b.ReportAllocs()
	var sink float32
	for i := 0; i < b.N; i++ {
		d, _ := o.Lookup(int32(i % len(o.Dist)))
		sink += d
	}
	_ = sink
}

func BenchmarkRebuild48kNodes300Chargers(b *testing.B) {
	o, srcs := cityOverlay(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		o.Rebuild(srcs)
	}
}

func BenchmarkRemoveOneCharger(b *testing.B) {
	o, srcs := cityOverlay(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := srcs[i%len(srcs)]
		o.Remove(s.ID)
		o.Add(s)
	}
}
