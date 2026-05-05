// Package influencers tracks people / companies the user has
// flagged as known voices on specific topics — a curated
// authority directory the agent can query mid-research.
//
// Two concepts:
//
//  1. **Influencer** — name + socials + topics they're authority
//     on + free-form description. Stored as a ledger item with
//     source="influencer" so FTS picks them up alongside fetched
//     content. Created/updated via Add (upsert by slug).
//
//  2. **Follow** — an explicit "I subscribe to <influencer>'s
//     <platform> channel for <topics>" relationship. Lives as
//     an entry on the influencer's Follows list, not a separate
//     row, since "the agent should refresh this person's X
//     timeline weekly" is naturally a property of the influencer.
//     Multiple follows per influencer are fine
//     ([{x, [ai]}, {github, [harness]}]).
//
// CLI / MCP both call into this package — the dispatchers parse
// args + render output, the package handles all storage logic
// (slug derivation, upsert merge, daemon-aware reads/writes).
package influencers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/ledger"
)

// Source is the value we stamp on every influencer's
// `core.Item.Source` so the ledger's filters / search can pick
// them out from regular fetched content.
const Source = "influencer"

// URLPrefix is the synthetic-URL scheme we use for influencer
// items. Every row's URL is `influencer://<slug>` — the ledger's
// URL→key derivation handles this fine and the agent can pass
// the URL straight back to social_ledger_get.
const URLPrefix = "influencer://"

// Influencer is the user-facing shape — what callers pass to
// Add and what List/Get return. Decoupled from `core.Item` so
// the storage layer stays an implementation detail (callers
// don't need to know about Extra map keys).
type Influencer struct {
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Type        string            `json:"type"` // "person" or "company"
	Description string            `json:"description,omitempty"`
	Socials     map[string]string `json:"socials,omitempty"`
	Topics      []string          `json:"topics,omitempty"`
	Follows     []Follow          `json:"follows,omitempty"`
	URL         string            `json:"url"` // "influencer://<slug>"
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Follow records that the user actively subscribes to an
// influencer's specific channel for specific topics. This is
// what tells the agent "yes, refresh Karpathy's X timeline when
// the user asks about AI; no, ignore his bluesky for now."
//
// Platform matches the keys in Influencer.Socials (linkedin / x /
// github / bluesky / website / mastodon / …). Topics scopes the
// follow ("subscribe to his X for AI specifically"); empty
// topics = follow every topic the influencer's known for.
type Follow struct {
	Platform string    `json:"platform"`
	Topics   []string  `json:"topics,omitempty"`
	Note     string    `json:"note,omitempty"`
	Since    time.Time `json:"since,omitempty"`
}

// AddInput is the patch passed to Add. Only set the fields you
// want to change — Add merges into an existing record when one
// exists for the same slug, otherwise creates a fresh row.
//
// Description == "" means "leave existing description as-is" on
// upsert. Pass a single space (" ") to clear an existing
// description (rare; use Remove + Add if you want a clean wipe).
type AddInput struct {
	Name        string            // required for new entries
	Slug        string            // optional override; default is slugify(Name)
	Type        string            // "person" (default) or "company"
	Description string            // free-form; replaces existing on upsert when non-empty
	Socials     map[string]string // merged with existing on upsert (new wins for same key)
	Topics      []string          // union with existing on upsert (sorted-dedup)
}

// FollowInput is the patch passed to Follow / Unfollow.
type FollowInput struct {
	NameOrSlug string   // identifies the influencer
	Platform   string   // required: which channel
	Topics     []string // optional: scope topics for the follow
	Note       string   // optional: why
}

// FilterOpts narrows what List returns. Zero-value means "all".
// Type matches Item.Kind; Topic does case-insensitive substring
// match on Tags; HasPlatform filters to entries that have a
// handle for that platform; FollowedOnly returns only those with
// at least one Follow recorded.
type FilterOpts struct {
	Type         string // "person" / "company"; "" = either
	Topic        string // case-insensitive substring match across Tags
	HasPlatform  string // "linkedin", "x", "github", … — only entries that have this social
	FollowedOnly bool   // true = only influencers with len(Follows) > 0
	Limit        int    // 0 = no cap
}

// Add inserts or upserts an influencer record. Returns the
// resulting Influencer as it now lives in the ledger — including
// any fields merged from a pre-existing row.
//
// The merge is the key invariant: re-running Add for the same
// slug with `--social mastodon=@x` adds mastodon to the existing
// socials map without dropping linkedin/x/github. Topics merge as
// a sorted-dedup union. Follows are NOT touched by Add — they
// have their own Follow / Unfollow entry points.
func Add(ctx context.Context, in AddInput) (*Influencer, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("influencers: name is required")
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		slug = Slugify(in.Name)
	}
	kind := strings.ToLower(strings.TrimSpace(in.Type))
	if kind == "" {
		kind = "person"
	}
	if kind != "person" && kind != "company" {
		return nil, fmt.Errorf("influencers: type must be 'person' or 'company', got %q", in.Type)
	}

	url := URLPrefix + slug
	prior, err := ledger.Get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("influencers: lookup existing %s: %w", slug, err)
	}

	merged := mergeForAdd(prior, in, slug, kind)
	merged.FetchedAt = time.Now().UTC()

	ledger.Ingest(ctx, *merged)

	return itemToInfluencer(merged), nil
}

