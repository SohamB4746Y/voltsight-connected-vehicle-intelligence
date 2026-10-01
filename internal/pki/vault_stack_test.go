//go:build stack

package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"

	"voltsight/internal/dotenv"
)

// These tests run against the real Vault of the compose stack (`make up`). G1.9.

func stackVault(t *testing.T) *Vault {
	t.Helper()
	env, err := dotenv.Load("../../.env")
	if err != nil {
		t.Fatal(err)
	}
	token := dotenv.Get(env, "VAULT_DEV_TOKEN")
	if token == "" {
		t.Fatal("VAULT_DEV_TOKEN missing; run `make env && make up`")
	}
	addr := dotenv.Get(env, "VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	v := NewVault(addr, token)
	if err := v.Bootstrap(context.Background()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return v
}

func TestStack_BootstrapIsIdempotent(t *testing.T) {
	v := stackVault(t)
	ctx := context.Background()
	if err := v.Bootstrap(ctx); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if _, err := v.RootPool(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStack_IssueVerifyRevoke(t *testing.T) {
	v := stackVault(t)
	ctx := context.Background()
	id := Identity{TenantID: uuid.New(), OEM: "AURORA"}

	iss, err := v.Issue(ctx, id, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	c := iss.Cert
	if got, err := ParseIdentity(c); err != nil || got != id {
		t.Fatalf("identity in issued cert = %+v, %v; want %+v", got, err, id)
	}
	hasClient := false
	for _, e := range c.ExtKeyUsage {
		if e == x509.ExtKeyUsageServerAuth {
			t.Fatal("connector certificate must not be valid for server authentication")
		}
		hasClient = hasClient || e == x509.ExtKeyUsageClientAuth
	}
	if !hasClient {
		t.Fatal("connector certificate lacks client-auth usage")
	}
	if c.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("expected an ECDSA key, got %v", c.PublicKeyAlgorithm)
	}
	if iss.KeyPEM == "" {
		t.Fatal("no private key returned")
	}

	roots, err := v.RootPool(ctx)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := v.CRL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Verify(c, roots, crl, time.Now()); err != nil || got != id {
		t.Fatalf("freshly issued certificate must verify: %+v %v", got, err)
	}

	// certificate from an unrelated CA is rejected against Vault's root
	rogueKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rogueTmpl := &x509.Certificate{SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "rogue"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	rogueTmpl.URIs = c.URIs
	der, err := x509.CreateCertificate(rand.Reader, rogueTmpl, rogueTmpl, &rogueKey.PublicKey, rogueKey)
	if err != nil {
		t.Fatal(err)
	}
	rogue, _ := x509.ParseCertificate(der)
	if _, err := Verify(rogue, roots, crl, time.Now()); err == nil {
		t.Fatal("self-signed look-alike certificate was accepted")
	}

	// revoke: the serial must appear in the CRL and Verify must now reject it
	if err := v.Revoke(ctx, iss.Serial); err != nil {
		t.Fatal(err)
	}
	crl2, err := v.CRL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !IsRevoked(crl2, c.SerialNumber) {
		t.Fatalf("serial %s not in CRL after revoke", iss.Serial)
	}
	if _, err := Verify(c, roots, crl2, time.Now()); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked certificate must be rejected, got %v", err)
	}
	// ... while a different, valid certificate is unaffected
	other, err := v.Issue(ctx, Identity{TenantID: uuid.New(), OEM: "NIMBUS"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(other.Cert, roots, crl2, time.Now()); err != nil {
		t.Fatalf("unrelated valid certificate rejected after a revocation: %v", err)
	}
}

func TestStack_ExpiredCertificateIsRejected(t *testing.T) {
	v := stackVault(t)
	ctx := context.Background()
	iss, err := v.Issue(ctx, Identity{TenantID: uuid.New(), OEM: "ZEPHYR"}, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	roots, _ := v.RootPool(ctx)
	if _, err := Verify(iss.Cert, roots, nil, time.Now()); err != nil {
		t.Fatalf("certificate should be valid right after issue: %v", err)
	}
	time.Sleep(time.Until(iss.Cert.NotAfter) + time.Second)
	if _, err := Verify(iss.Cert, roots, nil, time.Now()); err == nil {
		t.Fatal("expired certificate was accepted")
	}
}

func TestStack_LifetimeIsCappedByRole(t *testing.T) {
	v := stackVault(t)
	iss, err := v.Issue(context.Background(), Identity{TenantID: uuid.New(), OEM: "ORION"}, 10*365*24*time.Hour)
	if err != nil {
		return // refusing an over-long request is also acceptable
	}
	if life := iss.Cert.NotAfter.Sub(iss.Cert.NotBefore); life > MaxTTL+time.Minute {
		t.Fatalf("lifetime %s exceeds the role maximum %s", life, MaxTTL)
	}
}

func TestStack_RejectsIdentitiesOutsideTheRole(t *testing.T) {
	v := stackVault(t)
	ctx := context.Background()
	// a URI SAN that does not match spiffe://voltsight/tenant/*/oem/* must be refused by the role
	_, err := v.call(ctx, "POST", "/v1/pki/issue/"+Role, map[string]any{
		"common_name": "aurora.x.connector.voltsight.internal", "uri_sans": "spiffe://evil/tenant/x/oem/AURORA", "ttl": "1h"})
	if err == nil {
		t.Fatal("role issued a certificate with a foreign URI SAN")
	}
	// a common name outside the allowed domain must be refused
	_, err = v.call(ctx, "POST", "/v1/pki/issue/"+Role, map[string]any{"common_name": "evil.example.com", "ttl": "1h"})
	if err == nil {
		t.Fatal("role issued a certificate for a foreign common name")
	}
}
