//go:build integration

package dbtests

import (
	"context"
	"os"
	"regexp"
	"testing"

	"voltsight/internal/erdiagram"
)

// G1.10: the committed ER diagram is exactly what the migrated schema renders to.
func TestERDiagramIsGeneratedFromTheLiveSchema(t *testing.T) {
	src, err := erdiagram.Generate(context.Background(), ownerPool)
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"../../docs/er/er.mmd":    src,
		"../../docs/er/README.md": erdiagram.Markdown(src),
	} {
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s missing; run `go run ./cmd/vser`: %v", file, err)
		}
		if string(got) != want {
			t.Errorf("%s is stale; regenerate with `go run ./cmd/vser`", file)
		}
	}
	if n := len(erdiagram.Tables(src)); n != len(tablesWithoutPartitions(t)) {
		t.Errorf("diagram has %d tables, database has %d", n, len(tablesWithoutPartitions(t)))
	}
}

// G1.10: docs/3nf.md has a normal-form section for every table and no section for a table that no longer exists.
func TestNormalFormDocumentCoversEveryTable(t *testing.T) {
	raw, err := os.ReadFile("../../docs/3nf.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^### `([a-z_]+)`$").FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]] = true
	}
	actual := map[string]bool{}
	for _, tbl := range tablesWithoutPartitions(t) {
		actual[tbl] = true
		if !documented[tbl] {
			t.Errorf("table %s has no section in docs/3nf.md", tbl)
		}
	}
	for tbl := range documented {
		if !actual[tbl] {
			t.Errorf("docs/3nf.md documents %s, which is not in the schema", tbl)
		}
	}
	if len(documented) < 30 {
		t.Errorf("only %d tables documented", len(documented))
	}
}