// Subscribe records a "subscribe to this channel" entry on the
// influencer. Upserts: if a Follow for the same platform exists,
// merges topics (union) and replaces note when non-empty.
// Returns an error when the influencer doesn't exist (callers
// should Add them first).
func Subscribe(ctx context.Context, in FollowInput) (*Influencer, error) {
	if strings.TrimSpace(in.Platform) == "" {
		return nil, fmt.Errorf("influencers: platform is required for follow")
	}
	slug := Slugify(in.NameOrSlug)
	url := URLPrefix + slug
	prior, err := ledger.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	if prior == nil {
		return nil, fmt.Errorf("influencers: %q not found (add them first)", in.NameOrSlug)
	}

	priorFollows := readFollows(prior)
	platform := strings.ToLower(strings.TrimSpace(in.Platform))
	merged := mergeFollows(priorFollows, Follow{
		Platform: platform,
		Topics:   in.Topics,
		Note:     in.Note,
		Since:    time.Now().UTC(),
	})
	updated := rewriteWithFollows(prior, merged)
	updated.FetchedAt = time.Now().UTC()

	ledger.Ingest(ctx, *updated)
	return itemToInfluencer(updated), nil
}

// Unsubscribe removes a Follow entry by platform. Returns
// (influencer, true, nil) when something was removed,
// (influencer, false, nil) when no follow existed for that
// platform.
func Unsubscribe(ctx context.Context, nameOrSlug, platform string) (*Influencer, bool, error) {
	slug := Slugify(nameOrSlug)
	url := URLPrefix + slug
	prior, err := ledger.Get(ctx, url)
	if err != nil {
		return nil, false, err
	}
	if prior == nil {
		return nil, false, fmt.Errorf("influencers: %q not found", nameOrSlug)
	}

	want := strings.ToLower(strings.TrimSpace(platform))
	priorFollows := readFollows(prior)
	out := make([]Follow, 0, len(priorFollows))
	removed := false
	for _, f := range priorFollows {
		if strings.EqualFold(f.Platform, want) {
			removed = true
			continue
		}
		out = append(out, f)
	}
	if !removed {
		return itemToInfluencer(prior), false, nil
	}

	updated := rewriteWithFollows(prior, out)
	updated.FetchedAt = time.Now().UTC()
	ledger.Ingest(ctx, *updated)
	return itemToInfluencer(updated), true, nil
}

