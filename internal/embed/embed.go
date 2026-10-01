// Package embed turns text into 384-dimensional unit vectors for similarity search.
//
// This is a deterministic feature-hashing embedder (word unigrams and bigrams plus character trigrams, signed
// hashing, L2-normalised). It captures lexical similarity - shared vocabulary and word order - not deep
// semantics. It is dependency-free so the platform and its tests run offline; because the dimensionality
// matches all-MiniLM-L6-v2 (384), a semantic model is a drop-in replacement for Embed without touching the
// database schema or the search code. (Declared limitation; measured recall in evidence/G10.)
package embed

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Dim is the vector size (matches the incident_embedding column).
const Dim = 384

var stop = map[string]bool{"the": true, "a": true, "an": true, "of": true, "to": true, "in": true, "on": true, "and": true, "is": true, "was": true,
	"at": true, "for": true, "with": true, "by": true, "it": true, "that": true, "this": true, "be": true, "as": true, "had": true, "has": true}

func tokens(s string) []string {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := f[:0]
	for _, w := range f {
		if !stop[w] && len(w) > 1 {
			out = append(out, stem(w))
		}
	}
	return out
}

// stem is a crude suffix stripper so that "charging", "charged" and "charger" share a feature.
func stem(w string) string {
	for _, suf := range []string{"ing", "ers", "er", "ed", "es", "s", "ly"} {
		if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

func add(v []float32, feature string, weight float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(feature))
	x := h.Sum64()
	sign := float32(1)
	if x>>63 == 1 {
		sign = -1
	}
	v[x%Dim] += sign * weight
}

// Embed returns the unit-length embedding of text.
func Embed(text string) []float32 {
	v := make([]float32, Dim)
	toks := tokens(text)
	for i, t := range toks {
		add(v, "w:"+t, 1)
		if i+1 < len(toks) {
			add(v, "b:"+t+"_"+toks[i+1], 0.7)
		}
		if len(t) >= 3 {
			for j := 0; j+3 <= len(t); j++ {
				add(v, "c:"+t[j:j+3], 0.25)
			}
		}
	}
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// Cosine similarity of two unit vectors.
func Cosine(a, b []float32) float64 {
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

// Literal formats a vector as a pgvector literal.
func Literal(v []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strings.TrimRight(strings.TrimRight(formatFloat(x), "0"), "."))
	}
	sb.WriteByte(']')
	return sb.String()
}

func formatFloat(x float32) string { return strings.TrimSpace(strconvFormat(x)) }
