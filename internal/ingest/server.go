package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	dlqv1 "voltsight/gen/voltsight/dlq/v1"
	telemetryv1 "voltsight/gen/voltsight/telemetry/v1"
	"voltsight/internal/normalise"
	"voltsight/internal/pki"
)

// Config configures the gateway.
type Config struct {
	Brokers        []string
	TelemetryTopic string
	DLQTopic       string

	MaxBodyBytes      int64   // per request, default 8 MiB
	MaxInflightBytes  int64   // request bytes being processed at once before 429, default 256 MiB
	PerConnectorEPS   float64 // events/s allowed per connector before 429; 0 = unlimited
	PerConnectorBurst int
	Limits            normalise.Limits
	RefreshEvery      time.Duration // directory and CRL refresh, default 10 s
	ProduceTimeout    time.Duration // default 20 s
	Now               func() time.Time
	Logger            *slog.Logger
}

func (c *Config) defaults() {
	if c.MaxBodyBytes == 0 {
		c.MaxBodyBytes = 8 << 20
	}
	if c.MaxInflightBytes == 0 {
		c.MaxInflightBytes = 256 << 20
	}
	if c.RefreshEvery == 0 {
		c.RefreshEvery = 10 * time.Second
	}
	if c.ProduceTimeout == 0 {
		c.ProduceTimeout = 20 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.PerConnectorBurst == 0 {
		c.PerConnectorBurst = 100_000
	}
}

// CRLSource fetches the current certificate revocation list.
type CRLSource func(ctx context.Context) (*x509.RevocationList, error)

type metrics struct {
	reg        *prometheus.Registry
	events     *prometheus.CounterVec // result: accepted | rejected
	rejected   *prometheus.CounterVec // class
	requests   *prometheus.CounterVec // code
	inflight   prometheus.Gauge
	duration   prometheus.Histogram
	tlsReject  prometheus.Counter
	produceErr prometheus.Counter
}

func newMetrics() *metrics {
	m := &metrics{reg: prometheus.NewRegistry()}
	m.events = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_events_total", Help: "Events by outcome."}, []string{"result"})
	m.rejected = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_events_rejected_total", Help: "Rejected events by DLQ class."}, []string{"class"})
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gateway_requests_total", Help: "Requests by HTTP status."}, []string{"code"})
	m.inflight = prometheus.NewGauge(prometheus.GaugeOpts{Name: "gateway_inflight_bytes", Help: "Request bytes in flight."})
	m.duration = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gateway_request_seconds", Help: "Request latency.",
		Buckets: []float64{.001, .002, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}})
	m.tlsReject = prometheus.NewCounter(prometheus.CounterOpts{Name: "gateway_tls_rejected_total", Help: "Handshakes rejected after chain verification."})
	m.produceErr = prometheus.NewCounter(prometheus.CounterOpts{Name: "gateway_produce_errors_total", Help: "Kafka produce failures."})
	m.reg.MustRegister(m.events, m.rejected, m.requests, m.inflight, m.duration, m.tlsReject, m.produceErr)
	return m
}

// Server is the ingest gateway.
type Server struct {
	cfg   Config
	dir   Directory
	crlFn CRLSource
	roots *x509.CertPool
	cl    *kgo.Client
	m     *metrics

	vehicles atomic.Pointer[map[string]uuid.UUID]
	creds    atomic.Pointer[map[string]Credential]
	dialects atomic.Pointer[map[string]byte]
	crl      atomic.Pointer[x509.RevocationList]
	ready    atomic.Bool

	inflight atomic.Int64
	limiters sync.Map // connector key -> *rate.Limiter
}

