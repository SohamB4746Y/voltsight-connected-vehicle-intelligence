package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/encoding/protowire"

	"voltsight/internal/kafkautil"
	"voltsight/internal/pki"
	"voltsight/internal/sim"
)

// ClientStats are the sender-side counters.
type ClientStats struct {
	Requests, Sent, Accepted, Rejected atomic.Int64
	Retried429, Retried503, Failed     atomic.Int64
	BytesSent                          atomic.Int64
	ChargerEvents                      atomic.Int64
}

// Sender delivers simulator output to the gateway over mTLS, one HTTP/2 client per connector, retrying on
// 429/503 with the server's Retry-After, and publishes charger status events straight to Kafka (that stream
// comes from the charging-network operator, not from vehicles).
type Sender struct {
	URL       string
	Clients   map[sim.ConnectorKey]*http.Client
	Kafka     *kgo.Client // optional; charger events are dropped when nil
	BatchSize int         // events per request, default 500
	Retries   int         // default 8
	Stats     ClientStats
	Logf      func(format string, args ...any)
}

type connBuf struct {
	client *http.Client
	json   bool
	items  [][]byte
}

// Emit implements sim.Sink.
func (s *Sender) Emit(_ int, _ int, batch []sim.Message) error {
	if s.BatchSize == 0 {
		s.BatchSize = 500
	}
	bufs := map[sim.ConnectorKey]*connBuf{}
	var chargers []*kgo.Record
	for i := range batch {
		m := &batch[i]
		if m.Kind == sim.KindChargerStatus {
			if s.Kafka != nil {
				chargers = append(chargers, &kgo.Record{Topic: kafkautil.ChargerStatus, Key: []byte(m.Key), Value: append([]byte(nil), m.Payload...)})
			}
			continue
		}
		k := sim.ConnectorKey{Tenant: m.Tenant, OEM: m.OEM}
		b := bufs[k]
		if b == nil {
			c := s.Clients[k]
			if c == nil {
				return fmt.Errorf("no client certificate provisioned for connector %s/%s", k.Tenant, k.OEM)
			}
			b = &connBuf{client: c, json: m.Dialect != 0}
			bufs[k] = b
		}
		b.items = append(b.items, m.Payload)
	}
	if len(chargers) > 0 {
		if err := s.Kafka.ProduceSync(context.Background(), chargers...).FirstErr(); err != nil {
			return fmt.Errorf("charger status: %w", err)
		}
		s.Stats.ChargerEvents.Add(int64(len(chargers)))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	sem := make(chan struct{}, 4)
	for _, b := range bufs {
		for lo := 0; lo < len(b.items); lo += s.BatchSize {
			hi := lo + s.BatchSize
			if hi > len(b.items) {
				hi = len(b.items)
			}
			body, ctype := encodeBatch(b.items[lo:hi], b.json)
			wg.Add(1)
			sem <- struct{}{}
			go func(c *http.Client, body []byte, ctype string, n int) {
				defer func() { <-sem; wg.Done() }()
				if err := s.post(c, body, ctype, n); err != nil {
					select {
					case errs <- err:
					default:
					}
				}
			}(b.client, body, ctype, hi-lo)
		}
	}
	wg.Wait()
	close(errs)
	return <-errs
}

func encodeBatch(items [][]byte, jsonLines bool) ([]byte, string) {
	if jsonLines {
		var buf bytes.Buffer
		for _, it := range items {
			buf.Write(it)
			buf.WriteByte('\n')
		}
		return buf.Bytes(), "application/x-ndjson"
	}
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, uuid.NewString())
	for _, it := range items {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendBytes(b, it)
	}
	return b, "application/x-protobuf"
}

func (s *Sender) post(c *http.Client, body []byte, ctype string, n int) error {
	retries := s.Retries
	if retries == 0 {
		retries = 8
	}
	path := "/ingest/v1/batch"
	if ctype == "application/x-ndjson" {
		path = "/ingest/v1/batch.json"
	}
	s.Stats.Sent.Add(int64(n))
	backoff := 100 * time.Millisecond
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, s.URL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", ctype)
		s.Stats.Requests.Add(1)
		resp, err := c.Do(req)
		if err == nil {
			out, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode == http.StatusOK:
				var r response
				if err := json.Unmarshal(out, &r); err != nil {
					return fmt.Errorf("bad gateway response: %w", err)
				}
				s.Stats.Accepted.Add(int64(r.Accepted))
				s.Stats.Rejected.Add(int64(r.Rejected))
				s.Stats.BytesSent.Add(int64(len(body)))
				return nil
			case resp.StatusCode == http.StatusTooManyRequests:
				s.Stats.Retried429.Add(1)
			case resp.StatusCode == http.StatusServiceUnavailable:
				s.Stats.Retried503.Add(1)
			default:
				s.Stats.Failed.Add(int64(n))
				return fmt.Errorf("gateway answered HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(out))
			}
			if ra, _ := strconv.Atoi(resp.Header.Get("Retry-After")); ra > 0 {
				backoff = time.Duration(ra) * time.Second
			}
		} else if attempt >= retries {
			s.Stats.Failed.Add(int64(n))
			return err
		}
		if attempt >= retries {
			s.Stats.Failed.Add(int64(n))
			return errors.New("gave up after repeated 429/503 responses")
		}
		time.Sleep(backoff + time.Duration(attempt)*20*time.Millisecond)
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// NewClient builds an mTLS HTTP/2 client for one connector certificate.
func NewClient(cert tls.Certificate, roots *x509.CertPool, serverName string) *http.Client {
	return &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: serverName},
		ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second,
	}}
}

// ParseKeyPair turns the PEM from Vault into a tls.Certificate.
func ParseKeyPair(i *pki.Issued) (tls.Certificate, error) {
	return tls.X509KeyPair([]byte(i.CertPEM), []byte(i.KeyPEM))
}
