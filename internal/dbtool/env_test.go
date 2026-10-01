package dbtool

import (
	"net/url"
	"strings"
	"testing"
)

func TestOwnerDSN(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("POSTGRES_PASSWORD", "")
	t.Setenv("POSTGRES_HOST", "")
	t.Setenv("POSTGRES_PORT", "")

	if _, err := OwnerDSN(map[string]string{}); err == nil {
		t.Fatal("a missing password must be an error, not a passwordless DSN")
	}

	// defaults: IPv4 loopback (not "localhost", which may resolve to ::1 where a native Postgres can answer)
	dsn, err := OwnerDSN(map[string]string{"POSTGRES_PASSWORD": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "127.0.0.1" || u.Port() != "55432" || u.User.Username() != "voltsight" || u.Path != "/voltsight" {
		t.Fatalf("unexpected default DSN: %s", dsn)
	}

	// special characters in the password survive URL parsing
	nasty := `p@ss:w/ord?#%&=+ space`
	dsn, _ = OwnerDSN(map[string]string{"POSTGRES_PASSWORD": nasty})
	u, err = url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := u.User.Password(); got != nasty {
		t.Fatalf("password mangled: %q", got)
	}

	// explicit host/port, and the environment beats the map
	t.Setenv("POSTGRES_HOST", "db.internal")
	dsn, _ = OwnerDSN(map[string]string{"POSTGRES_PASSWORD": "x", "POSTGRES_PORT": "6543"})
	if !strings.Contains(dsn, "@db.internal:6543/") {
		t.Fatalf("host/port not honoured: %s", dsn)
	}

	// DATABASE_URL wins over everything
	t.Setenv("DATABASE_URL", "postgres://u:p@h:1/d")
	if dsn, _ := OwnerDSN(map[string]string{}); dsn != "postgres://u:p@h:1/d" {
		t.Fatalf("DATABASE_URL ignored: %s", dsn)
	}
}
