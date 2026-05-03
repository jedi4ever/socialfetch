package htmlmd

import (
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
)

// KaufmannConverter wraps github.com/JohannesKaufmann/html-to-markdown
// (v2). The library is the de facto standard for HTML→Markdown in Go
// — actively maintained, plugin system, much better edge-case
// coverage than the hand-rolled BuiltinConverter (tables,
// strikethrough, definition lists, complex code blocks, etc.).
//
// We use the library's `ConvertString` helper which configures the
// commonmark plugin with sensible defaults. No additional plugin
// wiring needed for the article-extraction use case — the default
// commonmark plugin already covers what BuiltinConverter handles plus
// the missing edge cases.
//
// Errors from the underlying library are converted to an empty string
// to match BuiltinConverter's "best-effort, never panic" contract;
// extraction callers test for empty output and fall back to summary
// text.
type KaufmannConverter struct{}

// kaufmannConv is a process-wide reusable converter. The package-level
// htmltomarkdown.ConvertString helper builds a fresh converter (with
// base + commonmark plugins, regex compiles, plugin slice copy) on
// every call — measurable in HN/LinkedIn comment-heavy fetches that
// invoke Convert hundreds of times per request. The Converter type
// holds a sync.RWMutex internally and is documented goroutine-safe,
// so a single shared instance is fine. Plugin set mirrors what
// htmltomarkdown.ConvertString itself wires up, so output is
// byte-identical to the previous per-call construction.
var kaufmannConv = converter.NewConverter(
	converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
	),
)

func (k *KaufmannConverter) Convert(htmlFragment string) string {
	out, err := kaufmannConv.ConvertString(htmlFragment)
	if err != nil {
		return ""
	}
	return out
}
