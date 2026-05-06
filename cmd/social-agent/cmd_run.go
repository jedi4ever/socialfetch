package main

// cmd_run.go — `social-agent run "<prompt>"`. The one-shot path:
// up + exec(harness.InvokePrompt) + [if --output] pull /artifacts
// + down. Claude's response streams to stdout as it's generated;
// pulled artifacts land in --output DIR (default: skip pull).
//
// Same shared upFlags as `social-agent up` so flag surface is
// consistent. The pull happens inside Provider.Run when
// UpOpts.OutputDir is set — keeps the orchestration in the
// substrate where it belongs (each provider knows how to reach
// its in-container artifacts server).

import (
	"flag"
	"fmt"
	"strings"

	"github.com/jedi4ever/social-skills/internal/agent"
)

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	flags := &upFlags{}
	flags.attach(fs)
	output := fs.String("output", "", "destination dir for /artifacts pulled after the run; default: skip pull")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("run: <prompt> required (e.g. `social-agent run \"summarise README.md\"`)")
	}
	prompt := strings.Join(fs.Args(), " ")
	envMap, err := flags.resolveEnv()
	if err != nil {
		return err
	}
	prov, err := buildProvider(flags.provider)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()
	return prov.Run(ctx, agent.UpOpts{
		Image:     flags.resolveImage(),
		Harness:   flags.harness,
		Workdir:   flags.workdir,
		Name:      flags.name,
		Env:       envMap,
		OutputDir: *output,
	}, prompt)
}
