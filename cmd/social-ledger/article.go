package main

// CLI dispatcher for `social-ledger article` — every verb that
// operates on stored items (articles, tweets, HN posts, anything
// that came in via fetch / ingest). Grouped under `article` so
// the noun-first mental model stays consistent across entities:
// `social-ledger article add/get/list/...` lives next to
// `social-ledger influencer add/get/list/...`.
//
// Implementation: this dispatcher routes to the existing cmd*
// functions (cmdIngest, cmdGet, cmdList, etc.) — no behaviour
// change, just relocation. The cmd* functions stay where they
// are so we don't churn 400 lines of handler code; this file is
// the thin namespace.

import (
	"fmt"
	"strings"
)

func cmdArticle(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "add", "ingest":
		// "add" is the new canonical name; "ingest" stays as an
		// alias because operators (and the auto-ingest path in
		// social-fetch's ledger.go) pipe to it heavily.
		return cmdIngest(args)
	case "get":
		return cmdGet(args)
	case "list":
		return cmdList(args)
	case "search":
		return cmdSearch(args)
	case "seen":
		return cmdSeen(args)
	case "stats":
		return cmdStats(args)
	case "forget":
		return cmdForget(args)
	case "record":
		return cmdRecord(args)
	case "filter":
		return cmdFilter(args)
	}
	if sub == "-h" || sub == "--help" {
		printArticleHelp()
		return nil
	}
	return fmt.Errorf("article: unknown subcommand %q (try `article --help`)", sub)
}

func printArticleHelp() {
	fmt.Print(`social-ledger article — operations on stored content items

Usage:
  social-ledger article add        ingest items (JSONL on stdin) — alias: ingest
  social-ledger article get        retrieve one item by URL or canonical id
  social-ledger article list       list recent items (newest first)
  social-ledger article search     FTS5 search across title/body/tags
  social-ledger article seen       check whether one or more URLs are cached
  social-ledger article stats      counts by source + disk usage
  social-ledger article forget     delete one item by URL or key
  social-ledger article record     write a citation-shaped stub
  social-ledger article filter     stdin/stdout JSONL filter (skip-seen, etc.)

Each subcommand has its own --help. Common flags:
  --data-dir DIR    explicit data directory (default: $SOCIAL_LEDGER_DIR
                    or $XDG_DATA_HOME/social-ledger/projects/social_fetch/)
  --format FMT      text (default) | json — for get/seen/stats/list/search

Items in the ledger don't have to be articles per se; the verb
group is named for the most common case. Tweets, HN posts, GitHub
repos, RSS entries etc. all flow through the same operations.
`)
}
