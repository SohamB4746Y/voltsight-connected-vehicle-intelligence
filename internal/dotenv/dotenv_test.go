package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndGet(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte("# comment\n\nA=1\nB=two=parts\n  C = spaced \nbroken-line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two=parts" || len(m) != 3 {
		t.Fatalf("unexpected parse: %v", m)
	}
	if _, ok := m["C"]; ok {
		t.Fatal("keys are not trimmed around '='; that form is not supported and must not silently half-parse")
	}
	if Get(m, "A") != "1" {
		t.Fatal("Get from map")
	}
	t.Setenv("A", "from-env")
	if Get(m, "A") != "from-env" {
		t.Fatal("process environment must win")
	}
	if Get(m, "MISSING") != "" {
		t.Fatal("missing key should be empty")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	m, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(m) != 0 {
		t.Fatalf("missing file: %v %v", m, err)
	}
}
