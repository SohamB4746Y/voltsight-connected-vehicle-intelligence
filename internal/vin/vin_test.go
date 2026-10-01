package vin

import (
	"math/rand/v2"
	"testing"
)

func TestKnownVINs(t *testing.T) {
	valid := []string{
		"1HGCM82633A004352", // example VIN from the problem statement
		"1M8GDM9AXKP042788", // widely used published example with check digit X
		"11111111111111111",
	}
	for _, v := range valid {
		if err := Check(v); err != nil {
			t.Errorf("%s should be valid: %v", v, err)
		}
	}
}

func TestRejects(t *testing.T) {
	cases := map[string]error{
		"1HGCM82633A00435":   ErrLength,
		"1HGCM82633A0043522": ErrLength,
		"1HGCM82I33A004352":  ErrCharacter, // I
		"1HGCM82O33A004352":  ErrCharacter, // O
		"1HGCM82Q33A004352":  ErrCharacter, // Q
		"1HGCM82633a004352":  ErrCharacter, // lowercase
		"1HGCM82-33A004352":  ErrCharacter,
		"1HGCM82633A004353":  ErrCheckDigit,
		"1HGCM82733A004352":  ErrCheckDigit, // check digit no longer matches
		"":                   ErrLength,
	}
	for v, want := range cases {
		if got := Check(v); got != want {
			t.Errorf("Check(%q) = %v, want %v", v, got, want)
		}
		if Valid(v) {
			t.Errorf("Valid(%q) = true", v)
		}
	}
}

func TestBuildProducesValidAndIsIdempotent(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alpha := Alphabet()
	for i := 0; i < 20000; i++ {
		b := make([]byte, Length)
		for j := range b {
			b[j] = alpha[r.IntN(len(alpha))]
		}
		b[checkPosition] = 'x'
		v, err := Build(string(b))
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !Valid(v) {
			t.Fatalf("Build produced invalid VIN %s", v)
		}
		again, _ := Build(v)
		if again != v {
			t.Fatalf("Build not idempotent: %s -> %s", v, again)
		}
	}
}

// Property: changing any single non-check character to a different valid one breaks the check
// digit for every position with a non-zero weight (ISO 3779 detects all single-character errors there).
func TestSingleCharacterErrorsDetected(t *testing.T) {
	v := "1HGCM82633A004352"
	for i := 0; i < Length; i++ {
		if i == checkPosition {
			continue
		}
		for _, c := range []byte(Alphabet()) {
			if c == v[i] {
				continue
			}
			mutated := []byte(v)
			mutated[i] = c
			// transliteration collisions (e.g. A/J both = 1) are an inherent limit of the scheme
			x, _ := charValue(v[i])
			y, _ := charValue(c)
			if x == y {
				continue
			}
			if Valid(string(mutated)) {
				t.Fatalf("undetected single error at %d: %s", i, mutated)
			}
		}
	}
}

func TestBuildErrors(t *testing.T) {
	if _, err := Build("short"); err != ErrBadTemplate {
		t.Fatalf("want ErrBadTemplate, got %v", err)
	}
	if _, err := Build("1HGCM8x633A00435I"); err != ErrCharacter {
		t.Fatalf("want ErrCharacter, got %v", err)
	}
}
