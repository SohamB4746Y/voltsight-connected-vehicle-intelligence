// Command reqtrace is the requirement-traceability gate. It parses the requirement matrix (a Markdown table with the
// columns ID | Requirement | Source | Implementation | Verification | Evidence | Measured | Target | Gap | Status)
// and fails when the matrix claims more than the repository can show:
//
//   - a required field is empty, or the status is not PASS/PARTIAL/BLOCKED/FAIL/NOT_APPLICABLE
//   - a PASS row has no implementation, no verification command, or no evidence
//   - an evidence path (comma or <br> separated, backticks allowed) does not exist, or is an empty directory
//   - a non-PASS row (other than NOT_APPLICABLE) names no gap
//   - an ID appears twice
//
// Usage: go run ./tools/reqtrace [matrix.md]   (paths are relative to the repository root)
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var statuses = map[string]bool{"PASS": true, "PARTIAL": true, "BLOCKED": true, "FAIL": true, "NOT_APPLICABLE": true}

// Row is one parsed requirement.
type Row struct {
	Line                                                                                           int
	ID, Requirement, Source, Implementation, Verification, Evidence, Measured, Target, Gap, Status string
}

var idRe = regexp.MustCompile("^[A-Z]{2,4}-[0-9]{2,3}$")

// Parse returns the rows of the matrix table; header and separator lines are skipped.
func Parse(md string) []Row {
	var rows []Row
	for i, ln := range strings.Split(md, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(ln, "|"), "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if len(cells) != 10 || !idRe.MatchString(cells[0]) {
			continue
		}
		rows = append(rows, Row{Line: i + 1, ID: cells[0], Requirement: cells[1], Source: cells[2], Implementation: cells[3], Verification: cells[4],
			Evidence: cells[5], Measured: cells[6], Target: cells[7], Gap: cells[8], Status: strings.Trim(cells[9], "* `")})
	}
	return rows
}

func evidencePaths(s string) []string {
	s = strings.NewReplacer("<br>", ",", ";", ",", "`", "").Replace(s)
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" && p != "-" {
			out = append(out, p)
		}
	}
	return out
}

func exists(root, p string) error {
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		return fmt.Errorf("evidence path %q does not exist", p)
	}
	if fi.IsDir() {
		es, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(p)))
		if len(es) == 0 {
			return fmt.Errorf("evidence directory %q is empty", p)
		}
	} else if fi.Size() == 0 {
		return fmt.Errorf("evidence file %q is empty", p)
	}
	return nil
}

// Check returns every violation found in rows.
func Check(root string, rows []Row) []string {
	var bad []string
	seen := map[string]int{}
	add := func(r Row, format string, a ...any) {
		bad = append(bad, fmt.Sprintf("line %d %s: %s", r.Line, r.ID, fmt.Sprintf(format, a...)))
	}
	for _, r := range rows {
		if prev, dup := seen[r.ID]; dup {
			add(r, "duplicate ID (first on line %d)", prev)
		}
		seen[r.ID] = r.Line
		if r.Requirement == "" || r.Source == "" {
			add(r, "requirement and source are required")
		}
		if !statuses[r.Status] {
			add(r, "status %q is not one of PASS, PARTIAL, BLOCKED, FAIL, NOT_APPLICABLE", r.Status)
			continue
		}
		if r.Status == "PASS" {
			if r.Implementation == "" || r.Implementation == "-" {
				add(r, "PASS without an implementation")
			}
			if r.Verification == "" || r.Verification == "-" {
				add(r, "PASS without a verification command")
			}
			ev := evidencePaths(r.Evidence)
			if len(ev) == 0 {
				add(r, "PASS without evidence")
			}
			for _, p := range ev {
				if err := exists(root, p); err != nil {
					add(r, "%v", err)
				}
			}
		} else if r.Status != "NOT_APPLICABLE" && (r.Gap == "" || r.Gap == "-") {
			add(r, "%s without a stated gap", r.Status)
		}
		if r.Status != "PASS" {
			for _, p := range evidencePaths(r.Evidence) { // evidence that is named must exist, whatever the status
				if err := exists(root, p); err != nil {
					add(r, "%v", err)
				}
			}
		}
	}
	return bad
}

func main() {
	path := "docs/compliance/FINAL_REQUIREMENT_MATRIX.md"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reqtrace:", err)
		os.Exit(2)
	}
	rows := Parse(string(b))
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "reqtrace: no requirement rows found")
		os.Exit(2)
	}
	count := map[string]int{}
	for _, r := range rows {
		count[r.Status]++
	}
	bad := Check(".", rows)
	fmt.Printf("reqtrace: %d requirements: PASS %d, PARTIAL %d, BLOCKED %d, FAIL %d, NOT_APPLICABLE %d\n", len(rows), count["PASS"], count["PARTIAL"], count["BLOCKED"], count["FAIL"], count["NOT_APPLICABLE"])
	for _, m := range bad {
		fmt.Println("  VIOLATION", m)
	}
	if len(bad) > 0 {
		os.Exit(1)
	}
}
