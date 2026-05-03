package main

// CLI entry point for the per-platform hints system. The actual
// catalog lives in internal/hints — kept there so the MCP layer can
// reuse the same registry without duplicating the platform-package
// imports.

import (
	"fmt"
	"strings"

	"github.com/jedi4ever/social-skills/internal/hints"
)

// runHints implements `social-fetch hints [name]`. With no argument
// it prints the registered platform names so an agent can see what
// docs are available. With a name it prints that platform's
// markdown verbatim so an agent's Read tool / the MCP envelope sees
// the same text the maintainer wrote.
func runHints(args []string) error {
	if len(args) == 0 {
		fmt.Println("Platforms with hints:")
		for _, n := range hints.Catalog() {
			fmt.Println("  " + n)
		}
		fmt.Println()
		fmt.Println("Run `social-fetch hints <name>` to read one.")
		return nil
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	md, err := hints.MustGet(name)
	if err != nil {
		return err
	}
	fmt.Println(md)
	return nil
}
