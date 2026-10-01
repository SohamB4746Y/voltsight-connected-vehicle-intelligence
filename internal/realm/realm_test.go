package realm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"voltsight/internal/seedgen"
)

// The committed realm file must be exactly what the generator produces (no hand edits, no drift
// from the seed identities).
func TestCommittedRealmMatchesGenerator(t *testing.T) {
	want, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../deploy/compose/config/keycloak/voltsight-realm.json")
	if err != nil {
		t.Fatalf("realm file missing; run `go run ./cmd/vsrealm`: %v", err)
	}
	if string(got) != string(want) {
		t.Fatal("deploy/compose/config/keycloak/voltsight-realm.json is stale; run `go run ./cmd/vsrealm`")
	}
}

func TestRealmContainsNoSecretsAndIsSafelyConfigured(t *testing.T) {
	b, _ := JSON()
	s := string(b)
	// every credential value and the client secret must be an ${ENV} placeholder
	var doc struct {
		Users []struct {
			Credentials []struct{ Value string }
		}
		Clients []struct {
			ClientID                  string
			Secret                    string
			PublicClient              bool
			DirectAccessGrantsEnabled bool
			ImplicitFlowEnabled       bool
			BearerOnly                bool
			Attributes                map[string]string
		}
		Attributes map[string]string
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Users) != 9 {
		t.Fatalf("expected 9 demo users, got %d", len(doc.Users))
	}
	for _, u := range doc.Users {
		for _, c := range u.Credentials {
			if !strings.HasPrefix(c.Value, "${") {
				t.Fatalf("a user password is not an environment placeholder: %q", c.Value)
			}
		}
	}
	for _, c := range doc.Clients {
		if c.Secret != "" && !strings.HasPrefix(c.Secret, "${") {
			t.Fatalf("client %s secret is not a placeholder", c.ClientID)
		}
		if c.ImplicitFlowEnabled {
			t.Fatalf("client %s enables the implicit flow", c.ClientID)
		}
		switch c.ClientID {
		case WebClient:
			if !c.PublicClient || c.DirectAccessGrantsEnabled || c.Attributes["pkce.code.challenge.method"] != "S256" {
				t.Fatalf("web client must be public, PKCE-S256, without direct grants: %+v", c)
			}
		case APIClient:
			if !c.BearerOnly {
				t.Fatal("API client must be bearer-only")
			}
		}
	}
	if doc.Attributes["unmanagedAttributePolicy"] != "ADMIN_EDIT" {
		t.Fatal("tenant_id must be admin-only editable")
	}
	if strings.Contains(s, "DEMO_USER_PASSWORD") == false || strings.Contains(s, "KC_TEST_CLIENT_SECRET") == false {
		t.Fatal("expected placeholders for the demo password and the test-client secret")
	}
}

func TestUsersMatchSeedIdentities(t *testing.T) {
	w, err := seedgen.Generate(seedgen.Config{Seed: 1, Vehicles: 10})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := JSON()
	var doc struct {
		Users []struct {
			ID         string
			Username   string
			Attributes map[string][]string
			RealmRoles []string
		}
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Users) != len(w.Users) {
		t.Fatalf("realm has %d users, seed has %d", len(doc.Users), len(w.Users))
	}
	for i, u := range w.Users {
		r := doc.Users[i]
		if r.ID != u.ID.String() || r.Username != u.Email || r.Attributes["tenant_id"][0] != u.TenantID.String() || r.RealmRoles[0] != u.Role {
			t.Fatalf("user %s differs between realm and seed: %+v", u.Email, r)
		}
	}
}
