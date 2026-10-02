package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const head = "| ID | Requirement | Source | Implementation | Verification | Evidence | Measured | Target | Gap | Status |\n|---|---|---|---|---|---|---|---|---|---|\n"

func run(t *testing.T, rows string, files ...string) []string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Check(dir, Parse(head+rows))
}

func TestPassNeedsImplementationVerificationAndExistingEvidence(t *testing.T) {
	if v := run(t, "| RQ-01 | r | s | code | go test | `e/a.txt` | m | t | - | PASS |\n", "e/a.txt"); len(v) != 0 {
		t.Fatalf("a complete PASS row was rejected: %v", v)
	}
	for name, row := range map[string]string{
		"no evidence":         "| RQ-01 | r | s | code | go test | | m | t | - | PASS |\n",
		"missing path":        "| RQ-01 | r | s | code | go test | `e/none.txt` | m | t | - | PASS |\n",
		"no verification":     "| RQ-01 | r | s | code | | `e/a.txt` | m | t | - | PASS |\n",
		"no implementation":   "| RQ-01 | r | s | | go test | `e/a.txt` | m | t | - | PASS |\n",
		"bad status":          "| RQ-01 | r | s | code | go test | `e/a.txt` | m | t | - | GREEN |\n",
		"partial without gap": "| RQ-01 | r | s | code | go test | `e/a.txt` | m | t | | PARTIAL |\n",
	} {
		if v := run(t, row, "e/a.txt"); len(v) == 0 {
			t.Errorf("%s: expected a violation", name)
		}
	}
}

func TestEmptyEvidenceAndDuplicatesAreRejected(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "e"), 0o755)
	v := Check(dir, Parse(head+"| RQ-01 | r | s | c | v | `e` | m | t | - | PASS |\n| RQ-01 | r | s | c | v | `e` | m | t | - | PASS |\n"))
	joined := strings.Join(v, "\n")
	if !strings.Contains(joined, "empty") || !strings.Contains(joined, "duplicate") {
		t.Fatalf("empty directory and duplicate ID must both be reported: %v", v)
	}
}

func TestNonPassRowsMayNameEvidenceButItMustExist(t *testing.T) {
	if v := run(t, "| RQ-01 | r | s | c | v | `e/none.txt` | m | t | the gap | PARTIAL |\n"); len(v) == 0 {
		t.Fatal("a named but missing evidence path must be reported even for PARTIAL")
	}
	if v := run(t, "| RQ-01 | r | s | c | - | - | m | t | blocked by credentials | BLOCKED |\n"); len(v) != 0 {
		t.Fatalf("BLOCKED with a gap and no evidence is valid: %v", v)
	}
}