// Remove deletes the influencer identified by name or slug.
// Returns (true, nil) when something was deleted, (false, nil)
// when no matching row existed (idempotent — agents can call
// this without checking first).
func Remove(ctx context.Context, nameOrSlug string) (bool, error) {
	slug := Slugify(nameOrSlug)
	url := URLPrefix + slug
	prior, err := ledger.Get(ctx, url)
	if err != nil {
		return false, err
	}
	if prior == nil {
		return false, nil
	}
	if !ledger.Disabled() {
		c := ledger.NewDaemonClient()
		if c.Reachable(ctx) {
			return c.Forget(ctx, url)
		}
	}
	return removeViaSubprocess(ctx, url)
}

// List returns all influencers matching the filter. Sorted by
// Name asc (alphabetical) so output is stable between calls and
// easy for the agent to scan.
func List(ctx context.Context, f FilterOpts) ([]Influencer, error) {
	items, err := listFromLedger(ctx, f.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]Influencer, 0, len(items))
	for _, it := range items {
		s := itemToInfluencer(&it)
		if !f.matches(s) {
			continue
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Get returns one influencer by name or slug. Returns
// (nil, nil) when no match — same convention as ledger.Get so
// callers can distinguish "lookup error" from "doesn't exist."
func Get(ctx context.Context, nameOrSlug string) (*Influencer, error) {
	slug := Slugify(nameOrSlug)
	url := URLPrefix + slug
	it, err := ledger.Get(ctx, url)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, nil
	}
	return itemToInfluencer(it), nil
}

// ----- helpers (exported where useful) -----

// Slugify produces the canonical slug used as the ledger key.
// Lowercase + replace non-alnum runs with `-` + trim leading /
// trailing dashes. Idempotent: Slugify(Slugify(x)) == Slugify(x).
//
// Exported so tests + the CLI's `--slug` flag share one
// derivation rule.
func Slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Empty / whitespace-only / non-alnum-only inputs collapse to
	// "default" so URLs like influencer:// (bare) never reach the
	// store. Callers that care about "did this name resolve to a
	// real slug?" should validate the name before calling.
	if s == "" {
		return "default"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "default"
	}
	return out
}

// mergeForAdd takes the prior ledger item (or nil) and an
// AddInput, returns the merged core.Item ready for re-ingest.
// Add does NOT touch Follows — those have their own entry
// points. Existing Follows are preserved through the merge.
func mergeForAdd(prior *core.Item, in AddInput, slug, kind string) *core.Item {
	out := &core.Item{
		Source:      Source,
		Kind:        kind,
		URL:         URLPrefix + slug,
		CanonicalID: slug,
		Title:       in.Name,
		Author:      in.Name,
	}

	priorSocials := map[string]string{}
	priorTopics := []string{}
	priorDescription := ""
	priorName := in.Name
	priorKind := kind
	priorFollows := []Follow{}
	if prior != nil {
		priorName = prior.Title
		if prior.Kind != "" {
			priorKind = prior.Kind
		}
		priorDescription = prior.Summary
		priorTopics = append(priorTopics, prior.Tags...)
		if raw, ok := prior.Extra["socials"].(map[string]any); ok {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					priorSocials[k] = s
				}
			}
		}
		priorFollows = readFollows(prior)
	}

	if strings.TrimSpace(in.Name) != "" {
		out.Title = in.Name
		out.Author = in.Name
	} else {
		out.Title = priorName
		out.Author = priorName
	}
	if strings.TrimSpace(in.Type) != "" {
		out.Kind = kind
	} else {
		out.Kind = priorKind
	}
	if strings.TrimSpace(in.Description) != "" {
		out.Summary = in.Description
	} else {
		out.Summary = priorDescription
	}

	socials := priorSocials
	for k, v := range in.Socials {
		if v == "" {
			continue
		}
		socials[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	out.Tags = mergeTopics(priorTopics, in.Topics)
	// Store socials as map[string]any (with string values) so the
	// in-memory shape matches what comes back after a JSON
	// round-trip through the ledger. itemToInfluencer reads via the
	// `map[string]any` type assertion — keep one shape on both
	// sides so callers don't see a phantom-empty socials map on
	// the immediate post-write read.
	socialsAny := make(map[string]any, len(socials))
	for k, v := range socials {
		socialsAny[k] = v
	}
	out.Extra = map[string]any{
		"socials":    socialsAny,
		"follows":    followsToExtra(priorFollows),
		"kind_label": out.Kind,
	}
	out.Content = renderBody(out.Title, out.Summary, socials, out.Tags, priorFollows)
	return out
}

// rewriteWithFollows produces a new core.Item from an existing
// row + a new Follows list. Used by Follow / Unfollow to update
// just the follows without touching socials / topics / description.
func rewriteWithFollows(prior *core.Item, follows []Follow) *core.Item {
	out := *prior
	if out.Extra == nil {
		out.Extra = map[string]any{}
	}
	// Copy the extra map so we don't mutate the caller's prior.
	extraCopy := make(map[string]any, len(out.Extra))
	for k, v := range out.Extra {
		extraCopy[k] = v
	}
	extraCopy["follows"] = followsToExtra(follows)
	out.Extra = extraCopy

	socials := map[string]string{}
	if raw, ok := prior.Extra["socials"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				socials[k] = s
			}
		}
	}
	out.Content = renderBody(out.Title, out.Summary, socials, out.Tags, follows)
	return &out
}

