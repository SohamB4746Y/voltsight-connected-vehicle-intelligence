//go:build stack

package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/pki"
	"voltsight/internal/seedgen"
)

// The gateway tests run against the real stack of `make up`: Vault (PKI), PostgreSQL (the 100K seed from
// `make seed`) and Kafka.

var brokers = []string{"127.0.0.1:29092"}

type harness struct {
	t                  *testing.T
	env                map[string]string
	vault              *pki.Vault
	owner              *pgxpool.Pool
	gwPool             *pgxpool.Pool
	srv                *Server
	url                string
	roots              *x509.CertPool
	telTopic, dlqTopic string
	world              *seedgen.World
	cfg                Config
	httpSrv            *http.Server
}

func newHarness(t *testing.T, mod func(*Config)) *harness {
	t.Helper()
	ctx := context.Background()
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, env: env}
	addr := dotenv.Get(env, "VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	h.vault = pki.NewVault(addr, dotenv.Get(env, "VAULT_DEV_TOKEN"))
	if err := h.vault.Bootstrap(ctx); err != nil {
		t.Fatalf("vault: %v", err)
	}
	dsn, err := dbtool.OwnerDSN(env)
	if err != nil {
		t.Fatal(err)
	}
	if h.owner, err = pgxpool.New(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.owner.Close)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_gateway", dotenv.Get(env, "DB_GATEWAY_PASSWORD"))
	if h.gwPool, err = pgxpool.New(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.gwPool.Close)
	var n int
	if err := h.owner.QueryRow(ctx, `SELECT count(*) FROM vehicle`).Scan(&n); err != nil || n != 100_000 {
		t.Fatalf("the 100K seed must be loaded (run `make seed`): vehicles=%d err=%v", n, err)
	}
	if h.world, err = seedgen.Generate(seedgen.Config{Seed: 20260925}); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()[:8]
	h.telTopic, h.dlqTopic = "t3."+suffix+".telemetry", "t3."+suffix+".dlq"
	adm := h.admin()
	for _, tp := range []string{h.telTopic, h.dlqTopic} {
		if _, err := adm.CreateTopic(ctx, 8, 1, nil, tp); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = adm.DeleteTopics(context.Background(), h.telTopic, h.dlqTopic) })

	h.cfg = Config{Brokers: brokers, TelemetryTopic: h.telTopic, DLQTopic: h.dlqTopic, RefreshEvery: time.Hour}
	if mod != nil {
		mod(&h.cfg)
	}
	if h.roots, err = h.vault.RootPool(ctx); err != nil {
		t.Fatal(err)
	}
	if h.srv, err = New(h.cfg, PGDirectory{Pool: h.gwPool}, h.roots, h.vault.CRL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.srv.Close)
	if err := h.srv.Start(ctx); err != nil {
		t.Fatal(err)
	}

	srvCert, err := h.vault.IssueServer(ctx, "localhost", []string{"localhost"}, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := ParseKeyPair(srvCert)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", h.srv.TLSConfig(kp))
	if err != nil {
		t.Fatal(err)
	}
	h.httpSrv = &http.Server{Handler: h.srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = h.httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = h.httpSrv.Close() })
	h.url = "https://" + ln.Addr().(*net.TCPAddr).String()
	return h
}

func (h *harness) admin() *kadm.Client {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

// connector issues and registers a certificate for tenant/OEM and refreshes the gateway's directory.
func (h *harness) connector(tenant uuid.UUID, oem string, ttl time.Duration, register bool) (*http.Client, *pki.Issued) {
	h.t.Helper()
	ctx := context.Background()
	id := pki.Identity{TenantID: tenant, OEM: oem}
	iss, err := h.vault.Issue(ctx, id, ttl)
	if err != nil {
		h.t.Fatal(err)
	}
	if register {
		if err := Register(ctx, h.owner, id, iss); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := h.srv.Refresh(ctx); err != nil {
		h.t.Fatal(err)
	}
	cert, err := ParseKeyPair(iss)
	if err != nil {
		h.t.Fatal(err)
	}
	return NewClient(cert, h.roots, "localhost"), iss
}

func (h *harness) tenantVINs(tenant uuid.UUID, n int) []string {
	var out []string
	for _, v := range h.world.Vehicles {
		if v.TenantID == tenant {
			out = append(out, v.VIN)
			if len(out) == n {
				break
			}
		}
	}
	return out
}

func (h *harness) tenants() (a, b uuid.UUID) { return h.world.Tenants[0].ID, h.world.Tenants[1].ID }

func baseEvent(vin string, seq uint64) *telemetryv1.TelemetryEvent {
	return &telemetryv1.TelemetryEvent{Vin: vin, Ts: timestamppb.New(time.Now().Add(-time.Second)), Lat: 13.08, Lon: 80.27,
		SpeedKmh: 30, SocPct: 50, OdoKm: 100, Seq: seq, Oem: "AURORA", SchemaVer: 1}
}

func marshal(t *testing.T, e *telemetryv1.TelemetryEvent) []byte {
	b, err := proto.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(c *http.Client, url string, items [][]byte, jsonLines bool) (*http.Response, []byte, error) {
	body, ctype := encodeBatch(items, jsonLines)
	path := "/ingest/v1/batch"
	if jsonLines {
		path = "/ingest/v1/batch.json"
	}
	req, _ := http.NewRequest(http.MethodPost, url+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", ctype)
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out, nil
}

// consume reads exactly n records (or fails) from the start of a topic.
func (h *harness) consume(topic string, n int, wait time.Duration) []*kgo.Record {
	h.t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		h.t.Fatal(err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var out []*kgo.Record
	for len(out) < n {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			break
		}
		fetches.EachRecord(func(r *kgo.Record) { out = append(out, r) })
	}
	return out
}

func (h *harness) endOffsets(topic string) int64 {
	h.t.Helper()
	offs, err := h.admin().ListEndOffsets(context.Background(), topic)
	if err != nil {
		h.t.Fatal(err)
	}
	var sum int64
	offs.Each(func(o kadm.ListedOffset) { sum += o.Offset })
	return sum
}

func dlqRecords(t *testing.T, recs []*kgo.Record) []*dlqv1.DlqRecord {
	var out []*dlqv1.DlqRecord
	for _, r := range recs {
		var d dlqv1.DlqRecord
		if err := proto.Unmarshal(r.Value, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, &d)
	}
	return out
}
