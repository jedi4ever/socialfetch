// Package htmlmd is a deprecated shim that delegates to
// internal/render/htmlmd, the new pluggable HTML→Markdown system.
//
// New code should import internal/render/htmlmd directly. This shim
// stays around for one cycle so existing callers (article extractors,
// medium / substack platforms) keep working without an edit; once
// every importer has migrated, this package will be deleted.
//
// Provider selection happens via the HTML2MD_PROVIDER env var. See
// internal/render/htmlmd/converter.go for the full menu.
package htmlmd

import (
	"github.com/jedi4ever/socialfetch/internal/render/htmlmd"
)

// Convert delegates to the package-level Convert in
// internal/render/htmlmd, which uses the env-selected default
// converter (Kaufmann v2 unless HTML2MD_PROVIDER says otherwise).
func Convert(htmlFragment string) string {
	return htmlmd.Convert(htmlFragment)
}
