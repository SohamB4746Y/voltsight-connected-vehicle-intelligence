// Command vsgateway runs the device-facing ingest gateway: mTLS (TLS 1.3) on -listen, plain admin
// endpoints (health, readiness, metrics) on -admin. In local development its server certificate is issued
// by the Vault PKI at start-up.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/ingest"
	"voltsight/internal/kafkautil"
	"voltsight/internal/pki"
)

func main() {
	var (
		listen    = flag.String("listen", "127.0.0.1:8443", "mTLS listener")
		admin     = flag.String("admin", "127.0.0.1:9100", "plain admin listener (/healthz /readyz /metrics)")
		brokers   = flag.String("brokers", "127.0.0.1:29092", "Kafka seed brokers")
		inflight  = flag.Int64("max-inflight-mb", 256, "request bytes in flight before answering 429")
		eps       = flag.Float64("eps-per-connector", 0, "events/s per connector before 429 (0 = unlimited)")
		telemetry = flag.String("telemetry-topic", kafkautil.Telemetry, "telemetry topic")
		dlq       = flag.String("dlq-topic", kafkautil.TelemetryDLQ, "dead-letter topic")
	)
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	env, err := dotenv.Load(".env")
	must(err)
	dsn, err := dbtool.OwnerDSN(env)
	must(err)
	u, err := url.Parse(dsn)
	must(err)
	pw := dotenv.Get(env, "DB_GATEWAY_PASSWORD")
	if pw == "" {
		must(fmt.Errorf("DB_GATEWAY_PASSWORD not set (run `make env && make bootstrap`)"))
	}
	u.User = url.UserPassword("voltsight_gateway", pw) // least-privilege role: read-only directory access
	pool, err := pgxpool.New(ctx, u.String())
	must(err)
	defer pool.Close()

	vaultAddr := dotenv.Get(env, "VAULT_ADDR")
	if vaultAddr == "" {
		vaultAddr = "http://127.0.0.1:8200"
	}
	vault := pki.NewVault(vaultAddr, dotenv.Get(env, "VAULT_DEV_TOKEN"))
	roots, err := vault.RootPool(ctx)
	must(err)
	host, _, _ := net.SplitHostPort(*listen)
	iss, err := vault.IssueServer(ctx, "localhost", []string{"localhost"}, []string{"127.0.0.1", host}, 24*time.Hour)
	must(err)
	serverCert, err := ingest.ParseKeyPair(iss)
	must(err)

	srv, err := ingest.New(ingest.Config{
		Brokers: strings.Split(*brokers, ","), TelemetryTopic: *telemetry, DLQTopic: *dlq,
		MaxInflightBytes: *inflight << 20, PerConnectorEPS: *eps, Logger: logger,
	}, ingest.PGDirectory{Pool: pool}, roots, vault.CRL)
	must(err)
	defer srv.Close()
	must(srv.Start(ctx))

	main := &http.Server{Addr: *listen, Handler: srv.Handler(), TLSConfig: srv.TLSConfig(serverCert),
		ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 1 << 16}
	adm := &http.Server{Addr: *admin, Handler: srv.AdminHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := adm.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("admin listener", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = main.Shutdown(sctx)
		_ = adm.Shutdown(sctx)
	}()
	logger.Info("gateway listening", "mtls", *listen, "admin", *admin, "tls", "1.3 only, client certificate required")
	if err := main.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		must(err)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsgateway:", err)
		os.Exit(1)
	}
}
