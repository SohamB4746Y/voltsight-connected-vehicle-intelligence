// Command vsincidents loads the synthetic resolved-incident corpus into pgvector (per tenant) for the Copilot's
// similar-incident search. Idempotent.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/embed"
)

func main() {
	ctx := context.Background()
	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_batch", dotenv.Get(env, "DB_BATCH_PASSWORD"))
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()
	rs, err := pool.Query(ctx, `SELECT id FROM tenant WHERE name <> 'VoltSight Platform'`)
	must(err)
	var tenants []uuid.UUID
	for rs.Next() {
		var id uuid.UUID
		must(rs.Scan(&id))
		tenants = append(tenants, id)
	}
	rs.Close()
	n := 0
	for _, t := range tenants {
		tx, err := pool.Begin(ctx)
		must(err)
		_, err = tx.Exec(ctx, `DELETE FROM incident_embedding WHERE tenant_id = $1 AND source_ref LIKE 'seed:%'`, t)
		must(err)
		for i, inc := range embed.Corpus() {
			_, err = tx.Exec(ctx, `INSERT INTO incident_embedding (tenant_id, kind, source_ref, body, embedding, meta) VALUES ($1,$2,$3,$4,$5::vector,$6)`,
				t, inc.Kind, fmt.Sprintf("seed:%d", i), inc.Text, embed.Literal(embed.Embed(inc.Text)), fmt.Sprintf(`{"group":%d}`, inc.Group))
			must(err)
			n++
		}
		must(tx.Commit(ctx))
	}
	fmt.Printf("loaded %d incident embeddings for %d tenants\n", n, len(tenants))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsincidents:", err)
		os.Exit(1)
	}
}
