//go:build stack

package ingest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/kafkautil"
	"voltsight/internal/pki"
	"voltsight/internal/sim"
)

func okEvent(t *testing.T, h *harness, tenant uuid.UUID, seq uint64) [][]byte {
	return [][]byte{marshal(t, baseEvent(h.tenantVINs(tenant, 1)[0], seq))}
}

// G3.3 mTLS: every kind of bad client is stopped at the handshake.
func TestMTLSRejectsEveryBadClient(t *testing.T) {
	h := newHarness(t, nil)
	tenantA, tenantB := h.tenants()

	good, goodCert := h.connector(tenantA, "AURORA", time.Hour, true)
	resp, body, err := post(good, h.url, okEvent(t, h, tenantA, 1), false)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("a registered connector must be accepted: %v %v %s", err, resp, body)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.TLS == nil || resp.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("response must be JSON over TLS 1.3, got %v", resp.TLS)
	}

	mustFail := func(name string, c *http.Client) {
		t.Helper()
		if resp, _, err := post(c, h.url, okEvent(t, h, tenantA, 2), false); err == nil {
			t.Errorf("%s: request was served (HTTP %d)", name, resp.StatusCode)
		}
	}

	// no client certificate at all
	mustFail("no certificate", NewClient(tls.Certificate{}, h.roots, "localhost"))

	// a certificate from an unrelated CA that claims a valid identity
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rogue"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "x"}, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
		URIs: goodCert.Cert.URIs}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	mustFail("unrelated CA", NewClient(tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}, h.roots, "localhost"))

	// valid CA signature but never registered in device_credential
	unreg, _ := h.connector(tenantA, "NIMBUS", time.Hour, false)
	mustFail("unregistered certificate", unreg)

	// registered under a different tenant than the identity it carries
	mismatch, iss := h.connector(tenantA, "ZEPHYR", time.Hour, false)
	if _, err := h.owner.Exec(context.Background(), `INSERT INTO device_credential (tenant_id, oem_code, cert_serial, cert_fingerprint, not_before, not_after)
		VALUES ($1,'ZEPHYR',$2,$3,$4,$5)`, tenantB, iss.Serial, fingerprint(iss.Cert), iss.Cert.NotBefore, iss.Cert.NotAfter); err != nil {
		t.Fatal(err)
	}
	_ = h.srv.Refresh(context.Background())
	mustFail("registration/identity mismatch", mismatch)

	// revoked in Vault only: the CRL check must catch it
	crlOnly, crlIss := h.connector(tenantA, "ORION", time.Hour, true)
	if resp, _, err := post(crlOnly, h.url, okEvent(t, h, tenantA, 3), false); err != nil || resp.StatusCode != 200 {
		t.Fatalf("before revocation: %v", err)
	}
	if err := h.vault.Revoke(context.Background(), crlIss.Serial); err != nil {
		t.Fatal(err)
	}
	_ = h.srv.Refresh(context.Background())
	crlOnly.CloseIdleConnections()
	mustFail("revoked in the CRL", NewClient(mustPair(t, crlIss), h.roots, "localhost"))

	// revoked in the database only
	dbOnly, dbIss := h.connector(tenantB, "AURORA", time.Hour, true)
	if err := MarkRevoked(context.Background(), h.owner, dbIss.Serial); err != nil {
		t.Fatal(err)
	}
	_ = h.srv.Refresh(context.Background())
	mustFail("revoked in device_credential", NewClient(mustPair(t, dbIss), h.roots, "localhost"))
	dbOnly.CloseIdleConnections()

	// expired
	exp, expIss := h.connector(tenantA, "AURORA", 3*time.Second, true)
	if resp, _, err := post(exp, h.url, okEvent(t, h, tenantA, 4), false); err != nil || resp.StatusCode != 200 {
		t.Fatalf("a short-lived certificate must work until it expires: %v", err)
	}
	time.Sleep(time.Until(expIss.Cert.NotAfter) + 1500*time.Millisecond)
	mustFail("expired certificate", NewClient(mustPair(t, expIss), h.roots, "localhost"))

	// TLS 1.2 is refused even with a perfectly good certificate
	c12 := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MaxVersion: tls.VersionTLS12, Certificates: []tls.Certificate{mustPair(t, goodCert)}, RootCAs: h.roots, ServerName: "localhost"}}}
	mustFail("TLS 1.2", c12)

	// the good connector still works after all of that, and the rejections were counted
	if resp, _, err := post(good, h.url, okEvent(t, h, tenantA, 5), false); err != nil || resp.StatusCode != 200 {
		t.Fatalf("the valid connector broke: %v", err)
	}
}

