//go:build stack

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"voltsight/internal/dbtool"
	"voltsight/internal/dotenv"
	"voltsight/internal/jwtverify"
	"voltsight/internal/privacy"
)

type harness struct {
	t   *testing.T
	srv *Server
	env map[string]string
	pg  *pgxpool.Pool
}

func newHarness(t *testing.T, mut ...func(*Config)) *harness {
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	dsn, _ := dbtool.OwnerDSN(env)
	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_app", dotenv.Get(env, "DB_APP_PASSWORD"))
	pool, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Password: dotenv.Get(env, "REDIS_PASSWORD")})
	t.Cleanup(func() { rdb.Close() })
	cfg := Config{Verifier: jwtverify.New(jwtverify.Config{Issuer: "http://localhost:8080/realms/voltsight", Audience: "voltsight-api",
		JWKSURL: "http://127.0.0.1:8080/realms/voltsight/protocol/openid-connect/certs"}), Pool: pool, Redis: rdb, RateRPS: 1000, RateBurst: 1000}
	for _, m := range mut {
		m(&cfg)
	}
	return &harness{t: t, srv: New(cfg), env: env, pg: pool}
}

func (h *harness) token(user string) string {
	form := url.Values{"grant_type": {"password"}, "client_id": {"voltsight-test"}, "client_secret": {dotenv.Get(h.env, "KC_TEST_CLIENT_SECRET")},
		"username": {user}, "password": {dotenv.Get(h.env, "DEMO_USER_PASSWORD")}}
	resp, err := http.PostForm("http://localhost:8080/realms/voltsight/protocol/openid-connect/token", form)
	if err != nil {
		h.t.Skipf("Keycloak unavailable: %v", err)
	}
	defer resp.Body.Close()
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	if tr.AccessToken == "" {
		h.t.Fatalf("no token for %s", user)
	}
	return tr.AccessToken
}

func (h *harness) do(method, path, token, body string) (int, []byte) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, b
}

// N11: authentication failures
func TestAuthenticationRejectsBadTokens(t *testing.T) {
	h := newHarness(t)
	good := h.token("viewer@meridian.example")
	parts := strings.Split(good, ".")
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		"missing":         "",
		"garbage":         "not-a-jwt",
		"alg none":        b64(`{"alg":"none","typ":"JWT"}`) + "." + parts[1] + ".",
		"tampered claims": parts[0] + "." + b64(`{"sub":"00000000-0000-0000-0000-000000000000","tenant_id":"00000000-0000-0000-0000-000000000001","iss":"http://localhost:8080/realms/voltsight","aud":"voltsight-api","exp":9999999999}`) + "." + parts[2],
		"bad signature":   parts[0] + "." + parts[1] + "." + b64("forged-signature"),
	}
	for name, tok := range cases {
		if code, _ := h.do("GET", "/v1/me", tok, ""); code != 401 {
			t.Errorf("%s: status %d, want 401", name, code)
		}
	}
	if code, _ := h.do("GET", "/v1/me", good, ""); code != 200 {
		t.Fatalf("valid token rejected: %d", code)
	}
}

// N12: RBAC and subscription entitlements
func TestRBACAndEntitlements(t *testing.T) {
	h := newHarness(t)
	viewer, dispatcher := h.token("viewer@meridian.example"), h.token("dispatcher@meridian.example")
	coastalDisp, coastalEM := h.token("dispatcher@coastal.example"), h.token("energy_manager@coastal.example")
	type c struct {
		name, method, path, tok string
		want                    int
	}
	for _, x := range []c{
		{"viewer reads vehicles", "GET", "/v1/vehicles?limit=1", viewer, 200},
		{"viewer cannot ack", "POST", "/v1/alerts/00000000-0000-0000-0000-000000000000/ack", viewer, 403},
		{"viewer cannot see plans", "GET", "/v1/plans", viewer, 403},
		{"viewer cannot read audit", "GET", "/v1/audit", viewer, 403},
		{"viewer cannot change chargers", "POST", "/v1/chargers/00000000-0000-0000-0000-000000000000/status", viewer, 403},
		{"viewer cannot use copilot", "POST", "/v1/copilot/messages", viewer, 403},
		{"dispatcher cannot see plans", "GET", "/v1/plans", dispatcher, 403},
		{"dispatcher cannot read ops", "GET", "/v1/ops/pipeline", dispatcher, 403},
		{"enterprise dispatcher may use copilot (404/503 without one is not 403)", "POST", "/v1/copilot/messages", dispatcher, 503},
		{"pro plan has no copilot (entitlement)", "POST", "/v1/copilot/messages", coastalDisp, 403},
		{"pro plan has charge planning", "GET", "/v1/plans", coastalEM, 200},
		{"energy manager reads reports", "GET", "/v1/reports/soh", coastalEM, 200},
	} {
		code, body := h.do(x.method, x.path, x.tok, "{}")
		if code != x.want {
			t.Errorf("%s: %s %s -> %d, want %d (%s)", x.name, x.method, x.path, code, x.want, string(body))
		}
	}
}

