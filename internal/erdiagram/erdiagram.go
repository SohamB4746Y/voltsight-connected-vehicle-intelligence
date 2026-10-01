// Package erdiagram renders the live PostgreSQL schema as a Mermaid entity-relationship diagram, so
// the committed diagram is generated from the migrated DDL and cannot drift from it.
package erdiagram

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type column struct {
	name, typ string
	nullable  bool
	pk, uk    bool
}

type fk struct {
	from, to      string
	cols          []string
	childNullable bool
}

var nonWord = regexp.MustCompile(`[^A-Za-z0-9]+`)

func mermaidType(t string) string {
	switch {
	case strings.HasPrefix(t, "timestamp with time zone"):
		return "timestamptz"
	case strings.HasPrefix(t, "double precision"):
		return "float8"
	}
	t = strings.Split(t, "(")[0] // drop modifiers like numeric(6,2), vector(384)
	return strings.Trim(nonWord.ReplaceAllString(t, "_"), "_")
}

// Generate returns the Mermaid source for the public schema (partition children and the migration
// bookkeeping table are omitted).
func Generate(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), NOT a.attnotnull,
		       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary AND a.attnum = ANY(i.indkey)),
		       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisunique AND NOT i.indisprimary
		               AND i.indnkeyatts = 1 AND a.attnum = i.indkey[0])
		FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r','p') AND NOT c.relispartition
		  AND c.relname <> 'schema_migrations'
		ORDER BY c.relname, a.attnum`)
	if err != nil {
		return "", err
	}
	tables := map[string][]column{}
	for rows.Next() {
		var t string
		var c column
		if err := rows.Scan(&t, &c.name, &c.typ, &c.nullable, &c.pk, &c.uk); err != nil {
			rows.Close()
			return "", err
		}
		tables[t] = append(tables[t], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}

	frows, err := pool.Query(ctx, `
		SELECT c.conrelid::regclass::text, c.confrelid::regclass::text,
		       (SELECT array_agg(a.attname ORDER BY k.ord) FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
		          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum),
		       (SELECT bool_or(NOT a.attnotnull) FROM unnest(c.conkey) k(attnum)
		          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum)
		FROM pg_constraint c
		WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
		  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.conrelid)
		ORDER BY 1, 2, 3`)
	if err != nil {
		return "", err
	}
	var fks []fk
	fkCols := map[string]map[string]bool{}
	for frows.Next() {
		var f fk
		if err := frows.Scan(&f.from, &f.to, &f.cols, &f.childNullable); err != nil {
			frows.Close()
			return "", err
		}
		fks = append(fks, f)
		if fkCols[f.from] == nil {
			fkCols[f.from] = map[string]bool{}
		}
		for _, c := range f.cols {
			fkCols[f.from][c] = true
		}
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return "", err
	}

	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("erDiagram\n")
	for _, f := range fks {
		left := "||"
		if f.childNullable {
			left = "|o"
		}
		fmt.Fprintf(&b, "  %s %s--o{ %s : \"%s\"\n", f.to, left, f.from, strings.Join(f.cols, "+"))
	}
	for _, n := range names {
		fmt.Fprintf(&b, "  %s {\n", n)
		for _, c := range tables[n] {
			var keys []string
			if c.pk {
				keys = append(keys, "PK")
			}
			if fkCols[n][c.name] {
				keys = append(keys, "FK")
			}
			if c.uk && !c.pk {
				keys = append(keys, "UK")
			}
			line := fmt.Sprintf("    %s %s", mermaidType(c.typ), c.name)
			if len(keys) > 0 {
				line += " " + strings.Join(keys, ",")
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("  }\n")
	}
	return b.String(), nil
}

// Markdown wraps the diagram source in a README that GitHub renders natively.
func Markdown(src string) string {
	return "# Entity-relationship diagram (generated)\n\n" +
		"Generated from the migrated PostgreSQL schema by `go run ./cmd/vser`; CI regenerates it and fails on any difference.\n" +
		"Child tables reference their parents with composite `(tenant_id, id)` foreign keys (ADR-006), shown as `tenant_id+...` labels.\n" +
		"See [`docs/3nf.md`](../3nf.md) for the normal-form analysis.\n\n" +
		"```mermaid\n" + src + "```\n"
}

// Tables returns the table names present in a generated diagram's source (for documentation checks).
func Tables(src string) []string {
	var out []string
	for _, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(l, "  ") && strings.HasSuffix(l, " {") && !strings.HasPrefix(l, "    ") {
			out = append(out, strings.TrimSuffix(strings.TrimSpace(l), " {"))
		}
	}
	return out
}
