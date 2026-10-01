package pki

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Role is the Vault PKI role used for connector certificates.
const Role = "device-connector"

// MaxTTL is the longest lifetime the role allows for a connector certificate.
const MaxTTL = 90 * 24 * time.Hour

// Vault is a minimal client for the Vault PKI secrets engine mounted at "pki".
type Vault struct {
	Addr  string // e.g. http://127.0.0.1:8200
	Token string
	HTTP  *http.Client
}

// NewVault returns a client with sane timeouts.
func NewVault(addr, token string) *Vault {
	return &Vault{Addr: strings.TrimRight(addr, "/"), Token: token, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

func (v *Vault) do(ctx context.Context, method, path string, body any, auth bool) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.Addr+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if auth {
		req.Header.Set("X-Vault-Token", v.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return out, resp.StatusCode, err
}

func (v *Vault) call(ctx context.Context, method, path string, body any) (map[string]any, error) {
	out, code, err := v.do(ctx, method, path, body, true)
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("vault %s %s: HTTP %d: %s", method, path, code, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Data map[string]any `json:"data"`
	}
	if len(out) > 0 {
		if err := json.Unmarshal(out, &parsed); err != nil {
			return nil, fmt.Errorf("vault %s %s: %w", method, path, err)
		}
	}
	return parsed.Data, nil
}

// Bootstrap idempotently mounts the PKI engine, creates the root CA and the connector role.
// (The dev-mode Vault is in-memory, so this runs on every `make up`.)
func (v *Vault) Bootstrap(ctx context.Context) error {
	mounts, err := v.call(ctx, http.MethodGet, "/v1/sys/mounts", nil)
	if err != nil {
		return err
	}
	if _, ok := mounts["pki/"]; !ok {
		if _, err := v.call(ctx, http.MethodPost, "/v1/sys/mounts/pki", map[string]any{
			"type": "pki", "config": map[string]any{"max_lease_ttl": "87600h"},
		}); err != nil {
			return err
		}
	}
	if pem, _, err := v.do(ctx, http.MethodGet, "/v1/pki/ca/pem", nil, false); err != nil || len(bytes.TrimSpace(pem)) == 0 {
		if _, err := v.call(ctx, http.MethodPost, "/v1/pki/root/generate/internal", map[string]any{
			"common_name": "VoltSight Device Root CA", "issuer_name": "voltsight-device-root",
			"key_type": "ec", "key_bits": 256, "ttl": "87600h",
		}); err != nil {
			return err
		}
	}
	if _, err := v.call(ctx, http.MethodPost, "/v1/pki/config/urls", map[string]any{
		"issuing_certificates":    []string{v.Addr + "/v1/pki/ca"},
		"crl_distribution_points": []string{v.Addr + "/v1/pki/crl"},
	}); err != nil {
		return err
	}
	if _, err := v.call(ctx, http.MethodPost, "/v1/pki/config/crl", map[string]any{
		"auto_rebuild": true, "expiry": "72h", "disable": false,
	}); err != nil {
		return err
	}
	_, err = v.call(ctx, http.MethodPost, "/v1/pki/roles/"+Role, map[string]any{
		"allowed_domains": []string{"connector.voltsight.internal"}, "allow_subdomains": true,
		"allowed_uri_sans":  []string{"spiffe://voltsight/tenant/*/oem/*"},
		"enforce_hostnames": false, "key_type": "ec", "key_bits": 256,
		"server_flag": false, "client_flag": true, "ext_key_usage": []string{"ClientAuth"},
		"ttl": "720h", "max_ttl": fmt.Sprintf("%dh", int(MaxTTL.Hours())), "require_cn": true,
		"no_store": false, "generate_lease": false,
	})
	if err != nil {
		return err
	}
	// server-only role for the ingest gateway's own certificate
	_, err = v.call(ctx, http.MethodPost, "/v1/pki/roles/"+ServerRole, map[string]any{
		"allowed_domains": []string{"localhost", "voltsight.internal"}, "allow_subdomains": true, "allow_localhost": true,
		"allow_ip_sans": true, "enforce_hostnames": true, "key_type": "ec", "key_bits": 256,
		"server_flag": true, "client_flag": false, "ext_key_usage": []string{"ServerAuth"},
		"ttl": "720h", "max_ttl": "2160h", "require_cn": true,
	})
	return err
}

// ServerRole is the Vault PKI role for server certificates.
const ServerRole = "gateway-server"

// IssueServer issues a server-auth certificate for the given common name and extra SANs (DNS names and IPs).
func (v *Vault) IssueServer(ctx context.Context, cn string, altNames, ipSANs []string, ttl time.Duration) (*Issued, error) {
	data, err := v.call(ctx, http.MethodPost, "/v1/pki/issue/"+ServerRole, map[string]any{
		"common_name": cn, "alt_names": strings.Join(altNames, ","), "ip_sans": strings.Join(ipSANs, ","),
		"ttl": fmt.Sprintf("%ds", int(ttl.Seconds())),
	})
	if err != nil {
		return nil, err
	}
	s := func(k string) string { x, _ := data[k].(string); return x }
	blk, _ := pem.Decode([]byte(s("certificate")))
	if blk == nil {
		return nil, errors.New("vault returned no certificate")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, err
	}
	return &Issued{CertPEM: s("certificate"), KeyPEM: s("private_key"), CAPEM: s("issuing_ca"), Serial: s("serial_number"), Cert: cert}, nil
}

// Issued is a freshly issued connector certificate.
type Issued struct {
	CertPEM, KeyPEM, CAPEM string
	Serial                 string
	Cert                   *x509.Certificate
}

// Issue creates a client certificate for the connector identity with the requested lifetime.
func (v *Vault) Issue(ctx context.Context, id Identity, ttl time.Duration) (*Issued, error) {
	data, err := v.call(ctx, http.MethodPost, "/v1/pki/issue/"+Role, map[string]any{
		"common_name": fmt.Sprintf("%s.%s.connector.voltsight.internal", strings.ToLower(id.OEM), id.TenantID),
		"uri_sans":    id.URI(),
		"ttl":         fmt.Sprintf("%ds", int(ttl.Seconds())),
	})
	if err != nil {
		return nil, err
	}
	s := func(k string) string { x, _ := data[k].(string); return x }
	blk, _ := pem.Decode([]byte(s("certificate")))
	if blk == nil {
		return nil, errors.New("vault returned no certificate")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, err
	}
	return &Issued{CertPEM: s("certificate"), KeyPEM: s("private_key"), CAPEM: s("issuing_ca"), Serial: s("serial_number"), Cert: cert}, nil
}

// Revoke revokes a certificate by its serial number and rebuilds the CRL so the revocation is
// visible to verifiers immediately instead of at the next scheduled rebuild.
func (v *Vault) Revoke(ctx context.Context, serial string) error {
	if _, err := v.call(ctx, http.MethodPost, "/v1/pki/revoke", map[string]any{"serial_number": serial}); err != nil {
		return err
	}
	_, err := v.call(ctx, http.MethodGet, "/v1/pki/crl/rotate", nil)
	return err
}

// RootPool downloads the CA certificate (unauthenticated endpoint) into a pool.
func (v *Vault) RootPool(ctx context.Context) (*x509.CertPool, error) {
	out, code, err := v.do(ctx, http.MethodGet, "/v1/pki/ca/pem", nil, false)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if code != 200 || !pool.AppendCertsFromPEM(out) {
		return nil, fmt.Errorf("vault CA unavailable (HTTP %d)", code)
	}
	return pool, nil
}

// CRL downloads and parses the current revocation list (unauthenticated endpoint).
func (v *Vault) CRL(ctx context.Context) (*x509.RevocationList, error) {
	out, code, err := v.do(ctx, http.MethodGet, "/v1/pki/crl/pem", nil, false)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(out)
	if code != 200 || blk == nil {
		return nil, fmt.Errorf("vault CRL unavailable (HTTP %d)", code)
	}
	return x509.ParseRevocationList(blk.Bytes)
}