// N13: a tenant cannot read or change another tenant's data by guessing identifiers
func TestTenantIsolationAttacks(t *testing.T) {
	h := newHarness(t)
	var coastalVIN, coastalCharger, coastalAlert string
	ctx := context.Background()
	dsn, _ := dbtool.OwnerDSN(h.env)
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.QueryRow(ctx, `SELECT vin FROM vehicle v JOIN tenant t ON t.id = v.tenant_id WHERE t.name = 'Coastal Rentals' LIMIT 1`).Scan(&coastalVIN); err != nil {
		t.Skipf("seed missing: %v", err)
	}
	_ = owner.QueryRow(ctx, `SELECT c.id::text FROM charger c JOIN tenant t ON t.id = c.tenant_id WHERE t.name = 'Coastal Rentals' LIMIT 1`).Scan(&coastalCharger)
	// plant an alert in Coastal (the owner role bypasses RLS) and remove it afterwards
	if err := owner.QueryRow(ctx, `INSERT INTO alert (tenant_id, vin, rule, severity, window_start, detected_at, evidence)
		SELECT tenant_id, vin, 'RANGE_LOW', 'WARNING', now(), now(), '{}' FROM vehicle WHERE vin = $1 RETURNING id::text`, coastalVIN).Scan(&coastalAlert); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(ctx, `DELETE FROM alert WHERE id = $1`, coastalAlert) })

	meridian := h.token("dispatcher@meridian.example")
	for _, p := range []string{"/v1/vehicles/" + coastalVIN, "/v1/vehicles/" + coastalVIN + "/telemetry", "/v1/alerts/" + coastalAlert} {
		if code, _ := h.do("GET", p, meridian, ""); code != 404 {
			t.Errorf("GET %s from another tenant -> %d, want 404", p, code)
		}
	}
	if code, _ := h.do("POST", "/v1/alerts/"+coastalAlert+"/ack", meridian, ""); code != 404 {
		t.Errorf("ack of another tenant's alert -> %d, want 404", code)
	}
	if code, _ := h.do("POST", "/v1/chargers/"+coastalCharger+"/status", meridian, `{"status":"OUT_OF_SERVICE"}`); code != 404 && code != 503 {
		t.Errorf("status change of another tenant's charger -> %d, want 404", code)
	}
	// list endpoints never contain foreign rows
	code, body := h.do("GET", "/v1/alerts?limit=200", meridian, "")
	if code != 200 || strings.Contains(string(body), coastalAlert) {
		t.Errorf("alert list leaked a foreign alert (%d)", code)
	}
	code, body = h.do("GET", "/v1/vehicles?limit=200&q="+coastalVIN[:10], meridian, "")
	if code != 200 || strings.Contains(string(body), coastalVIN) {
		t.Errorf("vehicle search leaked a foreign vehicle (%d)", code)
	}
	// the owner can see its own
	coastal := h.token("dispatcher@coastal.example")
	if code, _ := h.do("GET", "/v1/alerts/"+coastalAlert, coastal, ""); code != 200 {
		t.Errorf("owner cannot read its alert: %d", code)
	}
	// injection attempts are inert (parameterised queries / strict validation)
	for _, q := range []string{"/v1/vehicles?q=%27%20OR%20%271%27%3D%271", "/v1/alerts?status=open'%20OR%201=1--", "/v1/alerts?after=%27%3B%20DROP%20TABLE%20alert%3B--", "/v1/vehicles?q=%25"} {
		if code, _ := h.do("GET", q, meridian, ""); code != 400 && code != 200 {
			t.Errorf("%s -> %d", q, code)
		}
	}
	if code, _ := h.do("GET", "/v1/alerts?status=open'%20OR%201=1--", meridian, ""); code != 400 {
		t.Errorf("filter injection must be rejected with 400, got %d", code)
	}
}

