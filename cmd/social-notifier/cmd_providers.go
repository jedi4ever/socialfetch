package main

// cmd_providers.go — `social-notifier providers list`. Prints the
// registered provider names one per line so it composes in
// shell pipelines (`social-notifier providers list | grep slack`).

import (
	"flag"
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/notifier"
)

func cmdProviders(args []string) error {
	fs := flag.NewFlagSet("providers", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		rest = []string{"list"}
	}
	switch rest[0] {
	case "list":
		for _, name := range notifier.Names() {
			fmt.Fprintln(os.Stdout, name)
		}
		return nil
	default:
		return fmt.Errorf("providers: unknown verb %q (try: list)", rest[0])
	}
}
