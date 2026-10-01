//go:build stack

package jwtverify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"voltsight/internal/dotenv"
	"voltsight/internal/realm"
	"voltsight/internal/seedgen"
)

// G1.8 against the real Keycloak of the compose stack (`make up`).

const kcBase = "http://localhost:8080"

type kcEnv struct {
	secret, demoPassword, adminPassword string
	verifier                            *Verifier
}

func setupKC(t *testing.T) *kcEnv {
	t.Helper()
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	e := &kcEnv{
		secret: dotenv.Get(env, "KC_TEST_CLIENT_SECRET"), demoPassword: dotenv.Get(env, "DEMO_USER_PASSWORD"),
		adminPassword: dotenv.Get(env, "KEYCLOAK_ADMIN_PASSWORD"),
	}
	if e.secret == "" || e.demoPassword == "" {
		t.Fatal("secrets missing from .env; run `make env && make up`")
	}
	e.verifier = New(Config{
		Issuer: kcBase + "/realms/" + realm.Name, Audience: realm.APIClient,
		JWKSURL: kcBase + "/realms/" + realm.Name + "/protocol/openid-connect/certs",
	})
	return e
}

func post(t *testing.T, endpoint string, form url.Values, bearer string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *kcEnv) token(t *testing.T, user, password, client, secret string) (int, string) {
	t.Helper()
	form := url.Values{"grant_type": {"password"}, "client_id": {client}, "username": {user}, "password": {password}, "scope": {"openid"}}
	if secret != "" {
		form.Set("client_secret", secret)
	}
	code, body := post(t, kcBase+"/realms/"+realm.Name+"/protocol/openid-connect/token", form, "")
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(body, &out)
	return code, out.AccessToken
}

func TestKC_DemoUsersGetCorrectlyScopedTokens(t *testing.T) {
	e := setupKC(t)
	w, _ := seedgen.Generate(seedgen.Config{Seed: 1, Vehicles: 1})
	if len(w.Users) != 9 {
		t.Fatalf("seed users = %d", len(w.Users))
	}
	for _, u := range w.Users {
		code, tok := e.token(t, u.Email, e.demoPassword, realm.TestClient, e.secret)
		if code != 200 || tok == "" {
			t.Fatalf("login for %s failed: HTTP %d", u.Email, code)
		}
		c, err := e.verifier.Verify(context.Background(), tok)
		if err != nil {
			t.Fatalf("token for %s does not verify: %v", u.Email, err)
		}
		if c.Subject != u.ID.String() {
			t.Errorf("%s: subject %s != app_user.id %s", u.Email, c.Subject, u.ID)
		}
		if c.TenantID != u.TenantID {
			t.Errorf("%s: tenant claim %s != seed tenant %s", u.Email, c.TenantID, u.TenantID)
		}
		hasRole := false
		for _, r := range c.Roles {
			hasRole = hasRole || r == u.Role
		}
		if !hasRole {
			t.Errorf("%s: roles %v lack %s", u.Email, c.Roles, u.Role)
		}
		if len(c.Roles) != 1 && !strings.HasPrefix(strings.Join(c.Roles, ","), u.Role) {
			// default realm roles (offline_access, uma_authorization) may also appear; the app role must be present
			t.Logf("%s roles: %v", u.Email, c.Roles)
		}
	}
	// tenant A's token never carries tenant B's id
	a, b := w.Users[0], w.Users[len(DemoUsersPerTenant())]
	if a.TenantID == b.TenantID {
		t.Fatal("test precondition: first users of two different tenants expected")
	}
	_, tokA := e.token(t, a.Email, e.demoPassword, realm.TestClient, e.secret)
	cA, _ := e.verifier.Verify(context.Background(), tokA)
	if cA.TenantID == b.TenantID {
		t.Fatal("tenant A's token carries tenant B's id")
	}
}

// DemoUsersPerTenant is the number of seeded demo users per tenant (helper for index arithmetic).
func DemoUsersPerTenant() []string { return seedgen.DemoRoles }

func TestKC_BadCredentialsAndClientsAreRejected(t *testing.T) {
	e := setupKC(t)
	user := "dispatcher@meridian.example"
	if code, tok := e.token(t, user, "wrong-password", realm.TestClient, e.secret); code == 200 || tok != "" {
		t.Fatalf("wrong password accepted (HTTP %d)", code)
	}
	if code, tok := e.token(t, "nobody@meridian.example", e.demoPassword, realm.TestClient, e.secret); code == 200 || tok != "" {
		t.Fatalf("unknown user accepted (HTTP %d)", code)
	}
	if code, tok := e.token(t, user, e.demoPassword, realm.TestClient, "wrong-secret"); code == 200 || tok != "" {
		t.Fatalf("wrong client secret accepted (HTTP %d)", code)
	}
	// the production web client has no direct-grant (password) flow
	if code, tok := e.token(t, user, e.demoPassword, realm.WebClient, ""); code == 200 || tok != "" {
		t.Fatalf("web client allowed the password grant (HTTP %d)", code)
	}
	// the bearer-only API client cannot be used to log in
	if code, tok := e.token(t, user, e.demoPassword, realm.APIClient, ""); code == 200 || tok != "" {
		t.Fatalf("bearer-only API client allowed a login (HTTP %d)", code)
	}
}

