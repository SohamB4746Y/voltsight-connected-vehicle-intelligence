package roadnet

import (
	"math"
	"math/rand/v2"
	"testing"
)

func city() *Graph { return Generate("test", 13.0827, 80.2707, 60, 150, 7) }

func TestDeterministic(t *testing.T) {
	a, b := city(), city()
	for i := range a.Lat {
		if a.Lat[i] != b.Lat[i] || a.Lon[i] != b.Lon[i] {
			t.Fatal("node positions differ between identical generations")
		}
	}
	c := Generate("test", 13.0827, 80.2707, 60, 150, 8)
	same := true
	for i := range a.Lat {
		same = same && a.Lat[i] == c.Lat[i]
	}
	if same {
		t.Fatal("different seeds must change the jitter")
	}
}

func TestShapeAndConnectivity(t *testing.T) {
	g := city()
	if g.Nodes() != 3600 {
		t.Fatalf("nodes = %d", g.Nodes())
	}
	// interior nodes have degree 4, corners 2: directed edges = 2 * 2*n*(n-1)
	if want := 2 * 2 * g.N * (g.N - 1); g.Edges() != want {
		t.Fatalf("edges = %d want %d", g.Edges(), want)
	}
	seen := make([]bool, g.Nodes())
	queue := []int32{0}
	seen[0] = true
	count := 1
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for e := g.AdjStart[v]; e < g.AdjStart[v+1]; e++ {
			if w := g.AdjTo[e]; !seen[w] {
				seen[w] = true
				count++
				queue = append(queue, w)
			}
		}
	}
	if count != g.Nodes() {
		t.Fatalf("graph is not connected: reached %d of %d", count, g.Nodes())
	}
}

func TestEdgesAreSymmetricWithPlausibleLengthsAndClasses(t *testing.T) {
	g := city()
	classes := map[uint8]int{}
	for v := int32(0); v < int32(g.Nodes()); v++ {
		for e := g.AdjStart[v]; e < g.AdjStart[v+1]; e++ {
			w := g.AdjTo[e]
			back, bc, ok := g.EdgeBetween(w, v)
			if !ok || back != g.AdjLenM[e] || bc != g.AdjClass[e] {
				t.Fatalf("edge %d-%d is not symmetric", v, w)
			}
			if l := g.AdjLenM[e]; l < 90 || l > 210 {
				t.Fatalf("edge length %f m implausible for 150 m blocks", l)
			}
			classes[g.AdjClass[e]]++
		}
	}
	for _, c := range []uint8{ClassLocal, ClassCollect, ClassArterial} {
		if classes[c] == 0 {
			t.Fatalf("no edges of class %d", c)
		}
	}
	if SpeedKmh(ClassArterial) <= SpeedKmh(ClassCollect) || SpeedKmh(ClassCollect) <= SpeedKmh(ClassLocal) {
		t.Fatal("speed limits must increase with class")
	}
	if _, _, ok := g.EdgeBetween(0, int32(g.Nodes()-1)); ok {
		t.Fatal("non-adjacent nodes reported as an edge")
	}
}

func TestNearestNodeMatchesBruteForce(t *testing.T) {
	g := city()
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 500; i++ {
		lat := 13.0827 + (r.Float64()-0.5)*0.06
		lon := 80.2707 + (r.Float64()-0.5)*0.06
		got := g.NearestNode(lat, lon)
		best, bd := int32(0), math.MaxFloat64
		for v := range g.Lat {
			if d := DistM(lat, lon, float64(g.Lat[v]), float64(g.Lon[v])); d < bd {
				best, bd = int32(v), d
			}
		}
		dg := DistM(lat, lon, float64(g.Lat[got]), float64(g.Lon[got]))
		if got != best && dg > bd+1 { // identical up to ties
			t.Fatalf("NearestNode(%f,%f)=%d (%.1f m) but brute force says %d (%.1f m)", lat, lon, got, dg, best, bd)
		}
	}
	// far outside the city (north-west) snaps to the north-west border node instead of failing
	if r, c := g.RC(g.NearestNode(40, 10)); r != g.N-1 || c != 0 {
		t.Fatalf("outside point snapped to (row %d, col %d), want (%d, 0)", r, c, g.N-1)
	}
}

func TestRCandID(t *testing.T) {
	g := city()
	for _, id := range []int32{0, 1, 59, 60, 3599} {
		r, c := g.RC(id)
		if g.ID(r, c) != id {
			t.Fatalf("RC/ID round trip failed for %d", id)
		}
	}
}
