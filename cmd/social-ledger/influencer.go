package main

// CLI for `social-ledger influencer` — track people / companies
// the agent should know about as topic authorities. Lives under
// social-ledger because influencers are a ledger-managed entity
// (alongside articles); social-fetch is for going-and-getting
// new content, social-ledger is for managing what's stored.
//
// Two relationships:
//
//   - The influencer record (add / remove / list / show / update
//     of socials, topics, description). Re-adding upserts.
//   - Per-channel subscriptions (subscribe / unsubscribe). Says
//     "I want to track this person's X timeline for these
//     topics." Independent from the existence of the record
//     itself — you can have an influencer in the directory
//     without subscribing to any of their channels.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jedi4ever/social-skills/internal/platforms/influencers"
)

func cmdInfluencer(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "", "list":
		return runInfluencerList(args)
	case "add":
		return runInfluencerAdd(args)
	case "remove", "rm", "delete":
		return runInfluencerRemove(args)
	case "show", "get":
		return runInfluencerShow(args)
	case "subscribe", "follow":
		return runInfluencerSubscribe(args)
	case "unsubscribe", "unfollow":
		return runInfluencerUnsubscribe(args)
	}
	if sub == "-h" || sub == "--help" {
		printInfluencerHelp()
		return nil
	}
	return fmt.Errorf("influencer: unknown subcommand %q (try `influencer --help`)", sub)
}

func printInfluencerHelp() {
	fmt.Print(`social-ledger influencer — track people / companies as topic authorities

Usage:
  social-ledger influencer list                        list all (default)
  social-ledger influencer add <name> [flags]          create or upsert
  social-ledger influencer remove <name|slug>          delete record
  social-ledger influencer show <name|slug>            single record
  social-ledger influencer subscribe <name> --platform x [--topics ai,…] [--note "…"]
                                                       track this channel
  social-ledger influencer unsubscribe <name> --platform x
                                                       stop tracking the channel

Add flags:
  --type person|company         (default: person)
  --description "..."           free-form description; replaces existing on upsert
  --linkedin <handle>           shorthand for --social linkedin=<handle>
  --x <handle>                  shorthand for --social x=<handle>
  --github <user>               shorthand for --social github=<user>
  --bluesky <handle>            shorthand for --social bluesky=<handle>
  --website <url>               shorthand for --social website=<url>
  --social <platform>=<value>   repeated; extensible (mastodon, threads, ...)
  --topics <t1,t2,t3>           comma-separated; merged with existing on upsert
  --slug <s>                    override the canonical slug

List/show flags:
  --type person|company         only matching type
  --topic <tag>                 substring match across topics + follow scopes (case-insensitive)
  --has <platform>              only entries with a handle for that platform
  --followed                    only entries with at least one subscribed channel
  -n, --limit N                 cap output (0 = no cap)
  -f, --format md|json          markdown (default) | json

Storage: influencers live in the local ledger as
source="influencer" items, so social-ledger search "topic" also
surfaces matching authorities alongside fetched articles.

Examples:
  social-ledger influencer add "Cole Medin" \
    --linkedin cole-medin-727752184 --x colemedin --github coleam00 \
    --topics ai,agents --description "Built Archon (20k stars)"
  social-ledger influencer subscribe "Cole Medin" --platform x --topics ai
  social-ledger influencer list --topic ai --followed
  social-ledger influencer unsubscribe cole-medin --platform x
  social-ledger influencer remove cole-medin
`)
}

// influencerAddFlags is the parsed shape of `influencer add`.
type influencerAddFlags struct {
	name        string
	slug        string
	typ         string
	description string
	socials     map[string]string
	topics      []string
}

func parseInfluencerAdd(args []string) (influencerAddFlags, error) {
	f := influencerAddFlags{socials: map[string]string{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printInfluencerHelp()
			os.Exit(0)
		case "--type":
			if v, err := nextInfluencerArg(args, &i, "--type"); err != nil {
				return f, err
			} else {
				f.typ = v
			}
		case "--description":
			if v, err := nextInfluencerArg(args, &i, "--description"); err != nil {
				return f, err
			} else {
				f.description = v
			}
		case "--linkedin":
			if v, err := nextInfluencerArg(args, &i, "--linkedin"); err != nil {
				return f, err
			} else {
				f.socials["linkedin"] = v
			}
		case "--x", "--twitter":
			if v, err := nextInfluencerArg(args, &i, "--x"); err != nil {
				return f, err
			} else {
				f.socials["x"] = v
			}
		case "--github":
			if v, err := nextInfluencerArg(args, &i, "--github"); err != nil {
				return f, err
			} else {
				f.socials["github"] = v
			}
		case "--bluesky":
			if v, err := nextInfluencerArg(args, &i, "--bluesky"); err != nil {
				return f, err
			} else {
				f.socials["bluesky"] = v
			}
		case "--website":
			if v, err := nextInfluencerArg(args, &i, "--website"); err != nil {
				return f, err
			} else {
				f.socials["website"] = v
			}
		case "--social":
			if v, err := nextInfluencerArg(args, &i, "--social"); err != nil {
				return f, err
			} else {
				eq := strings.Index(v, "=")
				if eq < 1 || eq == len(v)-1 {
					return f, fmt.Errorf("--social: expected platform=value, got %q", v)
				}
				f.socials[strings.ToLower(strings.TrimSpace(v[:eq]))] = strings.TrimSpace(v[eq+1:])
			}
		case "--topics":
			if v, err := nextInfluencerArg(args, &i, "--topics"); err != nil {
				return f, err
			} else {
				for _, t := range strings.Split(v, ",") {
					if t = strings.TrimSpace(t); t != "" {
						f.topics = append(f.topics, t)
					}
				}
			}
		case "--slug":
			if v, err := nextInfluencerArg(args, &i, "--slug"); err != nil {
				return f, err
			} else {
				f.slug = v
			}
		default:
			if strings.HasPrefix(a, "-") {
				return f, fmt.Errorf("influencer add: unknown flag %q", a)
			}
			if f.name != "" {
				return f, fmt.Errorf("influencer add: too many positional args (got %q after name=%q)", a, f.name)
			}
			f.name = a
		}
	}
	if f.name == "" {
		return f, fmt.Errorf("influencer add: <name> required")
	}
	return f, nil
}