// mergeFollows upserts one Follow into a list. Same-platform
// entry overwrites topics+note via union (topics) and
// replace-when-non-empty (note + since).
func mergeFollows(prior []Follow, incoming Follow) []Follow {
	platform := strings.ToLower(strings.TrimSpace(incoming.Platform))
	out := make([]Follow, 0, len(prior)+1)
	merged := false
	for _, f := range prior {
		if strings.EqualFold(f.Platform, platform) {
			f.Topics = mergeTopics(f.Topics, incoming.Topics)
			if strings.TrimSpace(incoming.Note) != "" {
				f.Note = incoming.Note
			}
			if !incoming.Since.IsZero() {
				f.Since = incoming.Since
			}
			merged = true
		}
		out = append(out, f)
	}
	if !merged {
		out = append(out, Follow{
			Platform: platform,
			Topics:   incoming.Topics,
			Note:     incoming.Note,
			Since:    incoming.Since,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Platform < out[j].Platform
	})
	return out
}

// readFollows pulls the Follows list back out of an Item's Extra
// map. Tolerant of older rows that didn't have `follows` set.
func readFollows(it *core.Item) []Follow {
	if it == nil || it.Extra == nil {
		return nil
	}
	raw, ok := it.Extra["follows"].([]any)
	if !ok {
		return nil
	}
	out := make([]Follow, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		f := Follow{}
		if v, ok := m["platform"].(string); ok {
			f.Platform = v
		}
		if v, ok := m["note"].(string); ok {
			f.Note = v
		}
		if topics, ok := m["topics"].([]any); ok {
			for _, t := range topics {
				if ts, ok := t.(string); ok {
					f.Topics = append(f.Topics, ts)
				}
			}
		}
		if v, ok := m["since"].(string); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				f.Since = t
			}
		}
		if f.Platform != "" {
			out = append(out, f)
		}
	}
	return out
}