// New creates the gateway and its Kafka producer (idempotent, acks=all, lz4).
func New(cfg Config, dir Directory, roots *x509.CertPool, crl CRLSource) (*Server, error) {
	cfg.defaults()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...), kgo.ClientID("vs-gateway"),
		kgo.RequiredAcks(kgo.AllISRAcks()), // idempotent producer is the franz-go default with acks=all
		kgo.ProducerBatchCompression(kgo.Lz4Compression()),
		kgo.ProducerLinger(5*time.Millisecond), kgo.ProducerBatchMaxBytes(1<<20),
		kgo.MaxBufferedRecords(500_000), kgo.RecordDeliveryTimeout(cfg.ProduceTimeout),
	)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, dir: dir, crlFn: crl, roots: roots, cl: cl, m: newMetrics()}, nil
}

// Refresh reloads the directory and the CRL. It is called at start and every RefreshEvery.
func (s *Server) Refresh(ctx context.Context) error {
	v, err := s.dir.Vehicles(ctx)
	if err != nil {
		return err
	}
	c, err := s.dir.Credentials(ctx)
	if err != nil {
		return err
	}
	d, err := s.dir.Dialects(ctx)
	if err != nil {
		return err
	}
	s.vehicles.Store(&v)
	s.creds.Store(&c)
	s.dialects.Store(&d)
	if s.crlFn != nil {
		crl, err := s.crlFn(ctx)
		if err != nil {
			return fmt.Errorf("crl: %w", err)
		}
		s.crl.Store(crl)
	}
	s.ready.Store(true)
	return nil
}

// Start performs the first load (fail fast) and runs the periodic refresh until ctx ends.
func (s *Server) Start(ctx context.Context) error {
	if err := s.Refresh(ctx); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(s.cfg.RefreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.Refresh(ctx); err != nil {
					s.cfg.Logger.Error("directory refresh failed; keeping the previous snapshot", "err", err)
				}
			}
		}
	}()
	return nil
}

// Close flushes and closes the Kafka producer.
func (s *Server) Close() { s.cl.Close() }

// TLSConfig requires a verified client certificate (chain to the connector CA, client-auth usage),
// TLS 1.3 only, and then checks that the certificate is a registered, active, unrevoked credential whose
// registration matches the identity it carries.
func (s *Server) TLSConfig(serverCert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: s.roots, NextProtos: []string{"h2", "http/1.1"},
		VerifyConnection: func(cs tls.ConnectionState) error {
			if err := s.checkPeer(cs.PeerCertificates); err != nil {
				s.m.tlsReject.Inc()
				return err
			}
			return nil
		},
	}
}

func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) checkPeer(chain []*x509.Certificate) error {
	if len(chain) == 0 {
		return errors.New("no client certificate")
	}
	cert := chain[0]
	id, err := pki.ParseIdentity(cert)
	if err != nil {
		return err
	}
	creds := s.creds.Load()
	if creds == nil {
		return errors.New("gateway not ready")
	}
	cred, ok := (*creds)[fingerprint(cert)]
	if !ok || !cred.Active {
		return errors.New("certificate is not a registered active connector credential")
	}
	if cred.Tenant != id.TenantID || cred.OEM != id.OEM {
		return errors.New("certificate identity does not match its registration")
	}
	if crl := s.crl.Load(); crl != nil && pki.IsRevoked(crl, cert.SerialNumber) {
		return pki.ErrRevoked
	}
	return nil
}

// Handler serves the device API (behind the mTLS listener).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest/v1/batch", func(w http.ResponseWriter, r *http.Request) { s.handle(w, r, false) })
	mux.HandleFunc("POST /ingest/v1/batch.json", func(w http.ResponseWriter, r *http.Request) { s.handle(w, r, true) })
	return mux
}

// AdminHandler serves health, readiness and metrics on a separate plain listener.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if !s.ready.Load() || s.cl.Ping(ctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(s.m.reg, promhttp.HandlerOpts{}))
	return mux
}

type response struct {
	Accepted        int            `json:"accepted"`
	Rejected        int            `json:"rejected"`
	RejectedByClass map[string]int `json:"rejected_by_class,omitempty"`
}

