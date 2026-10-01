// Command vser generates docs/er/er.mmd (Mermaid ER diagram) from the migrated database.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/erdiagram"
)

func main() {
	out := "docs/er/er.mmd"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	must(err)
	defer pool.Close()
	src, err := erdiagram.Generate(ctx, pool)
	must(err)
	must(os.WriteFile(out, []byte(src), 0o644))
	readme := filepath.Join(filepath.Dir(out), "README.md")
	must(os.WriteFile(readme, []byte(erdiagram.Markdown(src)), 0o644))
	fmt.Printf("wrote %s and %s (%d tables)\n", out, readme, len(erdiagram.Tables(src)))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vser:", err)
		os.Exit(1)
	}
}