// nextInfluencerArg pulls the next positional value for a flag and bumps i.
func nextInfluencerArg(args []string, i *int, flag string) (string, error) {
	*i++
	if *i >= len(args) {
		return "", fmt.Errorf("%s needs a value", flag)
	}
	return args[*i], nil
}

func runInfluencerAdd(args []string) error {
	flags, err := parseInfluencerAdd(args)
	if err != nil {
		return err
	}
	in := influencers.AddInput{
		Name:        flags.name,
		Slug:        flags.slug,
		Type:        flags.typ,
		Description: flags.description,
		Socials:     flags.socials,
		Topics:      flags.topics,
	}
	s, err := influencers.Add(context.Background(), in)
	if err != nil {
		return err
	}
	fmt.Printf("influencer: %s (%s) — slug=%s\n", s.Name, s.Type, s.Slug)
	return nil
}

func runInfluencerRemove(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("influencer remove: <name|slug> required")
	}
	deleted, err := influencers.Remove(context.Background(), args[0])
	if err != nil {
		return err
	}
	if deleted {
		fmt.Printf("removed: %s\n", args[0])
	} else {
		fmt.Printf("influencer: %s not found\n", args[0])
	}
	return nil
}

type influencerSubFlags struct {
	name     string
	platform string
	topics   []string
	note     string
}

func parseInfluencerSubscribe(args []string) (influencerSubFlags, error) {
	f := influencerSubFlags{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printInfluencerHelp()
			os.Exit(0)
		case "--platform":
			if v, err := nextInfluencerArg(args, &i, "--platform"); err != nil {
				return f, err
			} else {
				f.platform = v
			}
		case "--topics":
			if v, err := nextInfluencerArg(args, &i, "--topics"); err != nil {
				return f, err
			} else {
				for _, t := range strings.Split(v, ",") {
					if t = strings.TrimSpace(t); t != "" {
						f.topics = append(f.topics, t)
					}
				}
			}
		case "--note":
			if v, err := nextInfluencerArg(args, &i, "--note"); err != nil {
				return f, err
			} else {
				f.note = v
			}
		default:
			if strings.HasPrefix(a, "-") {
				return f, fmt.Errorf("influencer subscribe: unknown flag %q", a)
			}
			if f.name != "" {
				return f, fmt.Errorf("influencer subscribe: too many positional args (got %q after name=%q)", a, f.name)
			}
			f.name = a
		}
	}
	if f.name == "" {
		return f, fmt.Errorf("influencer subscribe: <name|slug> required")
	}
	if f.platform == "" {
		return f, fmt.Errorf("influencer subscribe: --platform required")
	}
	return f, nil
}

func runInfluencerSubscribe(args []string) error {
	flags, err := parseInfluencerSubscribe(args)
	if err != nil {
		return err
	}
	s, err := influencers.Subscribe(context.Background(), influencers.FollowInput{
		NameOrSlug: flags.name,
		Platform:   flags.platform,
		Topics:     flags.topics,
		Note:       flags.note,
	})
	if err != nil {
		return err
	}
	fmt.Printf("subscribed: %s on %s", s.Name, flags.platform)
	if len(flags.topics) > 0 {
		fmt.Printf(" for [%s]", strings.Join(flags.topics, ", "))
	}
	fmt.Println()
	return nil
}

func runInfluencerUnsubscribe(args []string) error {
	flags, err := parseInfluencerSubscribe(args)
	if err != nil {
		return err
	}
	_, removed, err := influencers.Unsubscribe(context.Background(), flags.name, flags.platform)
	if err != nil {
		return err
	}
	if removed {
		fmt.Printf("unsubscribed: %s from %s\n", flags.name, flags.platform)
	} else {
		fmt.Printf("influencer: %s was not subscribed on %s\n", flags.name, flags.platform)
	}
	return nil
}

