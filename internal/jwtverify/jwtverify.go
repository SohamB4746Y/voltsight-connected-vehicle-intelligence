// Package jwtverify validates OIDC access tokens issued by Keycloak: signature against the issuer's
// JWKS, algorithm allow-list (RS256/ES256, never "none" or HMAC), issuer, audience, expiry and the
// presence of the tenant claim.
package jwtverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims are the validated claims the platform relies on.
type Claims struct {
	Subject  string
	TenantID uuid.UUID
	Roles    []string
}

// Config configures a Verifier.
type Config struct {
	Issuer   string // exact `iss` value, e.g. http://localhost:8080/realms/voltsight
	Audience string // required `aud` entry, e.g. voltsight-api
	JWKSURL  string
	Leeway   time.Duration
	HTTP     *http.Client
	Now      func() time.Time // injectable for tests
}

// Verifier validates tokens. It is safe for concurrent use.
type Verifier struct {
	cfg  Config
	mu   sync.RWMutex
	keys map[string]any // kid -> *rsa.PublicKey | *ecdsa.PublicKey
	last time.Time      // last JWKS refresh attempt
}

// New returns a Verifier.
func New(cfg Config) *Verifier {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 5 * time.Second
	}
	return &Verifier{cfg: cfg, keys: map[string]any{}}
}

// Errors.
var (
	ErrInvalid       = errors.New("jwtverify: invalid token")
	ErrNoTenantClaim = errors.New("jwtverify: token has no valid tenant_id claim")
)

type jwk struct {
	Kty, Kid, Alg, Use, N, E, Crv, X, Y string
}

func b64(s string) []byte { b, _ := base64.RawURLEncoding.DecodeString(s); return b }

func (v *Verifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("jwks fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var set struct{ Keys []jwk }
	if err := json.Unmarshal(body, &set); err != nil {
		return err
	}
	keys := map[string]any{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(b64(k.N)), E: int(new(big.Int).SetBytes(b64(k.E)).Int64())}
		case "EC":
			var c elliptic.Curve
			var size int
			switch k.Crv {
			case "P-256":
				c, size = elliptic.P256(), 32
			case "P-384":
				c, size = elliptic.P384(), 48
			default:
				continue
			}
			// uncompressed point 0x04 || X || Y with fixed-width coordinates; ParseUncompressedPublicKey also
			// rejects points that are not on the curve
			x, y := b64(k.X), b64(k.Y)
			if len(x) > size || len(y) > size {
				continue
			}
			pt := make([]byte, 1+2*size)
			pt[0] = 4
			copy(pt[1+size-len(x):1+size], x)
			copy(pt[1+2*size-len(y):], y)
			if pub, err := ecdsa.ParseUncompressedPublicKey(c, pt); err == nil {
				keys[k.Kid] = pub
			}
		}
	}
	v.mu.Lock()
	v.keys, v.last = keys, v.cfg.Now()
	v.mu.Unlock()
	return nil
}

// keyFor returns the key for kid, refreshing the JWKS at most once per 10 s when it is unknown
// (key rotation) so a flood of forged kids cannot hammer the identity provider.
func (v *Verifier) keyFor(ctx context.Context, kid string) (any, error) {
	v.mu.RLock()
	k, ok := v.keys[kid]
	last := v.last
	v.mu.RUnlock()
	if ok {
		return k, nil
	}
	if last.IsZero() || v.cfg.Now().Sub(last) > 10*time.Second {
		if err := v.refresh(ctx); err != nil {
			return nil, err
		}
		v.mu.RLock()
		k, ok = v.keys[kid]
		v.mu.RUnlock()
		if ok {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: unknown signing key %q", ErrInvalid, kid)
}

// Verify validates the compact JWT and returns its claims.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	parsed, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		return v.keyFor(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(v.cfg.Leeway),
		jwt.WithTimeFunc(v.cfg.Now),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalid
	}
	sub, _ := mc["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("%w: no subject", ErrInvalid)
	}
	tid, err := uuid.Parse(strClaim(mc["tenant_id"]))
	if err != nil {
		return nil, ErrNoTenantClaim
	}
	var roles []string
	if arr, ok := mc["roles"].([]any); ok {
		for _, r := range arr {
			if s, ok := r.(string); ok {
				roles = append(roles, s)
			}
		}
	}
	return &Claims{Subject: sub, TenantID: tid, Roles: roles}, nil
}

func strClaim(v any) string {
	s, _ := v.(string)
	return s
}
