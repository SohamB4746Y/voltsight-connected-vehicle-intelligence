package ingest

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"voltsight/internal/pki"
	"voltsight/internal/sim"
)

// Register records a connector certificate in device_credential (the gateway only accepts registered,
// active fingerprints). Existing registrations of the same fingerprint are left untouched.
func Register(ctx context.Context, pool *pgxpool.Pool, id pki.Identity, c *pki.Issued) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO device_credential (tenant_id, oem_code, cert_serial, cert_fingerprint, not_before, not_after)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (cert_fingerprint) DO NOTHING`,
		id.TenantID, id.OEM, c.Serial, fingerprint(c.Cert), c.Cert.NotBefore, c.Cert.NotAfter)
	return err
}

// MarkRevoked flags a registered credential as revoked in the database.
func MarkRevoked(ctx context.Context, pool *pgxpool.Pool, serial string) error {
	_, err := pool.Exec(ctx, `UPDATE device_credential SET status = 'revoked', revoked_at = now() WHERE cert_serial = $1`, serial)
	return err
}

// Provision issues and registers one certificate per connector and returns ready-to-use TLS certificates.
func Provision(ctx context.Context, v *pki.Vault, pool *pgxpool.Pool, keys []sim.ConnectorKey, ttl time.Duration) (map[sim.ConnectorKey]tls.Certificate, error) {
	out := make(map[sim.ConnectorKey]tls.Certificate, len(keys))
	for _, k := range keys {
		id := pki.Identity{TenantID: k.Tenant, OEM: k.OEM}
		iss, err := v.Issue(ctx, id, ttl)
		if err != nil {
			return nil, fmt.Errorf("issue %s/%s: %w", k.Tenant, k.OEM, err)
		}
		if err := Register(ctx, pool, id, iss); err != nil {
			return nil, fmt.Errorf("register %s/%s: %w", k.Tenant, k.OEM, err)
		}
		cert, err := ParseKeyPair(iss)
		if err != nil {
			return nil, err
		}
		out[k] = cert
	}
	return out, nil
}
