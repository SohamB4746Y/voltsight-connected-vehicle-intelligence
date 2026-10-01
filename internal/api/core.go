// Package api is the VoltSight platform API: OIDC/JWT authentication, role-based access control with
// subscription entitlements, tenant isolation (PostgreSQL row-level security + tenant-bound ClickHouse/Redis
// access), per-user rate limiting, a complete audit trail, SSE alert streaming and the read/write endpoints
// used by the web console and the Copilot.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
	"golang.org/x/time/rate"

	"voltsight/internal/jwtverify"
)

// Permissions.
const (
	PermFleetRead     = "fleet.read"
	PermMapRead       = "map.read"
	PermAlertsRead    = "alerts.read"
	PermAlertsWrite   = "alerts.write"
	PermChargersRead  = "chargers.read"
	PermChargersWrite = "chargers.write"
	PermPlansRead     = "plans.read"
	PermPlansWrite    = "plans.write"
	PermReportsRead   = "reports.read"
	PermCopilotUse    = "copilot.use"
	PermAuditRead     = "audit.read"
	PermPrivacyWrite  = "privacy.write"
	PermOpsRead       = "ops.read"
	PermPreciseGeo    = "geo.precise"
)

var rolePerms = map[string][]string{
	"viewer": {PermFleetRead, PermMapRead, PermAlertsRead, PermChargersRead},
	"dispatcher": {PermFleetRead, PermMapRead, PermAlertsRead, PermAlertsWrite, PermChargersRead, PermChargersWrite,
		PermCopilotUse, PermPreciseGeo},
	"energy_manager": {PermFleetRead, PermMapRead, PermAlertsRead, PermChargersRead, PermPlansRead, PermPlansWrite,
		PermReportsRead, PermCopilotUse},
	"tenant_admin": {PermFleetRead, PermMapRead, PermAlertsRead, PermAlertsWrite, PermChargersRead, PermChargersWrite,
		PermPlansRead, PermReportsRead, PermAuditRead, PermPrivacyWrite, PermPreciseGeo},
	"platform_admin": {PermOpsRead},
}

// feature required by a permission (subscription entitlements, plan_feature.feature)
var permFeature = map[string]string{
	PermMapRead: "live_map", PermAlertsRead: "alerts", PermAlertsWrite: "alerts",
	PermPlansRead: "charge_planning", PermPlansWrite: "charge_planning", PermReportsRead: "soh", PermCopilotUse: "copilot",
}

// Config configures the server.
type Config struct {
	Verifier  *jwtverify.Verifier
	Pool      *pgxpool.Pool // connected as voltsight_app (row-level security applies)
	Redis     redis.UniversalClient
	CH        driver.Conn // may be nil: history endpoints answer 503
	Kafka     *kgo.Client // may be nil: charger status writes answer 503
	RateRPS   float64     // per user, default 50
	RateBurst int         // default 100
	StaticDir string      // web console build, served at /
	Origins   []string    // CORS allow-list
	Now       func() time.Time
}

// CopilotHandler is implemented by the copilot package.
type CopilotHandler interface {
	Handle(w http.ResponseWriter, r *http.Request, p *Principal)
	Approve(w http.ResponseWriter, r *http.Request, p *Principal)
}

// Server is the API.
type Server struct {
	cfg  Config
	mux  *http.ServeMux
	lim  sync.Map // user -> *rate.Limiter
	feat sync.Map // tenant -> featureCache
	snap sync.Map // tenant -> *snapshot

	copilot CopilotHandler
}

// SetCopilot installs the copilot (it needs the server for its tools, so it is attached after New).
func (s *Server) SetCopilot(c CopilotHandler) { s.copilot = c }

// Principal is the authenticated caller. Tenant and roles come only from the verified token.
type Principal struct {
	UserID    uuid.UUID
	TenantID  uuid.UUID
	Roles     []string
	RequestID string
}

// Has reports whether any of the caller's roles grants the permission.
func (p *Principal) Has(perm string) bool {
	for _, r := range p.Roles {
		for _, x := range rolePerms[r] {
			if x == perm {
				return true
			}
		}
	}
	return false
}

type ctxKey int