// N19: every successful data access leaves an audit row
func TestAuditCompleteness(t *testing.T) {
	h := newHarness(t)
	tok := h.token("tenant_admin@meridian.example")
	count := func() int {
		code, body := h.do("GET", "/v1/audit?limit=500&action=vehicle.list", tok, "")
		if code != 200 {
			t.Fatalf("audit -> %d", code)
		}
		var r struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(body, &r)
		return len(r.Items)
	}
	before := count()
	const n = 7
	for i := 0; i < n; i++ {
		if code, _ := h.do("GET", "/v1/vehicles?limit=1", tok, ""); code != 200 {
			t.Fatalf("vehicles -> %d", code)
		}
	}
	if got := count() - before; got != n {
		t.Fatalf("%d reads left %d audit rows", n, got)
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.RateRPS, c.RateBurst = 1, 5 })
	tok := h.token("viewer@meridian.example")
	var limited int
	for i := 0; i < 20; i++ {
		if code, _ := h.do("GET", "/v1/me", tok, ""); code == 429 {
			limited++
		}
	}
	if limited < 10 {
		t.Fatalf("only %d of 20 rapid requests were limited", limited)
	}
}

// keyset pagination: pages are disjoint, ordered, and complete
func TestKeysetPagination(t *testing.T) {
	h := newHarness(t)
	tok := h.token("viewer@meridian.example")
	seen := map[string]bool{}
	after, last := "", ""
	for page := 0; page < 4; page++ {
		_, body := h.do("GET", "/v1/vehicles?limit=25&after="+after, tok, "")
		var r struct {
			Items []struct{ VIN string } `json:"items"`
			Next  string                 `json:"next"`
		}
		if err := json.Unmarshal(body, &r); err != nil || len(r.Items) != 25 {
			t.Fatalf("page %d: %v %d", page, err, len(r.Items))
		}
		for _, v := range r.Items {
			if seen[v.VIN] || v.VIN <= last {
				t.Fatalf("page %d repeats or misorders %s", page, v.VIN)
			}
			seen[v.VIN], last = true, v.VIN
		}
		after = r.Next
	}
	_ = time.Second
}

// N23: erasure through the API + worker, with verification evidence; another tenant's driver is not reachable
func TestErasureFlowEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dsn, _ := dbtool.OwnerDSN(h.env)
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var subject, other string
	if err := owner.QueryRow(ctx, `SELECT d.id::text FROM driver d JOIN tenant t ON t.id = d.tenant_id WHERE t.name = 'Meridian Logistics' AND d.erased_at IS NULL LIMIT 1`).Scan(&subject); err != nil {
		t.Skip("seed missing")
	}
	_ = owner.QueryRow(ctx, `SELECT d.id::text FROM driver d JOIN tenant t ON t.id = d.tenant_id WHERE t.name = 'Coastal Rentals' LIMIT 1`).Scan(&other)
	var piiBefore int
	_ = owner.QueryRow(ctx, `SELECT count(*) FROM driver WHERE id = $1 AND pii_enc IS NOT NULL`, subject).Scan(&piiBefore)
	if piiBefore != 1 {
		t.Fatal("subject has no PII to erase")
	}
	admin, viewer := h.token("tenant_admin@meridian.example"), h.token("viewer@meridian.example")
	if code, _ := h.do("POST", "/v1/privacy/erasure", viewer, `{"subject_id":"`+subject+`"}`); code != 403 {
		t.Fatalf("a viewer requested an erasure: %d", code)
	}
	if code, _ := h.do("POST", "/v1/privacy/erasure", admin, `{"subject_id":"`+other+`"}`); code != 404 {
		t.Fatalf("erasure of another tenant's driver -> %d, want 404", code)
	}
	code, body := h.do("POST", "/v1/privacy/erasure", admin, `{"subject_id":"`+subject+`"}`)
	if code != 202 {
		t.Fatalf("request -> %d %s", code, body)
	}
	var created struct{ ID string }
	_ = json.Unmarshal(body, &created)

	u, _ := url.Parse(dsn)
	u.User = url.UserPassword("voltsight_privacy", dotenv.Get(h.env, "DB_PRIVACY_PASSWORD"))
	pp, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	n, err := privacy.ProcessPending(ctx, pp)
	if err != nil || n < 1 {
		t.Fatalf("worker completed %d: %v", n, err)
	}
	code, body = h.do("GET", "/v1/privacy/erasure/"+created.ID, admin, "")
	if code != 200 || !strings.Contains(string(body), `"status":"completed"`) || !strings.Contains(string(body), `"driver_pii_ciphertext_remaining":0`) {
		t.Fatalf("status %d %s", code, body)
	}
	var pii int
	var pseudonym string
	_ = owner.QueryRow(ctx, `SELECT count(*) FILTER (WHERE pii_enc IS NOT NULL), max(pseudonym) FROM driver WHERE id = $1`, subject).Scan(&pii, &pseudonym)
	if pii != 0 || !strings.HasPrefix(pseudonym, "erased-") {
		t.Fatalf("subject data remains: pii=%d pseudonym=%q", pii, pseudonym)
	}
}
