// Package realm generates the Keycloak realm export for the platform from the same identities as the
// database seed, so user IDs (Keycloak subject == app_user.id) and tenant IDs cannot drift.
//
// Secrets are never written to the file: passwords and the test-client secret are ${ENV} placeholders
// that Keycloak substitutes from its container environment at import time.
package realm

import (
	"encoding/json"

	"voltsight/internal/seedgen"
)

// Name is the realm name; WebClient, APIClient and TestClient are its clients.
const (
	Name       = "voltsight"
	WebClient  = "voltsight-web"
	APIClient  = "voltsight-api"
	TestClient = "voltsight-test"
)

type obj = map[string]any

func mapper(name, typ string, cfg obj) obj {
	cfg["access.token.claim"] = "true"
	return obj{"name": name, "protocol": "openid-connect", "protocolMapper": typ, "consentRequired": false, "config": cfg}
}

// PublicURLPlaceholder is substituted by Keycloak at import time from the PUBLIC_URL container variable.
const PublicURLPlaceholder = "${PUBLIC_URL}"

// Build returns the development realm representation (localhost redirects, DEV-only test client).
func Build() obj { return build(false) }

// BuildPublic returns the realm for an internet-facing deployment: the only redirect URI and web origin is the
// deployment's own public URL (substituted from PUBLIC_URL at import) and the DEV-only direct-grant test client
// is omitted, so there is no password grant and no client secret in the realm at all.
func BuildPublic() obj { return build(true) }

func build(public bool) obj {
	w, err := seedgen.Generate(seedgen.Config{Seed: 1, Vehicles: 1}) // identities are seed-independent
	if err != nil {
		panic(err)
	}
	var roles []obj
	for _, r := range w.Roles {
		roles = append(roles, obj{"name": r})
	}
	tenantName := map[string]string{}
	tenantID := map[string]string{}
	for _, t := range w.Tenants {
		tenantID[t.ID.String()] = t.ID.String()
		tenantName[t.ID.String()] = t.Name
	}

	var users []obj
	for _, u := range w.Users {
		users = append(users, obj{
			"id": u.ID.String(), "username": u.Email, "email": u.Email, "emailVerified": true, "enabled": true,
			"firstName": tenantName[u.TenantID.String()], "lastName": u.Role,
			"attributes":  obj{"tenant_id": []string{u.TenantID.String()}},
			"credentials": []obj{{"type": "password", "value": "${DEMO_USER_PASSWORD}", "temporary": false}},
			"realmRoles":  []string{u.Role},
			// the standard self-service roles: users may manage their own profile, which is exactly the
			// channel an attacker would use to try to rewrite tenant_id (see the forgery test)
			"clientRoles": obj{"account": []string{"view-profile", "manage-account"}},
		})
	}

	// The three claims the API relies on. They are attached to the clients directly: a realm file that
	// defines its own clientScopes makes Keycloak skip its built-in scopes (basic/sub, roles, profile...).
	platformMappers := func() []obj {
		return []obj{
			mapper("tenant_id", "oidc-usermodel-attribute-mapper", obj{
				"user.attribute": "tenant_id", "claim.name": "tenant_id", "jsonType.label": "String", "id.token.claim": "true"}),
			mapper("roles", "oidc-usermodel-realm-role-mapper", obj{
				"claim.name": "roles", "multivalued": "true", "jsonType.label": "String", "id.token.claim": "false"}),
			mapper("audience", "oidc-audience-mapper", obj{"included.custom.audience": APIClient, "id.token.claim": "false"}),
		}
	}
	builtinScopes := []string{"basic", "profile", "email", "roles", "web-origins"}

	redirects, origins := []string{"http://localhost:5173/*", "http://localhost:8081/*"}, []string{"http://localhost:5173", "http://localhost:8081"}
	if public {
		redirects, origins = []string{PublicURLPlaceholder + "/*"}, []string{PublicURLPlaceholder}
	}
	clients := []obj{
		{
			"clientId": WebClient, "name": "VoltSight web", "enabled": true, "publicClient": true,
			"standardFlowEnabled": true, "directAccessGrantsEnabled": false, "implicitFlowEnabled": false,
			"serviceAccountsEnabled": false, "protocol": "openid-connect",
			"redirectUris": redirects, "webOrigins": origins,
			"attributes":          obj{"pkce.code.challenge.method": "S256", "post.logout.redirect.uris": "+"},
			"defaultClientScopes": builtinScopes, "protocolMappers": platformMappers(),
		},
		{
			"clientId": APIClient, "name": "VoltSight API (resource server)", "enabled": true, "bearerOnly": true,
			"standardFlowEnabled": false, "directAccessGrantsEnabled": false, "protocol": "openid-connect",
		},
	}
	if !public {
		clients = append(clients, obj{
			// DEV/TEST ONLY: lets automated tests obtain tokens with the demo users' passwords.
			// Production realms must omit this client (see docs/security/STRIDE.md).
			"clientId": TestClient, "name": "VoltSight test client (DEV ONLY)", "enabled": true,
			"publicClient": false, "secret": "${KC_TEST_CLIENT_SECRET}", "standardFlowEnabled": false,
			"directAccessGrantsEnabled": true, "implicitFlowEnabled": false, "serviceAccountsEnabled": false,
			"protocol": "openid-connect", "defaultClientScopes": builtinScopes,
			// extra audience so tests can call the Account REST API as the user (attack simulation)
			"protocolMappers": append(platformMappers(), mapper("account-audience", "oidc-audience-mapper",
				obj{"included.client.audience": "account", "id.token.claim": "false"})),
		})
	}

	return obj{
		"realm": Name, "enabled": true, "displayName": "VoltSight",
		"sslRequired": "external", "registrationAllowed": false, "resetPasswordAllowed": false,
		"bruteForceProtected": true, "failureFactor": 5, "maxFailureWaitSeconds": 900,
		"accessTokenLifespan": 300, "ssoSessionIdleTimeout": 1800, "ssoSessionMaxLifespan": 36000,
		"defaultSignatureAlgorithm": "RS256",
		// tenant_id is an unmanaged attribute that only administrators may edit or view: a user cannot
		// forge their own tenant claim through the account console or the account API.
		"attributes": obj{"unmanagedAttributePolicy": "ADMIN_EDIT"},
		"roles":      obj{"realm": roles},
		"clients":    clients,
		"users":      users,
	}
}

// JSON returns the indented development realm document with a trailing newline.
func JSON() ([]byte, error) { return marshal(Build()) }

// PublicJSON returns the indented public-deployment realm document with a trailing newline.
func PublicJSON() ([]byte, error) { return marshal(BuildPublic()) }

func marshal(r obj) ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
