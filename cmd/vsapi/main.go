// Command vsapi runs the platform API and serves the web console.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"voltsight/internal/api"
	"voltsight/internal/copilot"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/ingest"
	"voltsight/internal/jwtverify"
	"voltsight/internal/pki"
)

func main() {
	var (
		listen  = flag.String("listen", "127.0.0.1:8081", "HTTP listener")
		brokers = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		static  = flag.String("static", "web/dist", "web console build directory ('' to disable)")
		issuer  = flag.String("issuer", "http://localhost:8080/realms/voltsight", "OIDC issuer (exact `iss` claim)")
		jwks    = flag.String("jwks", "http://127.0.0.1:8080/realms/voltsight/protocol/openid-connect/certs", "JWKS URL")
		rps     = flag.Float64("rate", 50, "requests per second per user")
		useTLS  = flag.Bool("tls", false, "serve HTTPS (TLS 1.3 only) with a server certificate issued by the platform CA in Vault")
		llm     = flag.String("copilot", "auto", "copilot provider: auto | stub | anthropic (auto = anthropic when ANTHROPIC_API_KEY is set)")
	)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	env, err := dotenv.Load(".env")
	must(err)

	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, err := url.Parse(dsn)
	must(err)
	u.User = url.UserPassword("voltsight_app", dotenv.Get(env, "DB_APP_PASSWORD")) // RLS applies to this role
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()

	rdb := redis.NewClient(&redis.Options{Addr: dotenv.GetOr(env, "REDIS_ADDR", "127.0.0.1:6379"), Password: dotenv.Get(env, "REDIS_PASSWORD"), PoolSize: 32})
	defer rdb.Close()

	var ch clickhouse.Conn
	if c, err := clickhouse.Open(&clickhouse.Options{Addr: []string{dotenv.GetOr(env, "CLICKHOUSE_ADDR", "127.0.0.1:9000")},
		Auth:        clickhouse.Auth{Database: "default", Username: "voltsight", Password: dotenv.Get(env, "CLICKHOUSE_PASSWORD")},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}, MaxOpenConns: 8}); err == nil {
		ch = c
	} else {
		fmt.Fprintln(os.Stderr, "vsapi: clickhouse unavailable, history disabled:", err)
	}

	seeds := strings.Split(*brokers, ",")
	kc, err := kgo.NewClient(kgo.SeedBrokers(seeds...), kgo.ClientID("vs-api"), kgo.RequiredAcks(kgo.AllISRAcks()))
	must(err)
	defer kc.Close()
	go func() {
		if err := api.MirrorChargerStatus(ctx, seeds, rdb); err != nil {
			fmt.Fprintln(os.Stderr, "vsapi: charger status mirror stopped:", err)
		}
	}()

	_ = os.Setenv("OIDC_PUBLIC_ISSUER", *issuer)
	srv := api.New(api.Config{
		Verifier: jwtverify.New(jwtverify.Config{Issuer: *issuer, Audience: "voltsight-api", JWKSURL: *jwks}),
		Pool:     pool, Redis: rdb, CH: ch, Kafka: kc, RateRPS: *rps, StaticDir: *static,
		Origins: []string{"http://localhost:5173", "http://127.0.0.1:5173", "http://localhost:8081", "http://127.0.0.1:8081"},
	})
	if *llm == "anthropic" || (*llm == "auto" && os.Getenv("ANTHROPIC_API_KEY") != "") {
		model := os.Getenv("COPILOT_MODEL")
		if model == "" {
			model = "claude-sonnet-5-5"
		}
		srv.SetCopilot(copilot.New(srv, &copilot.Anthropic{APIKey: os.Getenv("ANTHROPIC_API_KEY"), Model: model}))
	} else {
		srv.SetCopilot(copilot.New(srv, copilot.Stub{}))
	}
	hs := &http.Server{Addr: *listen, Handler: srv, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = hs.Shutdown(sctx)
	}()
	if *useTLS {
		vault := pki.NewVault(dotenv.GetOr(env, "VAULT_ADDR", "http://127.0.0.1:8200"), dotenv.Get(env, "VAULT_DEV_TOKEN"))
		host, _, _ := net.SplitHostPort(*listen)
		iss, err := vault.IssueServer(ctx, "localhost", []string{"localhost"}, []string{"127.0.0.1", host}, 24*time.Hour)
		must(err)
		cert, err := ingest.ParseKeyPair(iss)
		must(err)
		hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
		fmt.Fprintf(os.Stderr, "vsapi: listening on https://%s (TLS 1.3 only, issuer %s)\n", *listen, *issuer)
		if err := hs.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			must(err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "vsapi: listening on http://%s (issuer %s)\n", *listen, *issuer)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		must(err)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsapi:", err)
		os.Exit(1)
	}
}