// followsToExtra serialises a Follow slice into a JSON-ready
// shape ([]any of maps) for storage in Item.Extra. Round-trip
// pair with readFollows.
func followsToExtra(follows []Follow) []any {
	out := make([]any, 0, len(follows))
	for _, f := range follows {
		m := map[string]any{"platform": f.Platform}
		if len(f.Topics) > 0 {
			m["topics"] = f.Topics
		}
		if f.Note != "" {
			m["note"] = f.Note
		}
		if !f.Since.IsZero() {
			m["since"] = f.Since.Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}

// mergeTopics produces a sorted-dedup union of two topic lists.
// Match is case-insensitive — first-seen casing wins so the
// UI sees stable labels.
func mergeTopics(a, b []string) []string {
	seen := map[string]string{}
	for _, src := range [][]string{a, b} {
		for _, t := range src {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			lc := strings.ToLower(t)
			if _, ok := seen[lc]; !ok {
				seen[lc] = t
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// renderBody produces the Content field — a markdown summary of
// the influencer that FTS picks up. Includes follows so an agent
// searching for "ai" finds influencers whose follows are scoped
// to ai topics, not just topics they're known for.
func renderBody(name, desc string, socials map[string]string, topics []string, follows []Follow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", name)
	if desc != "" {
		fmt.Fprintf(&b, "%s\n\n", desc)
	}
	if len(socials) > 0 {
		b.WriteString("## Socials\n\n")
		platforms := make([]string, 0, len(socials))
		for k := range socials {
			platforms = append(platforms, k)
		}
		sort.Strings(platforms)
		for _, p := range platforms {
			fmt.Fprintf(&b, "- %s: %s\n", p, socials[p])
		}
		b.WriteString("\n")
	}
	if len(topics) > 0 {
		b.WriteString("## Known for\n\n")
		for _, t := range topics {
			fmt.Fprintf(&b, "- %s\n", t)
		}
		b.WriteString("\n")
	}
	if len(follows) > 0 {
		b.WriteString("## Subscribed channels\n\n")
		for _, f := range follows {
			line := "- " + f.Platform
			if len(f.Topics) > 0 {
				line += " (" + strings.Join(f.Topics, ", ") + ")"
			}
			if f.Note != "" {
				line += " — " + f.Note
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// itemToInfluencer is the storage→user-shape conversion.
// Inverse of mergeForAdd's output construction.
//
// The store doesn't persist `Kind` in a typed column (only the
// canonical fields it indexes — source/url/title/etc. — get
// columns). We mirror Kind into Extra.kind_label on write and
// read it back from there, falling back to Item.Kind for
// round-trips that didn't go through the store (in-memory
// chains, tests).
func itemToInfluencer(it *core.Item) *Influencer {
	if it == nil {
		return nil
	}
	kind := it.Kind
	if kind == "" {
		if v, ok := it.Extra["kind_label"].(string); ok {
			kind = v
		}
	}
	s := &Influencer{
		Slug:        it.CanonicalID,
		Name:        it.Title,
		Type:        kind,
		Description: it.Summary,
		Topics:      append([]string(nil), it.Tags...),
		Follows:     readFollows(it),
		URL:         it.URL,
		UpdatedAt:   it.FetchedAt.UTC(),
	}
	if raw, ok := it.Extra["socials"].(map[string]any); ok {
		s.Socials = map[string]string{}
		for k, v := range raw {
			if str, ok := v.(string); ok {
				s.Socials[k] = str
			}
		}
	}
	return s
}

// matches applies a FilterOpts to a single Influencer.
func (f FilterOpts) matches(s *Influencer) bool {
	if f.Type != "" && !strings.EqualFold(s.Type, f.Type) {
		return false
	}
	if f.Topic != "" {
		hit := false
		needle := strings.ToLower(f.Topic)
		for _, t := range s.Topics {
			if strings.Contains(strings.ToLower(t), needle) {
				hit = true
				break
			}
		}
		if !hit {
			// Also match against follow topics, since the agent
			// asking "AI authorities" should find someone who's
			// followed for AI even if their general Topics list
			// doesn't include the term.
			for _, fl := range s.Follows {
				for _, t := range fl.Topics {
					if strings.Contains(strings.ToLower(t), needle) {
						hit = true
						break
					}
				}
				if hit {
					break
				}
			}
		}
		if !hit {
			return false
		}
	}
	if f.HasPlatform != "" {
		v, ok := s.Socials[strings.ToLower(strings.TrimSpace(f.HasPlatform))]
		if !ok || strings.TrimSpace(v) == "" {
			return false
		}
	}
	if f.FollowedOnly && len(s.Follows) == 0 {
		return false
	}
	return true
}
