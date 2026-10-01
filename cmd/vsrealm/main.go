// Command vsrealm writes the generated Keycloak realm export (deploy/compose/config/keycloak).
package main

import (
	"fmt"
	"os"

	"voltsight/internal/realm"
)

func main() {
	out := "deploy/compose/config/keycloak/voltsight-realm.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	b, err := realm.JSON()
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
