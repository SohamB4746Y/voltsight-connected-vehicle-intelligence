package dedup

import (
	"container/heap"
	"math"
	"math/bits"
	"sort"
)

func mix(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

// HashString hashes a string key (FNV-1a followed by a finaliser).
func HashString(s string) uint64 {
	h := uint64(1469598103934665603)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 1099511628211
	}
	return mix(h)
}

// Bloom is a fixed-size Bloom filter using double hashing (Kirsch-Mitzenmacher).
type Bloom struct {
	bits []uint64
	m    uint64
	k    int
	n    int
}

// NewBloom sizes a filter for n expected items at false-positive rate p.
func NewBloom(n int, p float64) *Bloom {
	m := uint64(math.Ceil(-float64(n) * math.Log(p) / (math.Ln2 * math.Ln2)))
	m = (m + 63) &^ 63
	k := int(math.Round(float64(m) / float64(n) * math.Ln2))
	if k < 1 {
		k = 1
	}
	return &Bloom{bits: make([]uint64, m/64), m: m, k: k}
}

// Add inserts a hashed key.
func (b *Bloom) Add(h uint64) {
	h1, h2 := h, mix(h)|1
	for i := 0; i < b.k; i++ {
		pos := (h1 + uint64(i)*h2) % b.m
		b.bits[pos/64] |= 1 << (pos % 64)
	}
	b.n++
}

// Has reports whether the key was possibly added (no false negatives).
func (b *Bloom) Has(h uint64) bool {
	h1, h2 := h, mix(h)|1
	for i := 0; i < b.k; i++ {
		pos := (h1 + uint64(i)*h2) % b.m
		if b.bits[pos/64]>>(pos%64)&1 == 0 {
			return false
		}
	}
	return true
}

// Items returns how many keys were added; Bits and Hashes expose the sizing.
func (b *Bloom) Items() int   { return b.n }
func (b *Bloom) Bits() uint64 { return b.m }
func (b *Bloom) Hashes() int  { return b.k }

// FillRatio is the share of bits set (drives the real false-positive rate).
func (b *Bloom) FillRatio() float64 {
	ones := 0
	for _, w := range b.bits {
		ones += bits.OnesCount64(w)
	}
	return float64(ones) / float64(b.m)
}

// Rotating keeps two generations so memory stays bounded: lookups consult both, additions go to the
// current one, and when the current generation holds its capacity it becomes the previous one.
type Rotating struct {
	cur, prev *Bloom
	capacity  int
	p         float64
}

// NewRotating creates a rotating filter with per-generation capacity n and false-positive rate p.
func NewRotating(n int, p float64) *Rotating {
	return &Rotating{cur: NewBloom(n, p), capacity: n, p: p}
}

// Add records a key, rotating generations when the current one is full.
func (r *Rotating) Add(h uint64) {
	if r.cur.Items() >= r.capacity {
		r.prev, r.cur = r.cur, NewBloom(r.capacity, r.p)
	}
	r.cur.Add(h)
}

// Has reports whether the key is possibly in either generation.
func (r *Rotating) Has(h uint64) bool { return r.cur.Has(h) || (r.prev != nil && r.prev.Has(h)) }

// CountMin is a Count-Min sketch: estimates never fall below the true count and exceed it by at most
// eps*N with probability 1-delta.
type CountMin struct {
	rows  [][]uint32
	width uint64
	total uint64
}

// NewCountMin sizes the sketch for error eps (fraction of the stream) with failure probability delta.
func NewCountMin(eps, delta float64) *CountMin {
	w := uint64(math.Ceil(math.E / eps))
	d := int(math.Ceil(math.Log(1 / delta)))
	cm := &CountMin{width: w, rows: make([][]uint32, d)}
	for i := range cm.rows {
		cm.rows[i] = make([]uint32, w)
	}
	return cm
}

// Add counts one occurrence of the hashed key and returns the new estimate.
func (c *CountMin) Add(h uint64) uint32 {
	c.total++
	est := uint32(math.MaxUint32)
	for i := range c.rows {
		pos := mix(h+uint64(i)*0x9e3779b97f4a7c15) % c.width
		c.rows[i][pos]++
		if v := c.rows[i][pos]; v < est {
			est = v
		}
	}
	return est
}

// Estimate returns the estimated count of the hashed key.
func (c *CountMin) Estimate(h uint64) uint32 {
	est := uint32(math.MaxUint32)
	for i := range c.rows {
		if v := c.rows[i][mix(h+uint64(i)*0x9e3779b97f4a7c15)%c.width]; v < est {
			est = v
		}
	}
	return est
}

// Total is the number of Add calls.
func (c *CountMin) Total() uint64 { return c.total }

// TopK tracks the K heaviest keys of a stream using a Count-Min sketch and a min-heap.
type TopK struct {
	k  int
	cm *CountMin
	h  entryHeap
}

type entry struct {
	key   string
	count uint32
}

type entryHeap struct {
	items []entry
	pos   map[string]int
}

func (h entryHeap) Len() int           { return len(h.items) }
func (h entryHeap) Less(i, j int) bool { return h.items[i].count < h.items[j].count }
func (h entryHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.pos[h.items[i].key], h.pos[h.items[j].key] = i, j
}
func (h *entryHeap) Push(x any) {
	e := x.(entry)
	h.pos[e.key] = len(h.items)
	h.items = append(h.items, e)
}
func (h *entryHeap) Pop() any {
	n := len(h.items)
	e := h.items[n-1]
	h.items = h.items[:n-1]
	delete(h.pos, e.key)
	return e
}

// NewTopK creates a tracker for the k heaviest keys with sketch error eps and failure probability delta.
func NewTopK(k int, eps, delta float64) *TopK {
	t := &TopK{k: k, cm: NewCountMin(eps, delta)}
	t.h = entryHeap{pos: map[string]int{}}
	return t
}

// Add counts one occurrence of key.
func (t *TopK) Add(key string) {
	c := t.cm.Add(HashString(key))
	if i, ok := t.h.pos[key]; ok {
		t.h.items[i].count = c
		heap.Fix(&t.h, i)
		return
	}
	if t.h.Len() < t.k {
		heap.Push(&t.h, entry{key, c})
		return
	}
	if c > t.h.items[0].count {
		heap.Pop(&t.h)
		heap.Push(&t.h, entry{key, c})
	}
}

// Item is a key with its estimated count.
type Item struct {
	Key   string
	Count uint32
}

// Top returns the tracked keys ordered by descending estimated count.
func (t *TopK) Top() []Item {
	out := make([]Item, 0, t.h.Len())
	for _, e := range t.h.items {
		out = append(out, Item{e.key, e.count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
