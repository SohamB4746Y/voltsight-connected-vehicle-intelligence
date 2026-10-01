// Package dedup holds the streaming primitives of the real-time path: an exact per-vehicle sequence
// window, a rotating Bloom filter and a Count-Min sketch with a top-K heap.
package dedup

import "math/bits"

// WindowBits is how many sequence numbers below the highest one the exact window remembers.
const WindowBits = 256

// Window tracks, for one vehicle, the highest sequence seen and which of the WindowBits sequences below it
// have been seen. Memory is 40 bytes per vehicle; every operation is O(1) (O(WindowBits/64) shifts).
type Window struct {
	High uint64
	Seen [WindowBits / 64]uint64 // bit i set = sequence High-i was seen (bit 0 is High itself)
}

// Verdict is the classification of an incoming sequence number.
type Verdict uint8

const (
	// New: first time this sequence is seen and it is the newest so far.
	New Verdict = iota
	// Late: first time seen but older than the newest (out-of-order delivery within the window).
	Late
	// Duplicate: already seen within the window.
	Duplicate
	// Stale: older than the window can tell; it may or may not be a duplicate.
	Stale
)

func (v Verdict) String() string {
	return [...]string{"new", "late", "duplicate", "stale"}[v]
}

// Observe classifies seq and records it. seq 0 is invalid and reported as Stale without recording.
func (w *Window) Observe(seq uint64) Verdict {
	switch {
	case seq == 0:
		return Stale
	case w.High == 0 || seq > w.High:
		w.shift(seq - w.High)
		w.High = seq
		w.Seen[0] |= 1
		return New
	}
	d := w.High - seq
	if d >= WindowBits {
		return Stale
	}
	word, bit := d/64, d%64
	if w.Seen[word]>>bit&1 == 1 {
		return Duplicate
	}
	w.Seen[word] |= 1 << bit
	return Late
}

// shift moves the bitmap n positions toward older sequences (the new high becomes bit 0).
func (w *Window) shift(n uint64) {
	if w.High == 0 || n >= WindowBits {
		w.Seen = [WindowBits / 64]uint64{}
		return
	}
	words, b := n/64, n%64
	for i := len(w.Seen) - 1; i >= 0; i-- {
		var v uint64
		if src := i - int(words); src >= 0 {
			v = w.Seen[src] << b
			if b > 0 && src-1 >= 0 {
				v |= w.Seen[src-1] >> (64 - b)
			}
		}
		w.Seen[i] = v
	}
}

// Count returns how many distinct sequences the window currently remembers.
func (w *Window) Count() int {
	n := 0
	for _, x := range w.Seen {
		n += bits.OnesCount64(x)
	}
	return n
}

// Marshal encodes the window as 8 + 32 bytes (little endian) for storage in Redis.
func (w *Window) Marshal() []byte {
	b := make([]byte, 8+len(w.Seen)*8)
	put := func(off int, v uint64) {
		for i := 0; i < 8; i++ {
			b[off+i] = byte(v >> (8 * i))
		}
	}
	put(0, w.High)
	for i, s := range w.Seen {
		put(8+8*i, s)
	}
	return b
}

// Unmarshal decodes Marshal's output; it reports false for a wrong length.
func (w *Window) Unmarshal(b []byte) bool {
	if len(b) != 8+len(w.Seen)*8 {
		return false
	}
	get := func(off int) uint64 {
		var v uint64
		for i := 0; i < 8; i++ {
			v |= uint64(b[off+i]) << (8 * i)
		}
		return v
	}
	w.High = get(0)
	for i := range w.Seen {
		w.Seen[i] = get(8 + 8*i)
	}
	return true
}
