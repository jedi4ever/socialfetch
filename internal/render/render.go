// Package render turns a core.Item into either pretty-printed JSON or
// clean Markdown. The two formats are designed to round-trip the same
// information; markdown is for humans, JSON for downstream pipelines.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// Format names a supported output format.
type Format string

const (
	FormatJSON     Format = "json"
	FormatJSONL    Format = "jsonl"
	FormatMarkdown Format = "markdown"
)

// ParseFormat is a forgiving alias resolver for CLI flag values.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json":
		return FormatJSON, nil
	case "jsonl", "ndjson":
		return FormatJSONL, nil
	case "md", "markdown":
		return FormatMarkdown, nil
	default:
		return "", fmt.Errorf("unknown format %q (want json, jsonl, or markdown)", s)
	}
}

// Envelope is what we actually serialize: the Item plus a small wrapper
// that records when it was rendered. Both fetched_at (when the data was
// pulled) and written_at (when this output was produced) are included so
// pipelines can tell stale items apart.
type Envelope struct {
	WrittenAt time.Time  `json:"written_at"`
	Item      *core.Item `json:"item"`
}

// Item renders a single item to w in the given format.
func Item(w io.Writer, item *core.Item, format Format) error {
	env := &Envelope{WrittenAt: time.Now().UTC(), Item: item}
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(env)
	case FormatJSONL:
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		return enc.Encode(env)
	case FormatMarkdown:
		return renderMarkdown(w, env)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

func renderMarkdown(w io.Writer, env *Envelope) error {
	var b strings.Builder
	it := env.Item

	if it.Title != "" {
		b.WriteString("# " + it.Title + "\n\n")
	}

	// Frontmatter-style metadata block — stable for diffing and easy to
	// strip when an agent wants only the body.
	b.WriteString("**Source:** " + it.Source)
	if it.Kind != "" {
		b.WriteString(" / " + it.Kind)
	}
	b.WriteString("  \n")
	if it.Author != "" {
		b.WriteString("**Author:** " + it.Author)
		if it.AuthorURL != "" {
			b.WriteString(" (" + it.AuthorURL + ")")
		}
		b.WriteString("  \n")
	}
	if it.URL != "" {
		b.WriteString("**URL:** " + it.URL + "  \n")
	}
	if it.Published != nil {
		b.WriteString("**Published:** " + it.Published.Format(time.RFC3339) + "  \n")
	}
	if it.Score != 0 {
		b.WriteString(fmt.Sprintf("**Score:** %d  \n", it.Score))
	}
	if len(it.Tags) > 0 {
		b.WriteString("**Tags:** " + strings.Join(it.Tags, ", ") + "  \n")
	}
	b.WriteString("**Fetched:** " + it.FetchedAt.Format(time.RFC3339) + "  \n")
	b.WriteString("**Written:** " + env.WrittenAt.Format(time.RFC3339) + "  \n")
	b.WriteString("\n")

	if it.Summary != "" && it.Summary != it.Content {
		b.WriteString("> " + strings.ReplaceAll(strings.TrimSpace(it.Summary), "\n", "\n> ") + "\n\n")
	}

	if it.Content != "" {
		b.WriteString(strings.TrimSpace(it.Content) + "\n\n")
	}

	if len(it.Media) > 0 {
		b.WriteString("## Media\n\n")
		for _, m := range it.Media {
			alt := m.Alt
			if alt == "" {
				alt = m.Type
			}
			b.WriteString(fmt.Sprintf("- ![%s](%s)\n", alt, m.URL))
		}
		b.WriteString("\n")
	}

	if len(it.Children) > 0 {
		b.WriteString("## Entries\n\n")
		for _, c := range it.Children {
			b.WriteString(renderChildEntry(c))
		}
	}

	if len(it.Comments) > 0 {
		b.WriteString("## Comments\n\n")
		for _, c := range it.Comments {
			b.WriteString(renderComment(c))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

func renderChildEntry(c core.Item) string {
	var b strings.Builder
	if c.Title != "" {
		b.WriteString("### [" + c.Title + "](" + c.URL + ")\n")
	} else {
		b.WriteString("### " + c.URL + "\n")
	}
	if c.Author != "" {
		b.WriteString("*" + c.Author + "*")
	}
	if c.Published != nil {
		if c.Author != "" {
			b.WriteString(" — ")
		}
		b.WriteString(c.Published.Format("2006-01-02"))
	}
	if c.Author != "" || c.Published != nil {
		b.WriteString("\n\n")
	}
	if c.Summary != "" {
		b.WriteString(strings.TrimSpace(c.Summary) + "\n\n")
	}
	return b.String()
}

func renderComment(c core.Comment) string {
	var b strings.Builder
	indent := strings.Repeat("  ", c.Depth)
	header := fmt.Sprintf("%s- **%s**", indent, ellipsis(c.Author, "anon"))
	if c.Score != 0 {
		header += fmt.Sprintf(" (%d)", c.Score)
	}
	if c.Published != nil {
		header += " · " + c.Published.Format(time.RFC3339)
	}
	b.WriteString(header + "\n")

	body := strings.TrimSpace(c.Body)
	if body != "" {
		for _, line := range strings.Split(body, "\n") {
			b.WriteString(indent + "  " + line + "\n")
		}
	}
	for _, r := range c.Replies {
		b.WriteString(renderComment(r))
	}
	return b.String()
}

func ellipsis(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
