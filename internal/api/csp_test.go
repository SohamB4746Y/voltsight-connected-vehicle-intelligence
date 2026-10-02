package api

import (
	"strings"
	"testing"
)

// The console's policy must allow exactly what the map needs and nothing broader.
func TestContentSecurityPolicyAllowsTheMapAndNothingBroader(t *testing.T) {
	csp := contentSecurityPolicy()
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'self' https://tiles.openfreemap.org", "worker-src 'self' blob:",
		"object-src 'none'", "frame-ancestors 'none'", "form-action 'self'", "base-uri 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lacks %q: %s", want, csp)
		}
	}
	for _, bad := range []string{"'unsafe-eval'", "script-src 'self' 'unsafe-inline'", " * ", "localhost"} {
		if strings.Contains(csp, bad) {
			t.Errorf("policy must not contain %q: %s", bad, csp)
		}
	}
}
