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
// it dumps EVERY registered platform's hints concatenated — so the
// agent gets the full reference in one shot without round-tripping
// "list names → pick one → fetch that one". With a name it prints
// just that platform's markdown verbatim, useful when the agent
// wants narrowly-scoped output.
func runHints(args []string) error {
	if len(args) == 0 {
		fmt.Println(hints.All())
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
