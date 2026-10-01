package jwtverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	issuer   = "https://idp.test/realms/voltsight"
	audience = "voltsight-api"
)

type idp struct {
	srv     *httptest.Server
	key     *rsa.PrivateKey
	kid     string
	fetches atomic.Int32
	extra   []map[string]string // extra JWKS entries
	hidden  atomic.Bool         // when true the JWKS omits the primary key (simulates pre-rotation)
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: k, kid: "key-1"}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.fetches.Add(1)
		keys := []map[string]string{}
		if !p.hidden.Load() {
			keys = append(keys, map[string]string{"kty": "RSA", "kid": p.kid, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
		}
		keys = append(keys, p.extra...)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) verifier(now time.Time) *Verifier {
	return New(Config{Issuer: issuer, Audience: audience, JWKSURL: p.srv.URL, Now: func() time.Time { return now }})
}

func (p *idp) sign(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func goodClaims(now time.Time, tenant uuid.UUID) jwt.MapClaims {
	return jwt.MapClaims{
		"iss": issuer, "aud": []string{audience, "account"}, "sub": "user-1",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "tenant_id": tenant.String(),
		"roles": []string{"dispatcher", "viewer"},
	}
}

func TestValidToken(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	c, err := p.verifier(now).Verify(context.Background(), p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, goodClaims(now, tenant)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-1" || c.TenantID != tenant || len(c.Roles) != 2 || c.Roles[0] != "dispatcher" {
		t.Fatalf("unexpected claims: %+v", c)
	}
}

func TestRejectedTokens(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	v := p.verifier(now)
	ctx := context.Background()
	mut := func(f func(jwt.MapClaims)) jwt.MapClaims { c := goodClaims(now, tenant); f(c); return c }
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	cases := map[string]string{
		"expired":             p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { c["exp"] = now.Add(-time.Hour).Unix() })),
		"no exp":              p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { delete(c, "exp") })),
		"not yet valid":       p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { c["nbf"] = now.Add(time.Hour).Unix() })),
		"wrong issuer":        p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { c["iss"] = "https://evil.test" })),
		"wrong audience":      p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { c["aud"] = "other-api" })),
		"no audience":         p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { delete(c, "aud") })),
		"no subject":          p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, mut(func(c jwt.MapClaims) { delete(c, "sub") })),
		"signed by other key": p.sign(t, jwt.SigningMethodRS256, other, p.kid, goodClaims(now, tenant)),
		"unknown kid":         p.sign(t, jwt.SigningMethodRS256, p.key, "nope", goodClaims(now, tenant)),
		"missing kid":         p.sign(t, jwt.SigningMethodRS256, p.key, "", goodClaims(now, tenant)),
		"alg none":            mustNone(t, goodClaims(now, tenant)),
		"garbage":             "not.a.jwt",
		"empty":               "",
	}
	// algorithm confusion: HS256 signed with the (public) RSA modulus bytes as the secret
	cases["hs256 confusion"] = p.sign(t, jwt.SigningMethodHS256, p.key.N.Bytes(), p.kid, goodClaims(now, tenant))
	der, _ := x509.MarshalPKIXPublicKey(&p.key.PublicKey)
	cases["hs256 with pkix key"] = p.sign(t, jwt.SigningMethodHS256, der, p.kid, goodClaims(now, tenant))

	for name, tok := range cases {
		if _, err := v.Verify(ctx, tok); err == nil {
			t.Errorf("%s: token was accepted", name)
		} else if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}

	// tampered payload keeps the original signature
	good := p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, goodClaims(now, tenant))
	parts := splitJWT(good)
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"` + issuer + `","aud":"` + audience + `","sub":"admin","tenant_id":"` + uuid.NewString() + `","exp":9999999999}`))
	if _, err := v.Verify(ctx, parts[0]+"."+forged+"."+parts[2]); err == nil {
		t.Error("tampered payload accepted")
	}
}

func TestTenantClaimRequired(t *testing.T) {
	p, now := newIDP(t), time.Now()
	v := p.verifier(now)
	for name, val := range map[string]any{"missing": nil, "not a uuid": "tenant-a", "empty": "", "number": 42, "list": []string{uuid.NewString()}} {
		c := goodClaims(now, uuid.New())
		if val == nil {
			delete(c, "tenant_id")
		} else {
			c["tenant_id"] = val
		}
		_, err := v.Verify(context.Background(), p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, c))
		if !errors.Is(err, ErrNoTenantClaim) {
			t.Errorf("%s: want ErrNoTenantClaim, got %v", name, err)
		}
	}
}

func TestECKeysAndKeyRotationRefetch(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p.extra = []map[string]string{{"kty": "EC", "kid": "ec-1", "use": "sig", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(ec.X.Bytes()), "y": base64.RawURLEncoding.EncodeToString(ec.Y.Bytes())},
		{"kty": "EC", "kid": "bad-curve", "crv": "P-521"}, {"kty": "RSA", "kid": "enc", "use": "enc"}}
	v := p.verifier(now)
	ctx := context.Background()
	if _, err := v.Verify(ctx, p.sign(t, jwt.SigningMethodES256, ec, "ec-1", goodClaims(now, tenant))); err != nil {
		t.Fatalf("ES256 token rejected: %v", err)
	}
	before := p.fetches.Load()
	for i := 0; i < 20; i++ { // a flood of unknown kids must not hammer the IdP
		_, _ = v.Verify(ctx, p.sign(t, jwt.SigningMethodRS256, p.key, "forged", goodClaims(now, tenant)))
	}
	if after := p.fetches.Load(); after-before > 1 {
		t.Fatalf("%d JWKS fetches for 20 forged kids; refetch must be rate-limited", after-before)
	}
}

// A JWKS entry whose point is not on the curve (or is oversized) must never become a usable key.
func TestOffCurveECKeyIsRejected(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	one := base64.RawURLEncoding.EncodeToString([]byte{1})
	huge := base64.RawURLEncoding.EncodeToString(make([]byte, 40))
	p.extra = []map[string]string{
		{"kty": "EC", "kid": "off-curve", "use": "sig", "crv": "P-256", "x": one, "y": one},
		{"kty": "EC", "kid": "oversized", "use": "sig", "crv": "P-256", "x": huge, "y": huge},
	}
	v := p.verifier(now)
	for _, kid := range []string{"off-curve", "oversized"} {
		if _, err := v.Verify(context.Background(), p.sign(t, jwt.SigningMethodES256, ec, kid, goodClaims(now, tenant))); err == nil {
			t.Fatalf("token verified against the invalid %s key", kid)
		}
	}
}

func TestRotationPicksUpNewKey(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	clock := now
	v := New(Config{Issuer: issuer, Audience: audience, JWKSURL: p.srv.URL, Now: func() time.Time { return clock }})
	p.hidden.Store(true) // IdP has not published the new key yet
	tok := p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, goodClaims(now, tenant))
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("token signed by an unpublished key was accepted")
	}
	p.hidden.Store(false) // key now published
	clock = clock.Add(11 * time.Second)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("after rotation the new key must be fetched: %v", err)
	}
}

func TestJWKSUnavailableFailsClosed(t *testing.T) {
	p, now, tenant := newIDP(t), time.Now(), uuid.New()
	tok := p.sign(t, jwt.SigningMethodRS256, p.key, p.kid, goodClaims(now, tenant))
	p.srv.Close()
	if _, err := p.verifier(now).Verify(context.Background(), tok); err == nil {
		t.Fatal("token accepted although the JWKS could not be fetched")
	}
}

func TestJWKSNon200AndGarbage(t *testing.T) {
	now := time.Now()
	for name, h := range map[string]http.HandlerFunc{
		"500":     func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) },
		"garbage": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		s := httptest.NewServer(h)
		v := New(Config{Issuer: issuer, Audience: audience, JWKSURL: s.URL, Now: func() time.Time { return now }})
		if _, err := v.Verify(context.Background(), "a.b.c"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		s.Close()
	}
}

func mustNone(t *testing.T, c jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func splitJWT(s string) [3]string {
	var out [3]string
	i := 0
	start := 0
	for j := 0; j < len(s) && i < 2; j++ {
		if s[j] == '.' {
			out[i] = s[start:j]
			start = j + 1
			i++
		}
	}
	out[2] = s[start:]
	return out
}
