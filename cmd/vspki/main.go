// Command vspki manages the connector-certificate PKI in Vault: bootstrap the CA and role, issue a
// connector certificate, revoke one.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"voltsight/internal/dotenv"
	"voltsight/internal/pki"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	env, err := dotenv.Load(".env")
	must(err)
	addr := dotenv.Get(env, "VAULT_ADDR")
	if addr == "" {
		addr = "http://127.0.0.1:8200"
	}
	token := dotenv.Get(env, "VAULT_DEV_TOKEN")
	if token == "" {
		must(fmt.Errorf("VAULT_DEV_TOKEN not set (run `make env`)"))
	}
	v := pki.NewVault(addr, token)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch os.Args[1] {
	case "bootstrap":
		must(v.Bootstrap(ctx))
		fmt.Println("vault PKI ready: root CA + role", pki.Role)
	case "issue":
		fs := flag.NewFlagSet("issue", flag.ExitOnError)
		tenant := fs.String("tenant", "", "tenant UUID (required)")
		oem := fs.String("oem", "", "OEM code, e.g. AURORA (required)")
		ttl := fs.Duration("ttl", 30*24*time.Hour, "certificate lifetime")
		out := fs.String("out", ".", "directory for <oem>-<tenant>.{crt,key} and ca.crt")
		must(fs.Parse(os.Args[2:]))
		tid, err := uuid.Parse(*tenant)
		must(err)
		if *oem == "" {
			must(fmt.Errorf("-oem is required"))
		}
		c, err := v.Issue(ctx, pki.Identity{TenantID: tid, OEM: *oem}, *ttl)
		must(err)
		base := filepath.Join(*out, fmt.Sprintf("%s-%s", *oem, tid))
		must(os.WriteFile(base+".crt", []byte(c.CertPEM), 0o644))
		must(os.WriteFile(base+".key", []byte(c.KeyPEM), 0o600))
		must(os.WriteFile(filepath.Join(*out, "ca.crt"), []byte(c.CAPEM), 0o644))
		fmt.Printf("issued serial=%s not_after=%s -> %s.{crt,key}\n", c.Serial, c.Cert.NotAfter.Format(time.RFC3339), base)
	case "revoke":
		fs := flag.NewFlagSet("revoke", flag.ExitOnError)
		serial := fs.String("serial", "", "certificate serial (required)")
		must(fs.Parse(os.Args[2:]))
		must(v.Revoke(ctx, *serial))
		fmt.Println("revoked", *serial)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vspki bootstrap | issue -tenant U -oem NAME [-ttl d -out dir] | revoke -serial S")
	os.Exit(2)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "vspki:", err)
		os.Exit(1)
	}
}
