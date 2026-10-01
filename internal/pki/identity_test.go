package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert, key, pool}
}

func (ca *testCA) issue(t *testing.T, serial int64, uris []string, eku []x509.ExtKeyUsage, nb, na time.Time) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var us []*url.URL
	for _, s := range uris {
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		us = append(us, u)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "connector"},
		NotBefore: nb, NotAfter: na, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: eku, URIs: us,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

var tenant = uuid.MustParse("11111111-2222-4333-8444-555555555555")

func TestParseIdentity(t *testing.T) {
	ca := newCA(t, "ca")
	now := time.Now()
	good := Identity{TenantID: tenant, OEM: "AURORA"}
	c := ca.issue(t, 2, []string{good.URI()}, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-time.Minute), now.Add(time.Hour))
	id, err := ParseIdentity(c)
	if err != nil || id != good {
		t.Fatalf("ParseIdentity = %+v, %v; want %+v", id, err, good)
	}

	bad := map[string][]string{
		"no uri":             nil,
		"foreign scheme":     {"https://voltsight/tenant/" + tenant.String() + "/oem/AURORA"},
		"foreign host":       {"spiffe://evil/tenant/" + tenant.String() + "/oem/AURORA"},
		"bad uuid":           {"spiffe://voltsight/tenant/not-a-uuid/oem/AURORA"},
		"missing oem":        {"spiffe://voltsight/tenant/" + tenant.String() + "/oem/"},
		"extra path":         {"spiffe://voltsight/tenant/" + tenant.String() + "/oem/AURORA/x"},
		"wrong keys":         {"spiffe://voltsight/org/" + tenant.String() + "/oem/AURORA"},
		"two identities":     {good.URI(), Identity{TenantID: uuid.New(), OEM: "NIMBUS"}.URI()},
		"identity+unrelated": {"https://example.test/x"},
	}
	for name, uris := range bad {
		cert := ca.issue(t, 3, uris, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-time.Minute), now.Add(time.Hour))
		if _, err := ParseIdentity(cert); !errors.Is(err, ErrNoIdentity) {
			t.Errorf("%s: expected ErrNoIdentity, got %v", name, err)
		}
	}
}

func TestVerify(t *testing.T) {
	ca, other := newCA(t, "ca"), newCA(t, "other-ca")
	now := time.Now()
	id := Identity{TenantID: tenant, OEM: "NIMBUS"}
	client := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}

	ok := ca.issue(t, 10, []string{id.URI()}, client, now.Add(-time.Minute), now.Add(time.Hour))
	got, err := Verify(ok, ca.pool, nil, now)
	if err != nil || got != id {
		t.Fatalf("valid certificate rejected: %v %+v", err, got)
	}

	t.Run("wrong CA", func(t *testing.T) {
		if _, err := Verify(ok, other.pool, nil, now); err == nil {
			t.Fatal("certificate from an unrelated CA must be rejected")
		}
	})
	t.Run("expired", func(t *testing.T) {
		exp := ca.issue(t, 11, []string{id.URI()}, client, now.Add(-2*time.Hour), now.Add(-time.Hour))
		if _, err := Verify(exp, ca.pool, nil, now); err == nil {
			t.Fatal("expired certificate must be rejected")
		}
	})
	t.Run("not yet valid", func(t *testing.T) {
		future := ca.issue(t, 12, []string{id.URI()}, client, now.Add(time.Hour), now.Add(2*time.Hour))
		if _, err := Verify(future, ca.pool, nil, now); err == nil {
			t.Fatal("not-yet-valid certificate must be rejected")
		}
	})
	t.Run("server-auth only", func(t *testing.T) {
		srv := ca.issue(t, 13, []string{id.URI()}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now.Add(-time.Minute), now.Add(time.Hour))
		if _, err := Verify(srv, ca.pool, nil, now); err == nil {
			t.Fatal("a certificate without client-auth usage must be rejected")
		}
	})
	t.Run("no identity", func(t *testing.T) {
		anon := ca.issue(t, 14, nil, client, now.Add(-time.Minute), now.Add(time.Hour))
		if _, err := Verify(anon, ca.pool, nil, now); !errors.Is(err, ErrNoIdentity) {
			t.Fatalf("want ErrNoIdentity, got %v", err)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		revoked := ca.issue(t, 15, []string{id.URI()}, client, now.Add(-time.Minute), now.Add(time.Hour))
		crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
			Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour),
			RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: big.NewInt(15), RevocationTime: now}},
		}, ca.cert, ca.key)
		if err != nil {
			t.Fatal(err)
		}
		crl, err := x509.ParseRevocationList(crlDER)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(revoked, ca.pool, crl, now); !errors.Is(err, ErrRevoked) {
			t.Fatalf("want ErrRevoked, got %v", err)
		}
		if _, err := Verify(ok, ca.pool, crl, now); err != nil {
			t.Fatalf("a non-revoked certificate must still pass with the same CRL: %v", err)
		}
		if !IsRevoked(crl, big.NewInt(15)) || IsRevoked(crl, big.NewInt(10)) {
			t.Fatal("IsRevoked wrong")
		}
	})
}

func TestSerialFromHex(t *testing.T) {
	n, err := SerialFromHex("1a:2b:3c")
	if err != nil || n.Cmp(big.NewInt(0x1a2b3c)) != 0 {
		t.Fatalf("got %v %v", n, err)
	}
	if n, err := SerialFromHex("ff"); err != nil || n.Int64() != 255 {
		t.Fatalf("plain hex: %v %v", n, err)
	}
	if _, err := SerialFromHex("zz:11"); err == nil {
		t.Fatal("expected error for non-hex serial")
	}
}
