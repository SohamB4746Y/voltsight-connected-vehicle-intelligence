package rangerisk

import "math"

// Coords gives node positions (degrees) for the A* heuristic.
type Coords struct{ Lat, Lon []float32 }

// AStar finds the shortest road route from src to dst. The heuristic is the straight-line distance, which never
// exceeds the road distance (edge lengths are straight-line lengths between their end points), so the route is
// optimal. It returns the node path, its length in metres and how many nodes were expanded; ok=false if
// dst is unreachable. It is used to show the route behind an alert, not on the per-event path.
func AStar(adj Adj, c Coords, src, dst int32) (path []int32, dist float32, expanded int, ok bool) {
	n := adj.Nodes()
	g := make([]float32, n)
	prev := make([]int32, n)
	closed := make([]bool, n)
	for i := range g {
		g[i], prev[i] = Inf, -1
	}
	h := func(a int32) float32 {
		const mPerDeg = 111_320.0
		dy := float64(c.Lat[a]-c.Lat[dst]) * mPerDeg
		dx := float64(c.Lon[a]-c.Lon[dst]) * mPerDeg * math.Cos(float64(c.Lat[a]+c.Lat[dst])/2*math.Pi/180)
		// 0.999: the equirectangular edge lengths are not an exact metric; this keeps the heuristic admissible
		return float32(0.999 * math.Hypot(dx, dy))
	}
	o := &Overlay{adj: adj} // reuse the binary heap, keyed by f = g + h
	g[src] = 0
	o.push(item{h(src), src})
	for len(o.heap) > 0 {
		it := o.pop()
		u := it.node
		if closed[u] {
			continue
		}
		closed[u] = true
		expanded++
		if u == dst {
			for x := dst; x >= 0; x = prev[x] {
				path = append(path, x)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path, g[dst], expanded, true
		}
		for e := adj.Start[u]; e < adj.Start[u+1]; e++ {
			v := adj.To[e]
			if nd := g[u] + adj.W[e]; nd < g[v] && !closed[v] {
				g[v], prev[v] = nd, u
				o.push(item{nd + h(v), v})
			}
		}
	}
	return nil, Inf, expanded, false
}