// New builds the server.
func New(cfg Config) *Server {
	if cfg.RateRPS == 0 {
		cfg.RateRPS = 50
	}
	if cfg.RateBurst == 0 {
		cfg.RateBurst = 100
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rid := r.Header.Get("X-Request-Id")
	if rid == "" || len(rid) > 64 {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		rid = hex.EncodeToString(b)
	}
	w.Header().Set("X-Request-Id", rid)
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self' http://localhost:8080; frame-ancestors 'none'")
	h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
	h.Set("Cache-Control", "no-store")
	if o := r.Header.Get("Origin"); o != "" {
		for _, a := range s.cfg.Origins {
			if a == o {
				h.Set("Access-Control-Allow-Origin", o)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Request-Id")
				h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			}
		}
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // bound request bodies
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey(2), rid)))
}

func requestID(ctx context.Context) string { s, _ := ctx.Value(ctxKey(2)).(string); return s }

// ---------------------------------------------------------------------------------------------
// errors and JSON

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // no mass assignment
	if err := dec.Decode(v); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// ---------------------------------------------------------------------------------------------
// middleware: authenticate -> rate limit -> authorise -> entitlement -> handler -> audit

type handler func(w http.ResponseWriter, r *http.Request, p *Principal)

// guard wraps a handler with authentication, the rate limit, a permission check, the subscription entitlement
// check and the audit record. perm "" means "any authenticated user".
func (s *Server) guard(perm, auditAction, resource string, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := ""
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			tok = strings.TrimSpace(a[7:])
		}
		if tok == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="voltsight"`)
			writeProblem(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		cl, err := s.cfg.Verifier.Verify(r.Context(), tok)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeProblem(w, http.StatusUnauthorized, "invalid token")
			return
		}
		uid, err := uuid.Parse(cl.Subject)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "invalid subject")
			return
		}
		p := &Principal{UserID: uid, TenantID: cl.TenantID, Roles: cl.Roles, RequestID: requestID(r.Context())}
		if !s.allow(p) {
			w.Header().Set("Retry-After", "1")
			writeProblem(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		if perm != "" {
			if !p.Has(perm) {
				writeProblem(w, http.StatusForbidden, "your role does not allow this")
				return
			}
			if f, ok := permFeature[perm]; ok {
				if has, err := s.hasFeature(r.Context(), p.TenantID, f); err != nil {
					writeProblem(w, http.StatusServiceUnavailable, "entitlement lookup failed")
					return
				} else if !has {
					writeProblem(w, http.StatusForbidden, fmt.Sprintf("feature %q is not part of your subscription", f))
					return
				}
			}
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h(sw, r, p)
		if auditAction != "" && sw.status < 500 {
			s.audit(r.Context(), p, auditAction, resource, r, sw.status)
		}
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) allow(p *Principal) bool {
	k := p.UserID.String()
	v, ok := s.lim.Load(k)
	if !ok {
		v, _ = s.lim.LoadOrStore(k, rate.NewLimiter(rate.Limit(s.cfg.RateRPS), s.cfg.RateBurst))
	}
	return v.(*rate.Limiter).Allow()
}

type featureCache struct {
	set map[string]bool
	at  time.Time
}

// hasFeature reads the tenant's active subscription entitlements (cached for 30 s).
func (s *Server) hasFeature(ctx context.Context, tenant uuid.UUID, feature string) (bool, error) {
	if v, ok := s.feat.Load(tenant); ok {
		if c := v.(featureCache); s.cfg.Now().Sub(c.at) < 30*time.Second {
			return c.set[feature], nil
		}
	}
	set := map[string]bool{}
	err := s.tx(ctx, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT pf.feature FROM subscription s JOIN plan_feature pf ON pf.plan_code = s.plan_code
			WHERE s.tenant_id = $1 AND s.valid_from <= now() AND (s.valid_to IS NULL OR s.valid_to > now())`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f string
			if err := rows.Scan(&f); err != nil {
				return err
			}
			set[f] = true
		}
		return rows.Err()
	})
	if err != nil {
		return false, err
	}
	s.feat.Store(tenant, featureCache{set: set, at: s.cfg.Now()})
	return set[feature], nil
}

// tx runs fn in a transaction bound to the tenant: app.tenant_id is set locally, so every statement is subject
// to the row-level-security policies of that tenant.
func (s *Server) tx(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) error {
	t, err := s.cfg.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer t.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := t.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant.String()); err != nil {
		return err
	}
	if err := fn(t); err != nil {
		return err
	}
	return t.Commit(ctx)
}