func mustPair(t *testing.T, i *pki.Issued) tls.Certificate {
	c, err := ParseKeyPair(i)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// G3.4 + G3.5: the tenant comes from the certificate; failures are isolated and classified.
func TestTenantFromCertificateAndPerEventIsolation(t *testing.T) {
	h := newHarness(t, nil)
	tenantA, tenantB := h.tenants()
	client, _ := h.connector(tenantA, "AURORA", time.Hour, true)
	own := h.tenantVINs(tenantA, 3)
	foreign := h.tenantVINs(tenantB, 1)[0]

	forged := baseEvent(own[1], 2)
	forged.TenantId = tenantB.String() // a device lying about its tenant
	forged.RecvTs = timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC))
	badVIN := baseEvent("1HGCM82633A004353", 4) // wrong check digit
	noSchema := baseEvent(own[2], 5)
	noSchema.SchemaVer = 0
	tooOld := baseEvent(own[2], 6)
	tooOld.Ts = timestamppb.New(time.Now().Add(-30 * 24 * time.Hour))
	corrupt := []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f, 0x01}

	items := [][]byte{
		marshal(t, baseEvent(own[0], 1)), marshal(t, forged), marshal(t, baseEvent(foreign, 3)),
		marshal(t, badVIN), marshal(t, noSchema), marshal(t, tooOld), corrupt,
	}
	before := time.Now()
	resp, body, err := post(client, h.url, items, false)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("batch with bad events must still be a 200: %v %v %s", err, resp, body)
	}
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.Accepted != 2 || r.Rejected != 5 {
		t.Fatalf("accepted/rejected = %d/%d, want 2/5 (%s)", r.Accepted, r.Rejected, body)
	}
	for class, want := range map[string]int{"ERROR_CLASS_UNAUTHORIZED_VIN": 1, "ERROR_CLASS_VIN_INVALID": 1, "ERROR_CLASS_SCHEMA_INVALID": 1,
		"ERROR_CLASS_TOO_LATE": 1, "ERROR_CLASS_MALFORMED": 1} {
		if r.RejectedByClass[class] != want {
			t.Errorf("class %s: %d, want %d (%v)", class, r.RejectedByClass[class], want, r.RejectedByClass)
		}
	}

	tel := h.consume(h.telTopic, 2, 20*time.Second)
	if len(tel) != 2 {
		t.Fatalf("telemetry topic holds %d records, want exactly the 2 valid events", len(tel))
	}
	for _, rec := range tel {
		var e telemetryv1.TelemetryEvent
		if err := proto.Unmarshal(rec.Value, &e); err != nil {
			t.Fatal(err)
		}
		if e.TenantId != tenantA.String() || e.Oem != "AURORA" {
			t.Errorf("tenant/oem must come from the certificate, got %s/%s", e.TenantId, e.Oem)
		}
		if e.RecvTs == nil || e.RecvTs.AsTime().Before(before.Add(-time.Second)) {
			t.Errorf("recv_ts must be set by the gateway, got %v", e.RecvTs)
		}
		if string(rec.Key) != e.Vin {
			t.Errorf("record key %q must be the VIN %q", rec.Key, e.Vin)
		}
	}
	dlq := dlqRecords(t, h.consume(h.dlqTopic, 5, 20*time.Second))
	if len(dlq) != 5 {
		t.Fatalf("DLQ holds %d records, want 5", len(dlq))
	}
	got := map[dlqv1.ErrorClass]int{}
	for _, d := range dlq {
		got[d.ErrorClass]++
		if len(d.Original) == 0 || d.TenantId != tenantA.String() || d.FailedAt == nil || d.ErrorDetail == "" {
			t.Errorf("DLQ record lacks the original bytes / tenant / detail: %+v", d)
		}
	}
	if got[dlqv1.ErrorClass_ERROR_CLASS_UNAUTHORIZED_VIN] != 1 || got[dlqv1.ErrorClass_ERROR_CLASS_MALFORMED] != 1 {
		t.Errorf("DLQ classes: %v", got)
	}

	// a body that is not a protobuf batch at all is a 400 and lands in the DLQ
	resp, _, err = post(client, h.url, nil, false)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("empty batch should be a harmless 200: %v", err)
	}
	garbage, _ := http.NewRequest(http.MethodPost, h.url+"/ingest/v1/batch", bytes.NewReader([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x7f}))
	garbage.Header.Set("Content-Type", "application/x-protobuf")
	gr, err := client.Do(garbage)
	if err != nil {
		t.Fatal(err)
	}
	gr.Body.Close()
	if gr.StatusCode != 400 {
		t.Fatalf("a broken envelope must be a 400, got %d", gr.StatusCode)
	}
}

