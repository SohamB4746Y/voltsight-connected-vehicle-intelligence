package embed

import (
	"math"
	"sort"
	"testing"
)

func TestEmbedIsUnitAndDeterministic(t *testing.T) {
	a, b := Embed("charger outage at the depot"), Embed("charger outage at the depot")
	var n float64
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("not deterministic")
		}
		n += float64(a[i]) * float64(a[i])
	}
	if math.Abs(n-1) > 1e-5 || len(a) != Dim {
		t.Fatalf("norm %v dim %d", n, len(a))
	}
	if z := Embed("   "); Cosine(z, z) != 0 {
		t.Fatal("empty text must embed to the zero vector")
	}
}

// T9: recall@5 of leave-one-out paraphrase retrieval over the labelled corpus (every other paraphrase of the
// same situation is relevant; the query itself is excluded).
func TestRecallAt5OnParaphrases(t *testing.T) {
	c := Corpus()
	vecs := make([][]float32, len(c))
	for i, x := range c {
		vecs[i] = Embed(x.Text)
	}
	var hit, total int
	var top1 int
	for q := range c {
		type s struct {
			i int
			d float64
		}
		var ss []s
		for i := range c {
			if i != q {
				ss = append(ss, s{i, Cosine(vecs[q], vecs[i])})
			}
		}
		sort.Slice(ss, func(a, b int) bool { return ss[a].d > ss[b].d })
		rel := 0
		for _, x := range c {
			if x.Group == c[q].Group {
				rel++
			}
		}
		rel-- // the query itself
		got := 0
		for k := 0; k < 5; k++ {
			if c[ss[k].i].Group == c[q].Group {
				got++
			}
		}
		if c[ss[0].i].Group == c[q].Group {
			top1++
		}
		hit += got
		total += rel
	}
	recall := float64(hit) / float64(total)
	t.Logf("MEASURED: recall@5 = %.3f over %d queries (%d relevant); top-1 precision %.3f", recall, len(c), total, float64(top1)/float64(len(c)))
	if recall < 0.9 {
		t.Fatalf("recall@5 %.2f", recall)
	}
}