// audit records an access. It is synchronous: an access that succeeded always has its audit row.
func (s *Server) audit(ctx context.Context, p *Principal, action, resource string, r *http.Request, status int) {
	rid := ""
	if r != nil {
		rid = p.RequestID
	}
	det := map[string]any{"status": status, "method": r.Method, "path": r.URL.Path}
	if q := r.URL.RawQuery; q != "" && len(q) < 512 {
		det["query"] = q
	}
	b, _ := json.Marshal(det)
	_ = s.tx(context.WithoutCancel(ctx), p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, actor_type, action, resource_type, resource_id, request_id, details)
			VALUES ($1, $2, 'user', $3, $4, $5, $6, $7)`, p.TenantID, p.UserID.String(), action, resource, r.PathValue("id")+r.PathValue("vin"), rid, b)
		return err
	})
}

// Audit lets other packages (copilot) record an event for the caller.
func (s *Server) Audit(ctx context.Context, p *Principal, actorType, action, resource, resourceID string, details map[string]any) error {
	b, _ := json.Marshal(details)
	return s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (tenant_id, actor, actor_type, action, resource_type, resource_id, request_id, details)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, p.UserID.String(), actorType, action, resource, resourceID, p.RequestID, b)
		return err
	})
}

// ---------------------------------------------------------------------------------------------
// helpers

func intParam(r *http.Request, name string, def, min, max int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func notFound(w http.ResponseWriter) { writeProblem(w, http.StatusNotFound, "not found") }

func serverError(w http.ResponseWriter, err error) {
	// never leak internals to the client; the request id correlates the response with this log line
	slog.Error("api error", "request_id", w.Header().Get("X-Request-Id"), "err", err)
	writeProblem(w, http.StatusInternalServerError, "internal error")
}

// maskLocation coarsens a coordinate to ~1.1 km (2 decimals) unless the caller may see precise positions.
func maskLocation(p *Principal, lat, lon float64) (float64, float64) {
	if p.Has(PermPreciseGeo) {
		return lat, lon
	}
	r := func(v float64) float64 { return float64(int64(v*100+sign(v)*0.5)) / 100 }
	return r(lat), r(lon)
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func fmtSscan(s string, f *float64) (int, error) { return fmt.Sscanf(s, "%g", f) }

func fmtSscanInt(s string, n *int64) (int, error) { return fmt.Sscanf(s, "%d", n) }

// ---------------------------------------------------------------------------------------------
// exported helpers for the copilot (same data access paths, same tenant binding)

// Tx runs fn in a tenant-bound transaction (row-level security applies).
func (s *Server) Tx(ctx context.Context, tenant uuid.UUID, fn func(pgx.Tx) error) error {
	return s.tx(ctx, tenant, fn)
}

// States fetches the latest vehicle states, masked for the caller's role.
func (s *Server) States(ctx context.Context, p *Principal, vins []string) (map[string]VehicleState, error) {
	m, err := s.states(ctx, p.TenantID, vins)
	for k, v := range m {
		s.maskState(p, &v)
		m[k] = v
	}
	return m, err
}

// CH returns the history store connection (may be nil).
func (s *Server) CH() driver.Conn { return s.cfg.CH }

// Redis returns the shared Redis client.
func (s *Server) Redis() redis.UniversalClient { return s.cfg.Redis }

// MaskEvidence applies the role-based masking to alert evidence.
func (s *Server) MaskEvidence(p *Principal, raw json.RawMessage) json.RawMessage {
	return s.maskEvidence(p, raw)
}

// ApprovePlan approves a proposed plan on behalf of the caller. It reports whether the plan was in the proposed state.
func (s *Server) ApprovePlan(ctx context.Context, p *Principal, id uuid.UUID) (bool, error) {
	var n int64
	err := s.tx(ctx, p.TenantID, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE charge_plan SET status = 'approved', approved_by = $3, approved_at = now() WHERE tenant_id = $1 AND id = $2 AND status = 'proposed'`, p.TenantID, id, p.UserID)
		if e == nil {
			n = tag.RowsAffected()
		}
		return e
	})
	return n > 0, err
}