// G3.8 the same VIN always lands on the same partition.
func TestSameVINSamePartition(t *testing.T) {
	h := newHarness(t, nil)
	tenantA, _ := h.tenants()
	client, _ := h.connector(tenantA, "AURORA", time.Hour, true)
	vins := h.tenantVINs(tenantA, 20)
	var items [][]byte
	for round := 0; round < 5; round++ {
		for i, v := range vins {
			items = append(items, marshal(t, baseEvent(v, uint64(round*100+i+1))))
		}
	}
	if resp, _, err := post(client, h.url, items, false); err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v", err)
	}
	recs := h.consume(h.telTopic, len(items), 30*time.Second)
	if len(recs) != len(items) {
		t.Fatalf("got %d of %d records", len(recs), len(items))
	}
	part := map[string]int32{}
	parts := map[int32]bool{}
	for _, r := range recs {
		if p, ok := part[string(r.Key)]; ok && p != r.Partition {
			t.Fatalf("VIN %s appeared on partitions %d and %d", r.Key, p, r.Partition)
		}
		part[string(r.Key)] = r.Partition
		parts[r.Partition] = true
	}
	if len(parts) < 3 {
		t.Fatalf("20 VINs only used %d partitions: the key is not spreading load", len(parts))
	}
}

// G3.8 the designed topics exist with their configuration (created by `make up`).
func TestDesignedTopicsExistWithTheirConfiguration(t *testing.T) {
	h := newHarness(t, nil)
	adm := h.admin()
	ctx := context.Background()
	details, err := adm.ListTopics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfgs, err := adm.DescribeTopicConfigs(ctx, kafkautil.Telemetry, kafkautil.ChargerStatus, kafkautil.TelemetryDLQ)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range kafkautil.Specs() {
		d, ok := details[s.Name]
		if !ok {
			t.Fatalf("topic %s missing: run `make up` (or `go run ./cmd/vstopics`)", s.Name)
		}
		if len(d.Partitions) != int(s.Partitions) {
			t.Errorf("%s has %d partitions, want %d", s.Name, len(d.Partitions), s.Partitions)
		}
	}
	for _, rc := range cfgs {
		get := func(k string) string {
			for _, c := range rc.Configs {
				if c.Key == k && c.Value != nil {
					return *c.Value
				}
			}
			return ""
		}
		switch rc.Name {
		case kafkautil.ChargerStatus:
			if get("cleanup.policy") != "compact" {
				t.Errorf("charger status must be compacted, got %q", get("cleanup.policy"))
			}
		case kafkautil.Telemetry:
			if get("retention.ms") != "259200000" {
				t.Errorf("telemetry retention is %s ms, want 72 h", get("retention.ms"))
			}
		case kafkautil.TelemetryDLQ:
			if get("retention.ms") != "1209600000" {
				t.Errorf("DLQ retention is %s ms, want 14 d", get("retention.ms"))
			}
		}
	}
}

