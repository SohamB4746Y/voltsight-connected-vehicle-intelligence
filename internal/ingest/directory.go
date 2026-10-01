// Package ingest is the device-facing gateway: mTLS connector authentication, per-event validation and
// normalisation, and durable hand-off to Kafka.
package ingest

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Credential is a registered connector certificate.
type Credential struct {
	Tenant uuid.UUID
	OEM    string
	Active bool
}

// Directory is the gateway's read-only view of who may send what.
type Directory interface {
	// Vehicles maps VIN -> owning tenant.
	Vehicles(ctx context.Context) (map[string]uuid.UUID, error)
	// Credentials maps the hex SHA-256 fingerprint of a connector certificate to its registration.
	Credentials(ctx context.Context) (map[string]Credential, error)
	// Dialects maps OEM code -> JSON dialect ('A' or 'B').
	Dialects(ctx context.Context) (map[string]byte, error)
}

// PGDirectory reads the directory from PostgreSQL using the least-privilege voltsight_gateway role.
type PGDirectory struct{ Pool *pgxpool.Pool }

// Vehicles implements Directory.
func (d PGDirectory) Vehicles(ctx context.Context) (map[string]uuid.UUID, error) {
	rows, err := d.Pool.Query(ctx, `SELECT trim(vin), tenant_id FROM vehicle`)
	if err != nil {
		return nil, fmt.Errorf("load vehicles: %w", err)
	}
	defer rows.Close()
	out := make(map[string]uuid.UUID, 110_000)
	for rows.Next() {
		var vin string
		var t uuid.UUID
		if err := rows.Scan(&vin, &t); err != nil {
			return nil, err
		}
		out[vin] = t
	}
	return out, rows.Err()
}

// Credentials implements Directory.
func (d PGDirectory) Credentials(ctx context.Context) (map[string]Credential, error) {
	rows, err := d.Pool.Query(ctx, `SELECT trim(cert_fingerprint), tenant_id, oem_code, status = 'active' AND not_after > now() FROM device_credential`)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	defer rows.Close()
	out := map[string]Credential{}
	for rows.Next() {
		var fp string
		var c Credential
		if err := rows.Scan(&fp, &c.Tenant, &c.OEM, &c.Active); err != nil {
			return nil, err
		}
		out[fp] = c
	}
	return out, rows.Err()
}

// Dialects implements Directory.
func (d PGDirectory) Dialects(ctx context.Context) (map[string]byte, error) {
	rows, err := d.Pool.Query(ctx, `SELECT code, dialect FROM oem`)
	if err != nil {
		return nil, fmt.Errorf("load oems: %w", err)
	}
	defer rows.Close()
	out := map[string]byte{}
	for rows.Next() {
		var code, dialect string
		if err := rows.Scan(&code, &dialect); err != nil {
			return nil, err
		}
		out[code] = dialect[0]
	}
	return out, rows.Err()
}
