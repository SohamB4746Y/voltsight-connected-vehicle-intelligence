// Package roadnet builds the synthetic city road graphs used by the simulator and the range-risk
// engine: a jittered street grid with arterials, stored as compressed sparse rows (CSR).
package roadnet

import (
	"math"
)

// Road classes (speed limits in km/h).
const (
	ClassLocal    = 0 // 30
	ClassCollect  = 1 // 50
	ClassArterial = 2 // 70
)

// SpeedKmh returns the speed limit of a road class.
func SpeedKmh(class uint8) float32 {
	switch class {
	case ClassArterial:
		return 70
	case ClassCollect:
		return 50
	}
	return 30
}

// Graph is an undirected road network in CSR form. Node id = row*N + col.
type Graph struct {
	Name       string
	N          int // grid is N x N
	Lat, Lon   []float32
	AdjStart   []int32 // len = nodes+1
	AdjTo      []int32
	AdjLenM    []float32
	AdjClass   []uint8
	lat0, lon0 float64
	dLat, dLon float64
}

// Nodes returns the node count.
func (g *Graph) Nodes() int { return g.N * g.N }

// Edges returns the number of directed edges (twice the undirected count).
func (g *Graph) Edges() int { return len(g.AdjTo) }

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func unit(x uint64) float64 { return float64(x>>11) / (1 << 53) }

// Generate builds an n x n city graph centred on (lat, lon) with the given block spacing in metres.
// The result is a pure function of its arguments.
func Generate(name string, lat, lon float64, n int, spacingM float64, seed uint64) *Graph {
	const mPerDegLat = 111_320.0
	dLat := spacingM / mPerDegLat
	dLon := spacingM / (mPerDegLat * math.Cos(lat*math.Pi/180))
	g := &Graph{Name: name, N: n, lat0: lat - float64(n)/2*dLat, lon0: lon - float64(n)/2*dLon, dLat: dLat, dLon: dLon}
	nodes := n * n
	g.Lat = make([]float32, nodes)
	g.Lon = make([]float32, nodes)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			id := uint64(i*n + j)
			jl := (unit(splitmix(seed^id*2+1)) - 0.5) * 0.3 * dLat // +-15% of a block
			jo := (unit(splitmix(seed^id*2+2)) - 0.5) * 0.3 * dLon
			g.Lat[i*n+j] = float32(g.lat0 + float64(i)*dLat + jl)
			g.Lon[i*n+j] = float32(g.lon0 + float64(j)*dLon + jo)
		}
	}

	class := func(i, j, i2, j2 int) uint8 { // arterial lines every 8th row/col, collectors every 4th
		line := j    // vertical edge (row changes): lies on column j
		if i == i2 { // horizontal edge: lies on row i
			line = i
		}
		switch {
		case line%8 == 0:
			return ClassArterial
		case line%4 == 0:
			return ClassCollect
		}
		return ClassLocal
	}

	g.AdjStart = make([]int32, nodes+1)
	g.AdjTo = make([]int32, 0, nodes*4)
	g.AdjLenM = make([]float32, 0, nodes*4)
	g.AdjClass = make([]uint8, 0, nodes*4)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			id := i*n + j
			g.AdjStart[id] = int32(len(g.AdjTo))
			for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
				i2, j2 := i+d[0], j+d[1]
				if i2 < 0 || j2 < 0 || i2 >= n || j2 >= n {
					continue
				}
				id2 := i2*n + j2
				g.AdjTo = append(g.AdjTo, int32(id2))
				g.AdjLenM = append(g.AdjLenM, float32(distM(float64(g.Lat[id]), float64(g.Lon[id]), float64(g.Lat[id2]), float64(g.Lon[id2]))))
				g.AdjClass = append(g.AdjClass, class(i, j, i2, j2))
			}
		}
	}
	g.AdjStart[nodes] = int32(len(g.AdjTo))
	return g
}

// distM is an equirectangular distance in metres (accurate to well under 0.1% at city scale).
func distM(lat1, lon1, lat2, lon2 float64) float64 {
	const mPerDeg = 111_320.0
	dy := (lat2 - lat1) * mPerDeg
	dx := (lon2 - lon1) * mPerDeg * math.Cos((lat1+lat2)/2*math.Pi/180)
	return math.Hypot(dx, dy)
}

// DistM returns the approximate distance in metres between two coordinates.
func DistM(lat1, lon1, lat2, lon2 float64) float64 { return distM(lat1, lon1, lat2, lon2) }

// NearestNode returns the node closest to (lat, lon) in O(1): the grid cell is computed directly and
// the 3x3 neighbourhood is searched to absorb the node jitter. Points outside the city snap to the border.
func (g *Graph) NearestNode(lat, lon float64) int32 {
	ci := int(math.Round((lat - g.lat0) / g.dLat))
	cj := int(math.Round((lon - g.lon0) / g.dLon))
	best, bestD := int32(0), math.MaxFloat64
	for i := ci - 1; i <= ci+1; i++ {
		for j := cj - 1; j <= cj+1; j++ {
			ii, jj := clamp(i, g.N), clamp(j, g.N)
			id := ii*g.N + jj
			if d := distM(lat, lon, float64(g.Lat[id]), float64(g.Lon[id])); d < bestD {
				best, bestD = int32(id), d
			}
		}
	}
	return best
}

func clamp(v, n int) int {
	if v < 0 {
		return 0
	}
	if v >= n {
		return n - 1
	}
	return v
}

// RC splits a node id into grid row and column.
func (g *Graph) RC(node int32) (row, col int) { return int(node) / g.N, int(node) % g.N }

// ID joins grid row and column into a node id.
func (g *Graph) ID(row, col int) int32 { return int32(row*g.N + col) }

// EdgeBetween returns the length (m) and road class of the edge between two adjacent nodes, or ok=false.
func (g *Graph) EdgeBetween(a, b int32) (lenM float32, class uint8, ok bool) {
	for e := g.AdjStart[a]; e < g.AdjStart[a+1]; e++ {
		if g.AdjTo[e] == b {
			return g.AdjLenM[e], g.AdjClass[e], true
		}
	}
	return 0, 0, false
}
