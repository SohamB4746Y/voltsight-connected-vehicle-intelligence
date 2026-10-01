package rangerisk

import (
	"sync"

	"voltsight/internal/roadnet"
)

// Charger is a charging point at a node of a city graph. Tenant "" means the public network.
type Charger struct {
	ID        string
	City      int
	Node      int32
	Tenant    string
	Available bool
}

type ovKey struct {
	city   int
	tenant string
}

// Engine owns one overlay per (city, tenant): a tenant sees the public chargers plus its own depot chargers,
// never another tenant's. Overlays are built lazily and kept current on charger status changes.
type Engine struct {
	mu       sync.RWMutex
	graphs   []*roadnet.Graph
	adj      []Adj
	chargers []Charger
	byID     map[string]int32
	overlays map[ovKey]*Overlay
}

// NewEngine creates the engine for the given city graphs and chargers.
func NewEngine(graphs []*roadnet.Graph, chargers []Charger) *Engine {
	e := &Engine{graphs: graphs, chargers: chargers, byID: make(map[string]int32, len(chargers)), overlays: map[ovKey]*Overlay{}}
	for _, g := range graphs {
		e.adj = append(e.adj, AdjOf(g))
	}
	for i, c := range chargers {
		e.byID[c.ID] = int32(i)
	}
	return e
}

func (e *Engine) visible(c *Charger, tenant string) bool { return c.Tenant == "" || c.Tenant == tenant }

func (e *Engine) overlay(city int, tenant string) *Overlay {
	k := ovKey{city, tenant}
	e.mu.RLock()
	o := e.overlays[k]
	e.mu.RUnlock()
	if o != nil {
		return o
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if o = e.overlays[k]; o != nil {
		return o
	}
	o = NewOverlay(e.adj[city])
	var srcs []Source
	for i := range e.chargers {
		c := &e.chargers[i]
		if c.City == city && c.Available && e.visible(c, tenant) {
			srcs = append(srcs, Source{Node: c.Node, ID: int32(i)})
		}
	}
	o.Rebuild(srcs)
	e.overlays[k] = o
	return o
}

// Lookup returns the road distance in metres from node to the nearest available charger the tenant may use and
// that charger's id. ok is false when none is reachable. The steady-state cost is one map read and two array
// reads under a read lock.
func (e *Engine) Lookup(city int, tenant string, node int32) (distM float32, chargerID string, ok bool) {
	o := e.overlay(city, tenant)
	e.mu.RLock()
	d, near := o.Lookup(node)
	var id string
	if near >= 0 {
		id = e.chargers[near].ID
	}
	e.mu.RUnlock()
	return d, id, near >= 0
}

// SetAvailable records a charger status change and updates every affected overlay incrementally. It reports
// whether anything changed (unknown chargers and repeated statuses are ignored, so replays are harmless).
func (e *Engine) SetAvailable(chargerID string, available bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.byID[chargerID]
	if !ok || e.chargers[i].Available == available {
		return false
	}
	c := &e.chargers[i]
	c.Available = available
	for k, o := range e.overlays {
		if k.city != c.City || !e.visible(c, k.tenant) {
			continue
		}
		if available {
			o.Add(Source{Node: c.Node, ID: i})
		} else {
			o.Remove(i)
		}
	}
	return true
}

// Available reports a charger's current status.
func (e *Engine) Available(chargerID string) (available, known bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	i, ok := e.byID[chargerID]
	if !ok {
		return false, false
	}
	return e.chargers[i].Available, true
}

// Graph returns the city graph.
func (e *Engine) Graph(city int) *roadnet.Graph { return e.graphs[city] }

// Route computes the A* road route from node to the given charger, for alert evidence.
func (e *Engine) Route(city int, node int32, chargerID string) (path []int32, distM float32, ok bool) {
	e.mu.RLock()
	i, known := e.byID[chargerID]
	var dst int32
	if known {
		dst = e.chargers[i].Node
	}
	e.mu.RUnlock()
	if !known {
		return nil, 0, false
	}
	g := e.graphs[city]
	path, distM, _, ok = AStar(e.adj[city], Coords{Lat: g.Lat, Lon: g.Lon}, node, dst)
	return path, distM, ok
}

// CountAvailable returns how many chargers of a city are currently available.
func (e *Engine) CountAvailable(city int) (available, total int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for i := range e.chargers {
		if e.chargers[i].City == city {
			total++
			if e.chargers[i].Available {
				available++
			}
		}
	}
	return
}
