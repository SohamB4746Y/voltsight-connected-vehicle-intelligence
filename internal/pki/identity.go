// Package pki issues and verifies the mTLS client certificates of OEM-cloud connectors.
//
// One certificate identifies one connector (tenant x OEM). The identity is a URI SAN of the form
//
//	spiffe://voltsight/tenant/<tenant-uuid>/oem/<OEM_CODE>
//
// Certificates are issued by a Vault PKI engine (see Vault); verification here is independent of
// Vault so the gateway can validate peers without a runtime dependency on it.
package pki

import (
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const spiffeScheme, spiffeHost = "spiffe", "voltsight"

// Identity is the connector identity carried by a certificate.
type Identity struct {
	TenantID uuid.UUID
	OEM      string
}

// URI renders the SAN URI for an identity.
func (i Identity) URI() string {
	return fmt.Sprintf("%s://%s/tenant/%s/oem/%s", spiffeScheme, spiffeHost, i.TenantID, i.OEM)
}

// Errors returned by identity parsing and verification.
var (
	ErrNoIdentity = errors.New("pki: certificate carries no VoltSight connector identity")
	ErrRevoked    = errors.New("pki: certificate is revoked")
)

// ParseIdentity extracts the connector identity from a certificate's URI SANs. A certificate with
// zero or more than one VoltSight identity is rejected.
func ParseIdentity(cert *x509.Certificate) (Identity, error) {
	var found []Identity
	for _, u := range cert.URIs {
		if id, ok := parseURI(u); ok {
			found = append(found, id)
		}
	}
	if len(found) != 1 {
		return Identity{}, fmt.Errorf("%w (found %d)", ErrNoIdentity, len(found))
	}
	return found[0], nil
}

func parseURI(u *url.URL) (Identity, bool) {
	if u.Scheme != spiffeScheme || u.Host != spiffeHost {
		return Identity{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "tenant" || parts[2] != "oem" || parts[3] == "" {
		return Identity{}, false
	}
	tid, err := uuid.Parse(parts[1])
	if err != nil {
		return Identity{}, false
	}
	return Identity{TenantID: tid, OEM: parts[3]}, true
}

// Verify checks that cert chains to roots with client-auth usage at time `now`, carries exactly one
// connector identity, and is not listed in crl (nil crl skips the revocation check; callers that
// enforce revocation must pass one).
func Verify(cert *x509.Certificate, roots *x509.CertPool, crl *x509.RevocationList, now time.Time) (Identity, error) {
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return Identity{}, fmt.Errorf("pki: chain verification failed: %w", err)
	}
	id, err := ParseIdentity(cert)
	if err != nil {
		return Identity{}, err
	}
	if crl != nil && IsRevoked(crl, cert.SerialNumber) {
		return Identity{}, ErrRevoked
	}
	return id, nil
}

// IsRevoked reports whether serial appears in the CRL.
func IsRevoked(crl *x509.RevocationList, serial *big.Int) bool {
	for _, e := range crl.RevokedCertificateEntries {
		if e.SerialNumber.Cmp(serial) == 0 {
			return true
		}
	}
	return false
}

// SerialFromHex parses Vault's colon-separated hexadecimal serial ("1a:2b:...") or plain hex.
func SerialFromHex(s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(strings.ReplaceAll(s, ":", ""), 16)
	if !ok {
		return nil, fmt.Errorf("pki: bad serial %q", s)
	}
	return n, nil
}