// G3.7 back-pressure: saturation answers 429 + Retry-After before anything is produced; retrying loses nothing.
func TestBackPressureAnswers429AndRetriesLoseNothing(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxInflightBytes = 150_000 })
	tenantA, _ := h.tenants()
	client, _ := h.connector(tenantA, "AURORA", time.Hour, true)
	vins := h.tenantVINs(tenantA, 800)
	mkBatch := func(worker int) [][]byte {
		var items [][]byte
		for i, v := range vins {
			items = append(items, marshal(t, baseEvent(v, uint64(worker*1000+i+1))))
		}
		return items
	}
	const workers = 10
	var saw429, sawRetryAfter atomic.Int64
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			items := mkBatch(w)
			for attempt := 0; attempt < 200; attempt++ {
				resp, body, err := post(client, h.url, items, false)
				if err != nil {
					t.Errorf("worker %d: %v", w, err)
					return
				}
				if resp.StatusCode == http.StatusTooManyRequests {
					saw429.Add(1)
					if resp.Header.Get("Retry-After") == "1" {
						sawRetryAfter.Add(1)
					}
					time.Sleep(20 * time.Millisecond)
					continue
				}
				var r response
				_ = json.Unmarshal(body, &r)
				if resp.StatusCode != 200 || r.Accepted != len(items) {
					t.Errorf("worker %d: HTTP %d %s", w, resp.StatusCode, body)
				}
				accepted.Add(int64(r.Accepted))
				return
			}
			t.Errorf("worker %d never got through", w)
		}(w)
	}
	wg.Wait()
	if saw429.Load() == 0 || sawRetryAfter.Load() != saw429.Load() {
		t.Fatalf("saturation produced %d 429s of which %d carried Retry-After; expected some, all with the header", saw429.Load(), sawRetryAfter.Load())
	}
	want := int64(workers * len(vins))
	if accepted.Load() != want {
		t.Fatalf("accepted %d of %d after retries", accepted.Load(), want)
	}
	// nothing was produced for the refused attempts: the topic holds exactly the accepted events
	time.Sleep(500 * time.Millisecond)
	if got := h.endOffsets(h.telTopic); got != want {
		t.Fatalf("topic holds %d records, want exactly %d (a refused request must not produce)", got, want)
	}
	t.Logf("MEASURED: %d refusals (429) across %d workers before every batch was accepted", saw429.Load(), workers)
}

func TestPerConnectorRateLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.PerConnectorEPS, c.PerConnectorBurst = 50, 100 })
	tenantA, _ := h.tenants()
	client, _ := h.connector(tenantA, "AURORA", time.Hour, true)
	vins := h.tenantVINs(tenantA, 300)
	var items [][]byte
	for i, v := range vins {
		items = append(items, marshal(t, baseEvent(v, uint64(i+1))))
	}
	resp, _, err := post(client, h.url, items, false)
	if err != nil || resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("300 events against a 100-event burst must be refused with 429: %v %v", err, resp)
	}
	if got := h.endOffsets(h.telTopic); got != 0 {
		t.Fatalf("a rate-limited request produced %d records", got)
	}
	if resp, _, err := post(client, h.url, items[:50], false); err != nil || resp.StatusCode != 200 {
		t.Fatalf("a batch within the burst must pass: %v %v", err, resp)
	}
}