func (s *Server) reply(w http.ResponseWriter, code int, body any) {
	s.m.requests.WithLabelValues(strconv.Itoa(code)).Inc()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) tooMany(w http.ResponseWriter, why string) {
	w.Header().Set("Retry-After", "1")
	s.reply(w, http.StatusTooManyRequests, map[string]string{"error": why})
}

func (s *Server) limiter(key string) *rate.Limiter {
	if s.cfg.PerConnectorEPS <= 0 {
		return nil
	}
	if l, ok := s.limiters.Load(key); ok {
		return l.(*rate.Limiter)
	}
	l, _ := s.limiters.LoadOrStore(key, rate.NewLimiter(rate.Limit(s.cfg.PerConnectorEPS), s.cfg.PerConnectorBurst))
	return l.(*rate.Limiter)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, jsonLines bool) {
	start := time.Now()
	defer func() { s.m.duration.Observe(time.Since(start).Seconds()) }()

	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		s.reply(w, http.StatusUnauthorized, map[string]string{"error": "client certificate required"})
		return
	}
	id, err := pki.ParseIdentity(r.TLS.PeerCertificates[0])
	if err != nil {
		s.reply(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	// back-pressure: refuse before reading when the in-flight byte budget is spent
	size := r.ContentLength
	if size <= 0 || size > s.cfg.MaxBodyBytes {
		size = s.cfg.MaxBodyBytes
	}
	if s.inflight.Add(size) > s.cfg.MaxInflightBytes {
		s.inflight.Add(-size)
		s.tooMany(w, "gateway saturated")
		return
	}
	s.m.inflight.Set(float64(s.inflight.Load()))
	defer func() { s.m.inflight.Set(float64(s.inflight.Add(-size))) }()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.reply(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large"})
			return
		}
		s.reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	var items [][]byte
	if jsonLines {
		items = splitLines(body)
	} else if items, err = splitBatch(body); err != nil {
		s.dlqBatch(r.Context(), id, dlqv1.ErrorClass_ERROR_CLASS_MALFORMED, "batch envelope: "+err.Error(), body)
		s.reply(w, http.StatusBadRequest, map[string]string{"error": "malformed batch"})
		return
	}
	if lim := s.limiter(id.TenantID.String() + "/" + id.OEM); lim != nil && len(items) > 0 && !lim.AllowN(time.Now(), len(items)) {
		s.tooMany(w, "connector rate limit")
		return
	}

	vehicles, dialects := s.vehicles.Load(), s.dialects.Load()
	if vehicles == nil || dialects == nil {
		s.reply(w, http.StatusServiceUnavailable, map[string]string{"error": "gateway not ready"})
		return
	}
	dialect := (*dialects)[id.OEM]
	now := s.cfg.Now()
	res := response{RejectedByClass: map[string]int{}}
	var recs []*kgo.Record
	for _, raw := range items {
		ev, perr := s.decode(raw, jsonLines, dialect)
		if perr == nil {
			ev.TenantId, ev.Oem, ev.RecvTs = id.TenantID.String(), id.OEM, timestamppb.New(now)
			perr = normalise.Validate(ev, now, s.cfg.Limits)
		}
		if perr == nil {
			if owner, ok := (*vehicles)[ev.Vin]; !ok || owner != id.TenantID {
				perr = &normalise.Error{Class: dlqv1.ErrorClass_ERROR_CLASS_UNAUTHORIZED_VIN,
					Msg: "vehicle does not belong to the connector's tenant"}
			}
		}
		if perr != nil {
			vin, schema := "", uint32(0)
			if ev != nil {
				vin, schema = ev.Vin, ev.SchemaVer
			}
			class := normalise.ClassOf(perr)
			recs = append(recs, s.dlqRecord(id, vin, schema, class, perr.Error(), raw))
			res.Rejected++
			res.RejectedByClass[class.String()]++
			s.m.rejected.WithLabelValues(class.String()).Inc()
			continue
		}
		val, merr := proto.Marshal(ev)
		if merr != nil {
			s.reply(w, http.StatusInternalServerError, map[string]string{"error": merr.Error()})
			return
		}
		recs = append(recs, &kgo.Record{Topic: s.cfg.TelemetryTopic, Key: []byte(ev.Vin), Value: val})
		res.Accepted++
	}

	if err := s.produce(r.Context(), recs); err != nil {
		s.m.produceErr.Inc()
		s.cfg.Logger.Error("kafka produce failed", "err", err)
		w.Header().Set("Retry-After", "1")
		s.reply(w, http.StatusServiceUnavailable, map[string]string{"error": "stream unavailable"})
		return
	}
	s.m.events.WithLabelValues("accepted").Add(float64(res.Accepted))
	s.m.events.WithLabelValues("rejected").Add(float64(res.Rejected))
	if len(res.RejectedByClass) == 0 {
		res.RejectedByClass = nil
	}
	s.reply(w, http.StatusOK, res)
}

func (s *Server) decode(raw []byte, jsonLines bool, dialect byte) (*telemetryv1.TelemetryEvent, error) {
	if !jsonLines {
		var ev telemetryv1.TelemetryEvent
		if err := proto.Unmarshal(raw, &ev); err != nil {
			return nil, &normalise.Error{Class: dlqv1.ErrorClass_ERROR_CLASS_MALFORMED, Msg: "protobuf: " + err.Error()}
		}
		return &ev, nil
	}
	switch dialect {
	case 'A':
		return normalise.ParseOEMA(raw)
	case 'B':
		return normalise.ParseOEMB(raw)
	}
	return nil, &normalise.Error{Class: dlqv1.ErrorClass_ERROR_CLASS_SCHEMA_INVALID, Msg: "no JSON dialect registered for this connector's OEM"}
}

func (s *Server) dlqRecord(id pki.Identity, vin string, schema uint32, class dlqv1.ErrorClass, detail string, original []byte) *kgo.Record {
	val, _ := proto.Marshal(&dlqv1.DlqRecord{
		Original: truncate(original, 64<<10), ErrorClass: class, ErrorDetail: detail, SourceTopic: "ingest",
		TenantId: id.TenantID.String(), Vin: vin, SchemaVer: schema, FailedAt: timestamppb.New(s.cfg.Now()), Attempts: 1,
	})
	return &kgo.Record{Topic: s.cfg.DLQTopic, Key: []byte(vin), Value: val}
}

// dlqBatch files an undecodable request body in the DLQ (best effort: the caller already answers 400).
func (s *Server) dlqBatch(ctx context.Context, id pki.Identity, class dlqv1.ErrorClass, detail string, original []byte) {
	_ = s.produce(ctx, []*kgo.Record{s.dlqRecord(id, "", 0, class, detail, original)})
	s.m.rejected.WithLabelValues(class.String()).Inc()
}

// produce writes the records and waits for every acknowledgement (so a 200 means durable in Kafka).
func (s *Server) produce(ctx context.Context, recs []*kgo.Record) error {
	if len(recs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ProduceTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var first atomic.Pointer[error]
	wg.Add(len(recs))
	for _, r := range recs {
		s.cl.Produce(ctx, r, func(_ *kgo.Record, err error) {
			if err != nil {
				first.CompareAndSwap(nil, &err)
			}
			wg.Done()
		})
	}
	wg.Wait()
	if p := first.Load(); p != nil {
		return *p
	}
	return nil
}

// splitBatch extracts the repeated `events` field (2) of a TelemetryBatch without decoding the events, so
// one corrupt event cannot take its neighbours down with it.
func splitBatch(b []byte) ([][]byte, error) {
	var out [][]byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			if num == 2 {
				out = append(out, v)
			}
			b = b[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return nil, protowire.ParseError(m)
		}
		b = b[m:]
	}
	return out, nil
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		line := b
		if i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		if line = bytes.TrimSpace(line); len(line) > 0 {
			out = append(out, line)
		}
	}
	return out
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}
