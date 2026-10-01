// Package rangerisk decides, for every telemetry sample, whether a vehicle can still reach a charger.
//
// The core is a road-graph overlay: for every node, the road distance to the nearest *available* charger a
// tenant may use. It is a multi-source Dijkstra (all chargers are sources at distance 0; the graph is
// undirected, so a search from the chargers gives the distance *to* the nearest charger from every node).
// A per-event decision is then one O(1) array lookup. When a charger changes status the overlay is updated
// incrementally (only the part of the map that charger served is recomputed), which keeps the decision
// current within milliseconds of a status event.
package rangerisk

import (
	"math"

	"voltsight/internal/roadnet"
)

// Adj is an undirected graph in CSR form with edge lengths in metres.
type Adj struct {
	Start []int32 // len = nodes+1
	To    []int32
	W     []float32
}

// AdjOf views a road network as an Adj without copying.
func AdjOf(g *roadnet.Graph) Adj { return Adj{Start: g.AdjStart, To: g.AdjTo, W: g.AdjLenM} }

// Nodes returns the node count.
func (a Adj) Nodes() int { return len(a.Start) - 1 }

// Inf marks an unreachable node.
var Inf = float32(math.Inf(1))

// Source is a charger located at a graph node. IDs are chosen by the caller and must be unique per overlay.
type Source struct {
	Node int32
	ID   int32
}

type item struct {
	d    float32
	node int32
}

// Overlay holds, for every node, the distance to and the id of the nearest active source.
type Overlay struct {
	adj    Adj
	Dist   []float32
	Near   []int32 // source id, -1 when unreachable
	active map[int32]int32
	byNode map[int32][]int32
	heap   []item
}

// NewOverlay returns an overlay with no sources (everything unreachable).
func NewOverlay(adj Adj) *Overlay {
	n := adj.Nodes()
	o := &Overlay{adj: adj, Dist: make([]float32, n), Near: make([]int32, n),
		active: map[int32]int32{}, byNode: map[int32][]int32{}}
	o.reset()
	return o
}

func (o *Overlay) reset() {
	for i := range o.Dist {
		o.Dist[i], o.Near[i] = Inf, -1
	}
}

// Lookup returns the distance (metres) to the nearest available source and its id, or (Inf, -1).
// It is a pair of array reads: O(1).
func (o *Overlay) Lookup(node int32) (float32, int32) { return o.Dist[node], o.Near[node] }

// Sources returns the number of active sources.
func (o *Overlay) Sources() int { return len(o.active) }

// Rebuild discards everything and recomputes the overlay from scratch: O((V+E) log V).
func (o *Overlay) Rebuild(sources []Source) {
	o.reset()
	o.active = map[int32]int32{}
	o.byNode = map[int32][]int32{}
	o.heap = o.heap[:0]
	for _, s := range sources {
		o.register(s)
		if o.Dist[s.Node] > 0 {
			o.Dist[s.Node], o.Near[s.Node] = 0, s.ID
			o.push(item{0, s.Node})
		}
	}
	o.run()
}

func (o *Overlay) register(s Source) {
	o.active[s.ID] = s.Node
	o.byNode[s.Node] = append(o.byNode[s.Node], s.ID)
}

// Add activates a source (a charger that became available). Distances can only shrink, so the search starts
// at the new source and stops wherever it no longer improves the existing overlay.
func (o *Overlay) Add(s Source) {
	if _, ok := o.active[s.ID]; ok {
		return
	}
	o.register(s)
	if o.Dist[s.Node] > 0 {
		o.Dist[s.Node], o.Near[s.Node] = 0, s.ID
		o.heap = o.heap[:0]
		o.push(item{0, s.Node})
		o.run()
	}
}

// Remove deactivates a source (a charger that went out of service). Only the nodes it served can get worse:
// they are invalidated and re-seeded from their still-valid neighbours (whose distances cannot change), then
// the usual Dijkstra settles them. Cost is proportional to the size of the removed charger's catchment.
func (o *Overlay) Remove(id int32) {
	node, ok := o.active[id]
	if !ok {
		return
	}
	delete(o.active, id)
	ids := o.byNode[node]
	for i, x := range ids {
		if x == id {
			ids = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(ids) == 0 {
		delete(o.byNode, node)
	} else {
		o.byNode[node] = ids
	}

	var region []int32
	for n, near := range o.Near {
		if near == id {
			region = append(region, int32(n))
			o.Dist[n], o.Near[n] = Inf, -1
		}
	}
	o.heap = o.heap[:0]
	// a different charger at the same node still serves it at distance 0
	if len(ids) > 0 {
		o.Dist[node], o.Near[node] = 0, ids[0]
		o.push(item{0, node})
	}
	for _, n := range region {
		for e := o.adj.Start[n]; e < o.adj.Start[n+1]; e++ {
			nb := o.adj.To[e]
			if o.Near[nb] < 0 {
				continue // also invalid (or unreachable)
			}
			if d := o.Dist[nb] + o.adj.W[e]; d < o.Dist[n] {
				o.Dist[n], o.Near[n] = d, o.Near[nb]
			}
		}
		if o.Dist[n] < Inf {
			o.push(item{o.Dist[n], n})
		}
	}
	o.run()
}

// run settles the heap (lazy deletion).
func (o *Overlay) run() {
	for len(o.heap) > 0 {
		it := o.pop()
		if it.d > o.Dist[it.node] {
			continue
		}
		src := o.Near[it.node]
		for e := o.adj.Start[it.node]; e < o.adj.Start[it.node+1]; e++ {
			nb := o.adj.To[e]
			if nd := it.d + o.adj.W[e]; nd < o.Dist[nb] {
				o.Dist[nb], o.Near[nb] = nd, src
				o.push(item{nd, nb})
			}
		}
	}
}

func (o *Overlay) push(it item) {
	o.heap = append(o.heap, it)
	i := len(o.heap) - 1
	for i > 0 {
		p := (i - 1) / 2
		if o.heap[p].d <= o.heap[i].d {
			break
		}
		o.heap[p], o.heap[i] = o.heap[i], o.heap[p]
		i = p
	}
}

func (o *Overlay) pop() item {
	top := o.heap[0]
	last := len(o.heap) - 1
	o.heap[0] = o.heap[last]
	o.heap = o.heap[:last]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < last && o.heap[l].d < o.heap[m].d {
			m = l
		}
		if r < last && o.heap[r].d < o.heap[m].d {
			m = r
		}
		if m == i {
			break
		}
		o.heap[m], o.heap[i] = o.heap[i], o.heap[m]
		i = m
	}
	return top
}
