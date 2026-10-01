package dbtool

import (
	"fmt"
	"net/url"

	"voltsight/internal/dotenv"
)

// OwnerDSN builds the migration-owner connection string for the local compose stack, unless
// DATABASE_URL is set.
func OwnerDSN(env map[string]string) (string, error) {
	if u := dotenv.Get(env, "DATABASE_URL"); u != "" {
		return u, nil
	}
	pw := dotenv.Get(env, "POSTGRES_PASSWORD")
	if pw == "" {
		return "", fmt.Errorf("POSTGRES_PASSWORD not set (run `make env`)")
	}
	host := dotenv.Get(env, "POSTGRES_HOST")
	if host == "" {
		host = "127.0.0.1" // not "localhost": that can resolve to ::1, where a native service may answer
	}
	port := dotenv.Get(env, "POSTGRES_PORT")
	if port == "" {
		port = "55432" // host port published by deploy/compose
	}
	// url.UserPassword escapes for the userinfo component (a space must be %20, not the query-style '+')
	u := url.URL{Scheme: "postgres", User: url.UserPassword("voltsight", pw), Host: host + ":" + port,
		Path: "/voltsight", RawQuery: "sslmode=disable"}
	return u.String(), nil
}