func TestKC_WebClientRequiresPKCE(t *testing.T) {
	cl := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	auth := func(extra url.Values) (int, string) {
		q := url.Values{"response_type": {"code"}, "client_id": {realm.WebClient}, "redirect_uri": {"http://localhost:5173/callback"}, "scope": {"openid"}, "state": {"s"}}
		for k, v := range extra {
			q[k] = v
		}
		resp, err := cl.Get(kcBase + "/realms/" + realm.Name + "/protocol/openid-connect/auth?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	code, loc := auth(nil)
	if code/100 != 3 || !strings.Contains(loc, "error=invalid_request") {
		t.Fatalf("authorization request without PKCE must be rejected, got HTTP %d Location=%q", code, loc)
	}
	code, loc = auth(url.Values{"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"plain"}})
	if code/100 != 3 || !strings.Contains(loc, "error=invalid_request") {
		t.Fatalf("PKCE method 'plain' must be rejected (S256 only), got HTTP %d Location=%q", code, loc)
	}
	code, loc = auth(url.Values{"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}})
	if code != 200 && !(code/100 == 3 && !strings.Contains(loc, "error=")) {
		t.Fatalf("a valid S256 request should reach the login page, got HTTP %d Location=%q", code, loc)
	}
	// open redirect: an unregistered redirect_uri must not be honoured
	resp, err := cl.Get(kcBase + "/realms/" + realm.Name + "/protocol/openid-connect/auth?" + url.Values{
		"response_type": {"code"}, "client_id": {realm.WebClient}, "redirect_uri": {"http://evil.test/cb"}, "scope": {"openid"},
		"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if strings.Contains(resp.Header.Get("Location"), "evil.test") {
		t.Fatal("authorization endpoint redirected to an unregistered redirect_uri")
	}
}

// A user must not be able to change their own tenant_id (the attribute is ADMIN_EDIT only).
func TestKC_UserCannotForgeTenantClaim(t *testing.T) {
	e := setupKC(t)
	w, _ := seedgen.Generate(seedgen.Config{Seed: 1, Vehicles: 1})
	victim, other := w.Users[0], w.Users[len(seedgen.DemoRoles)] // different tenants
	_, tok := e.token(t, victim.Email, e.demoPassword, realm.TestClient, e.secret)

	account := func(method, body string) (int, string) {
		req, _ := http.NewRequest(method, kcBase+"/realms/"+realm.Name+"/account", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// Precondition: the attacker really can use the self-service API (otherwise the test proves nothing).
	code, profile := account(http.MethodGet, "")
	if code != 200 {
		t.Fatalf("precondition failed: the user cannot read their own profile (HTTP %d): %s", code, profile)
	}
	if strings.Contains(profile, "tenant_id") {
		t.Fatalf("tenant_id is visible to the user in their own profile (ADMIN_EDIT must hide it): %s", profile)
	}
	body := `{"firstName":"x","lastName":"y","email":"` + victim.Email + `","attributes":{"tenant_id":["` + other.TenantID.String() + `"]}}`
	code, resp := account(http.MethodPost, body)
	t.Logf("attack: POST /account with a forged tenant_id -> HTTP %d %s", code, resp)

	_, tok2 := e.token(t, victim.Email, e.demoPassword, realm.TestClient, e.secret)
	c, err := e.verifier.Verify(context.Background(), tok2)
	if err != nil {
		t.Fatal(err)
	}
	if c.TenantID != victim.TenantID {
		t.Fatalf("user changed their own tenant claim to %s", c.TenantID)
	}
}

func TestKC_RealmHardeningSettings(t *testing.T) {
	e := setupKC(t)
	code, body := post(t, kcBase+"/realms/master/protocol/openid-connect/token",
		url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {e.adminPassword}}, "")
	if code != 200 {
		t.Fatalf("admin login failed: HTTP %d %s", code, body)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(body, &tok)
	get := func(path string, v any) {
		req, _ := http.NewRequest(http.MethodGet, kcBase+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	var r struct {
		BruteForceProtected  bool
		RegistrationAllowed  bool
		AccessTokenLifespan  int
		SslRequired          string
		DefaultSignatureAlgo string `json:"defaultSignatureAlgorithm"`
	}
	get("/admin/realms/"+realm.Name, &r)
	if !r.BruteForceProtected || r.RegistrationAllowed || r.AccessTokenLifespan > 900 || r.SslRequired != "external" || r.DefaultSignatureAlgo != "RS256" {
		t.Fatalf("realm hardening not applied: %+v", r)
	}
	var roles []struct{ Name string }
	get("/admin/realms/"+realm.Name+"/roles", &roles)
	have := map[string]bool{}
	for _, ro := range roles {
		have[ro.Name] = true
	}
	for _, want := range []string{"viewer", "dispatcher", "energy_manager", "tenant_admin", "platform_admin"} {
		if !have[want] {
			t.Errorf("role %s missing from the realm", want)
		}
	}
	var clients []struct {
		ClientID                  string
		PublicClient              bool
		ImplicitFlowEnabled       bool
		DirectAccessGrantsEnabled bool
		Attributes                map[string]string
	}
	get("/admin/realms/"+realm.Name+"/clients?clientId="+realm.WebClient, &clients)
	if len(clients) != 1 || !clients[0].PublicClient || clients[0].ImplicitFlowEnabled || clients[0].DirectAccessGrantsEnabled ||
		clients[0].Attributes["pkce.code.challenge.method"] != "S256" {
		t.Fatalf("web client configuration unsafe: %+v", clients)
	}
}
