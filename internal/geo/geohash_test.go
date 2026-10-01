package geo

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestEncodeKnownVectors(t *testing.T) {
	// Reference vectors from the original geohash specification examples.
	cases := []struct {
		lat, lon float64
		p        int
		want     string
	}{
		{57.64911, 10.40744, 11, "u4pruydqqvj"},
		{42.6, -5.6, 5, "ezs42"},
		{-25.382708, -49.265506, 8, "6gkzwgjz"},
	}
	for _, c := range cases {
		if got := Encode(c.lat, c.lon, c.p); got != c.want {
			t.Errorf("Encode(%v,%v,%d)=%s want %s", c.lat, c.lon, c.p, got, c.want)
		}
	}
}

func TestEncodeClampsPrecision(t *testing.T) {
	if len(Encode(10, 10, 0)) != 1 || len(Encode(10, 10, 99)) != 12 {
		t.Fatal("precision not clamped to 1..12")
	}
}

func TestRoundTripWithinCell(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for i := 0; i < 5000; i++ {
		lat := r.Float64()*180 - 90
		lon := r.Float64()*360 - 180
		for _, p := range []int{4, 6, 8, 9} {
			h := Encode(lat, lon, p)
			latLo, latHi, lonLo, lonHi, err := Bounds(h)
			if err != nil {
				t.Fatal(err)
			}
			if lat < latLo || lat > latHi || lon < lonLo || lon > lonHi {
				t.Fatalf("point (%v,%v) outside cell %s", lat, lon, h)
			}
			clat, clon, _ := Decode(h)
			if Encode(clat, clon, p) != h {
				t.Fatalf("centre of %s does not re-encode to itself", h)
			}
		}
	}
}

func TestPrefixProperty(t *testing.T) {
	h12 := Encode(13.0827, 80.2707, 12)
	for p := 1; p < 12; p++ {
		if Encode(13.0827, 80.2707, p) != h12[:p] {
			t.Fatalf("geohash precision %d is not a prefix of precision 12", p)
		}
	}
}

func TestInvalid(t *testing.T) {
	for _, h := range []string{"", "a", "u4pruy!", "ILOa"} {
		if _, _, err := Decode(h); err == nil && h != "a" {
			t.Errorf("Decode(%q) should fail", h)
		}
	}
	if _, _, err := Decode("a"); err == nil {
		t.Error("'a' is not in the geohash alphabet")
	}
}

func TestHaversine(t *testing.T) {
	// Chennai -> Bengaluru is roughly 290 km great-circle.
	d := HaversineKm(13.0827, 80.2707, 12.9716, 77.5946)
	if d < 280 || d > 300 {
		t.Fatalf("Chennai-Bengaluru distance %f km outside [280,300]", d)
	}
	if HaversineKm(10, 10, 10, 10) != 0 {
		t.Fatal("zero distance expected")
	}
	if math.Abs(HaversineKm(0, 0, 0, 180)-math.Pi*earthRadiusKm) > 1e-6 {
		t.Fatal("antipodal distance wrong")
	}
}
