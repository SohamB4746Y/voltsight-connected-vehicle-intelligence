// Command vsrealm writes the generated Keycloak realm exports: the development realm (deploy/compose/config/keycloak)
// and, with -public, the internet-facing one (deploy/compose/config/keycloak-live).
package main

import (
	"fmt"
	"os"

	"voltsight/internal/realm"
)

func main() {
	out, gen := "deploy/compose/config/keycloak/voltsight-realm.json", realm.JSON
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-public" {
		out, gen, args = "deploy/compose/config/keycloak-live/voltsight-realm.json", realm.PublicJSON, args[1:]
	}
	if len(args) > 0 {
		out = args[0]
	}
	b, err := gen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vsrealm:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "vsrealm:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}