type influencerListFlags struct {
	typ          string
	topic        string
	hasPlatform  string
	followedOnly bool
	limit        int
	format       string
}

func parseInfluencerList(args []string) (influencerListFlags, []string, error) {
	f := influencerListFlags{format: "markdown", limit: 0}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printInfluencerHelp()
			os.Exit(0)
		case "--type":
			if v, err := nextInfluencerArg(args, &i, "--type"); err != nil {
				return f, positional, err
			} else {
				f.typ = v
			}
		case "--topic":
			if v, err := nextInfluencerArg(args, &i, "--topic"); err != nil {
				return f, positional, err
			} else {
				f.topic = v
			}
		case "--has":
			if v, err := nextInfluencerArg(args, &i, "--has"); err != nil {
				return f, positional, err
			} else {
				f.hasPlatform = v
			}
		case "--followed":
			f.followedOnly = true
		case "-n", "--limit":
			if v, err := nextInfluencerArg(args, &i, "--limit"); err != nil {
				return f, positional, err
			} else {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return f, positional, fmt.Errorf("--limit: invalid value %q", v)
				}
				f.limit = n
			}
		case "-f", "--format":
			if v, err := nextInfluencerArg(args, &i, "--format"); err != nil {
				return f, positional, err
			} else {
				v = strings.ToLower(v)
				if v == "md" {
					v = "markdown"
				}
				if v != "markdown" && v != "json" {
					return f, positional, fmt.Errorf("--format: must be markdown or json")
				}
				f.format = v
			}
		default:
			if strings.HasPrefix(a, "-") {
				return f, positional, fmt.Errorf("influencer: unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	return f, positional, nil
}

func runInfluencerList(args []string) error {
	flags, positional, err := parseInfluencerList(args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return fmt.Errorf("influencer list: unexpected positional args %v", positional)
	}
	results, err := influencers.List(context.Background(), influencers.FilterOpts{
		Type:         flags.typ,
		Topic:        flags.topic,
		HasPlatform:  flags.hasPlatform,
		FollowedOnly: flags.followedOnly,
		Limit:        flags.limit,
	})
	if err != nil {
		return err
	}
	return renderInfluencerList(flags.format, results)
}

func runInfluencerShow(args []string) error {
	flags, positional, err := parseInfluencerList(args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return fmt.Errorf("influencer show: <name|slug> required")
	}
	s, err := influencers.Get(context.Background(), positional[0])
	if err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("influencer: %q not found", positional[0])
	}
	return renderInfluencerList(flags.format, []influencers.Influencer{*s})
}

func renderInfluencerList(format string, results []influencers.Influencer) error {
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(results)
	}
	if len(results) == 0 {
		fmt.Println("(no influencers matched)")
		return nil
	}
	for _, s := range results {
		bits := []string{}
		platforms := sortedSocialKeys(s.Socials)
		for _, p := range platforms {
			v := s.Socials[p]
			if v == "" {
				continue
			}
			bits = append(bits, fmt.Sprintf("[%s](%s)", p, socialURL(p, v)))
		}
		fmt.Printf("- **%s** (%s)", s.Name, s.Type)
		if len(bits) > 0 {
			fmt.Printf(" · %s", strings.Join(bits, " · "))
		}
		if len(s.Topics) > 0 {
			fmt.Printf(" · 📚 %s", strings.Join(s.Topics, ", "))
		}
		fmt.Println()
		if s.Description != "" {
			fmt.Printf("  > %s\n", s.Description)
		}
		if len(s.Follows) > 0 {
			fmt.Print("  📡 subscribed:")
			for i, f := range s.Follows {
				sep := ", "
				if i == 0 {
					sep = " "
				}
				fmt.Printf("%s%s", sep, f.Platform)
				if len(f.Topics) > 0 {
					fmt.Printf(" (%s)", strings.Join(f.Topics, ", "))
				}
			}
			fmt.Println()
		}
	}
	return nil
}

func sortedSocialKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	priority := map[string]int{"linkedin": 0, "x": 1, "github": 2, "bluesky": 3, "website": 4}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			pa, ok := priority[out[j]]
			if !ok {
				pa = 100
			}
			pb, ok := priority[out[j-1]]
			if !ok {
				pb = 100
			}
			if pa < pb || (pa == pb && out[j] < out[j-1]) {
				out[j], out[j-1] = out[j-1], out[j]
			} else {
				break
			}
		}
	}
	return out
}

func socialURL(platform, handle string) string {
	switch strings.ToLower(platform) {
	case "linkedin":
		return "https://www.linkedin.com/in/" + strings.TrimPrefix(handle, "@")
	case "x", "twitter":
		return "https://x.com/" + strings.TrimPrefix(handle, "@")
	case "github":
		return "https://github.com/" + strings.TrimPrefix(handle, "@")
	case "bluesky":
		return "https://bsky.app/profile/" + strings.TrimPrefix(handle, "@")
	case "website":
		return handle
	default:
		return handle
	}
}
