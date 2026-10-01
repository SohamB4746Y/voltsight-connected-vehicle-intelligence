package batch

import (
	"sort"

	"voltsight/internal/geo"
)

// Stop is a long stationary period of a vehicle.
type Stop struct {
	VIN      string
	Lat, Lon float64
}

// Cluster is a group of stops in neighbouring geohash cells.
type Cluster struct {
	Cells    []string
	Stops    int
	Vehicles int
	Lat, Lon float64 // centroid
}

type unionFind struct{ parent, size []int }

func newUF(n int) *unionFind {
	u := &unionFind{parent: make([]int, n), size: make([]int, n)}
	for i := range u.parent {
		u.parent[i], u.size[i] = i, 1
	}
	return u
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path halving
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if u.size[ra] < u.size[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	u.size[ra] += u.size[rb]
}

// ClusterStops finds groups of stops at places that are not approved depots: stops are binned into geohash
// cells of the given precision; cells that touch (8-neighbourhood) are merged with union-find (connected
// components), O(n * alpha(n)). Clusters with fewer than minVehicles distinct vehicles are ignored; stops inside
// an approved cell are skipped.
func ClusterStops(stops []Stop, precision, minVehicles int, approved map[string]bool) []Cluster {
	type agg struct {
		stops    int
		vins     map[string]bool
		lat, lon float64
	}
	cells := map[string]*agg{}
	for _, s := range stops {
		h := geo.Encode(s.Lat, s.Lon, precision)
		if approved[h] {
			continue
		}
		a := cells[h]
		if a == nil {
			a = &agg{vins: map[string]bool{}}
			cells[h] = a
		}
		a.stops++
		a.vins[s.VIN] = true
		a.lat += s.Lat
		a.lon += s.Lon
	}
	keys := make([]string, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	idx := map[string]int{}
	for i, k := range keys {
		idx[k] = i
	}
	uf := newUF(len(keys))
	for _, k := range keys {
		for _, nb := range geo.Neighbors(k) {
			if j, ok := idx[nb]; ok {
				uf.union(idx[k], j)
			}
		}
	}
	groups := map[int]*Cluster{}
	vins := map[int]map[string]bool{}
	for _, k := range keys {
		r := uf.find(idx[k])
		c := groups[r]
		if c == nil {
			c = &Cluster{}
			groups[r], vins[r] = c, map[string]bool{}
		}
		a := cells[k]
		c.Cells = append(c.Cells, k)
		c.Stops += a.stops
		c.Lat += a.lat
		c.Lon += a.lon
		for v := range a.vins {
			vins[r][v] = true
		}
	}
	var out []Cluster
	for r, c := range groups {
		c.Vehicles = len(vins[r])
		if c.Vehicles < minVehicles {
			continue
		}
		c.Lat /= float64(c.Stops)
		c.Lon /= float64(c.Stops)
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Stops > out[j].Stops })
	return out
}

// geoEncode is a test helper.
func geoEncode(lat, lon float64, p int) string { return geo.Encode(lat, lon, p) }