// G3.6 end to end: simulator -> mTLS gateway -> Kafka, with duplicates, out-of-order, malformed and a
// mid-run schema rollout. Nothing may be silently lost and the schema rollout must cause zero rejects.
func TestSimulatorThroughGatewayToKafka(t *testing.T) {
	for _, format := range []sim.Format{sim.FormatOEMJSON, sim.FormatProto} {
		t.Run(string(format), func(t *testing.T) {
			h := newHarness(t, nil)
			cfg := sim.Config{Seed: 7, Vehicles: 100_000, Duration: 8, Format: format, StartTOD: 27000, DupRate: 0.02, OOORate: 0.03,
				MalformedRate: 0.005, SchemaV2At: 4, BurstAt: 5, BurstDuration: 2, BurstMult: 2, TimeBase: time.Now()}
			keys, err := sim.Connectors(cfg)
			if err != nil {
				t.Fatal(err)
			}
			certs, err := Provision(context.Background(), h.vault, h.owner, keys, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.srv.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			sender := &Sender{URL: h.url, Clients: map[sim.ConnectorKey]*http.Client{}}
			for k, c := range certs {
				sender.Clients[k] = NewClient(c, h.roots, "localhost")
			}
			var stats sim.Stats
			start := time.Now()
			if err := sim.Run(context.Background(), cfg, sender, &stats); err != nil {
				t.Fatal(err)
			}
			wall := time.Since(start)

			sent, acc, rej := sender.Stats.Sent.Load(), sender.Stats.Accepted.Load(), sender.Stats.Rejected.Load()
			if sender.Stats.Failed.Load() != 0 {
				t.Fatalf("%d events failed permanently", sender.Stats.Failed.Load())
			}
			if acc+rej != sent {
				t.Fatalf("accepted %d + rejected %d != sent %d: events vanished", acc, rej, sent)
			}
			snap := stats.Snapshot()
			if sent != snap.Emitted {
				t.Fatalf("sender saw %d events but the simulator emitted %d", sent, snap.Emitted)
			}
			time.Sleep(500 * time.Millisecond)
			if got := h.endOffsets(h.telTopic); got != acc {
				t.Fatalf("Kafka telemetry holds %d records, gateway said it accepted %d", got, acc)
			}
			if got := h.endOffsets(h.dlqTopic); got != rej {
				t.Fatalf("DLQ holds %d records, gateway said it rejected %d", got, rej)
			}
			// rejects are the corrupted payloads (and their duplicates), nothing else
			mal := float64(snap.Malformed)
			if float64(rej) < mal*0.95 || float64(rej) > mal*1.15 {
				t.Fatalf("rejected %d but %v payloads were corrupted", rej, mal)
			}
			// duplicates and out-of-order events are passed through, not dropped
			if snap.Duplicates == 0 || snap.OutOfOrder == 0 {
				t.Fatal("scenario produced no duplicates / out-of-order events")
			}
			dlq := dlqRecords(t, h.consume(h.dlqTopic, int(rej), 60*time.Second))
			classes := map[dlqv1.ErrorClass]int{}
			for _, d := range dlq {
				classes[d.ErrorClass]++
			}
			if format == sim.FormatOEMJSON && classes[dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID] != 0 {
				t.Fatalf("OEM schema v2 rollout produced SCHEMA_INVALID rejects: %v (the rollout must be seamless)", classes)
			}
			if sender.Stats.Retried429.Load() > 0 {
				t.Logf("sender was throttled %d times", sender.Stats.Retried429.Load())
			}
			t.Logf("MEASURED (%s): %d events via mTLS gateway in %.1fs = %.0f events/s end to end; accepted=%d rejected=%d (DLQ %v) dup=%d ooo=%d",
				format, sent, wall.Seconds(), float64(sent)/wall.Seconds(), acc, rej, classes, snap.Duplicates, snap.OutOfOrder)
		})
	}
}
