// Package arxiv fetches paper metadata + abstract via a configurable
// fetch chain. Default chain is `api,jina`:
//
//   - `api`  — arXiv's public Atom API at export.arxiv.org/api/query.
//     Highest fidelity: structured authors, categories,
//     publication dates, plus the abstract. Body is then
//     enriched from arxiv.org/html/<id> (post-2024 papers) or
//     the PDF via PDFReader.
//   - `jina` — anonymous catch-all via r.jina.ai on arxiv.org/abs/<id>.
//     Body-only — no structured authors / categories / dates.
//     Used when the Atom API is unreachable (rare).
//
// We claim:
//
//	arxiv.org/abs/<id>     → metadata page
//	arxiv.org/pdf/<id>     → PDF (we still pull metadata, not PDF text)
//	arxiv.org/html/<id>    → rendered HTML version (metadata path)
//
// IDs follow either the legacy hyphenated form (cs.LG/9301001) or the
// 2007+ "YYMM.NNNN" form (2403.04132); both are accepted.
//
// Operators override the chain via SOCIAL_FETCH_CHAIN_ARXIV.
package arxiv

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/fetchchain"
	"github.com/jedi4ever/social-skills/internal/render/htmlmd"
)

const (
	defaultAPIBase  = "https://export.arxiv.org/api/query"
	defaultHTMLBase = "https://arxiv.org/html/"
	defaultPDFBase  = "https://arxiv.org/pdf/"
)

type Fetcher struct {
	BaseURL string

	// HTMLBase + PDFBase + PDFReader exist so unit tests can swap out
	// the body-enrichment paths without hitting real arxiv.org or
	// r.jina.ai. Production callers stick with the defaults via
	// New(); only TestFetch overrides them. EnrichBody=false skips
	// the enrichment entirely (test default — keeps the contract
	// "Content == abstract" stable).
	HTMLBase   string
	PDFBase    string
	PDFReader  htmlmd.PDFReader
	EnrichBody bool
}

// New returns a Fetcher with body enrichment ON — production callers
// fetch the full paper text from arXiv's HTML render (or the PDF as
// fallback). Tests want EnrichBody false unless they explicitly stub
// the HTML/PDF endpoints.
func New() *Fetcher {
	return &Fetcher{
		BaseURL:    defaultAPIBase,
		HTMLBase:   defaultHTMLBase,
		PDFBase:    defaultPDFBase,
		EnrichBody: true,
	}
}

func (Fetcher) Name() string { return "arxiv" }

// idRE matches both the post-2007 NNNN.NNNNN form and the legacy
// archive/category/yymm form. We accept an optional version suffix.
var idRE = regexp.MustCompile(`(?:[a-z\-]+(?:\.[A-Z]{2})?/[0-9]{7}|[0-9]{4}\.[0-9]{4,5})(v[0-9]+)?`)

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "arxiv.org" && host != "export.arxiv.org" {
		return false
	}
	return strings.Contains(u.Path, "/abs/") ||
		strings.Contains(u.Path, "/pdf/") ||
		strings.Contains(u.Path, "/html/")
}

func extractID(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	path := u.Path
	for _, prefix := range []string{"/abs/", "/pdf/", "/html/"} {
		if i := strings.Index(path, prefix); i >= 0 {
			rest := path[i+len(prefix):]
			rest = strings.TrimSuffix(rest, ".pdf")
			rest = strings.TrimSuffix(rest, ".html")
			if id := idRE.FindString(rest); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("no arxiv id in %q", rawURL)
}

// atomFeed models the slice of arXiv's Atom output we read.
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string       `xml:"id"`
	Title      string       `xml:"title"`
	Summary    string       `xml:"summary"`
	Published  string       `xml:"published"`
	Updated    string       `xml:"updated"`
	Authors    []atomAuthor `xml:"author"`
	Categories []struct {
		Term string `xml:"term,attr"`
	} `xml:"category"`
	Links []atomLink `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Href  string `xml:"href,attr"`
	Title string `xml:"title,attr"`
}

var defaultChain = []fetchchain.Method{
	fetchchain.MethodAPI,
	fetchchain.MethodJina,
}

var supportedMethods = map[fetchchain.Method]bool{
	fetchchain.MethodAPI:  true,
	fetchchain.MethodJina: true,
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	id, err := extractID(raw)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	chain := fetchchain.Resolve(fetchchain.FromEnv("arxiv"), defaultChain, supportedMethods)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodAPI: func(ctx context.Context, _ string) (*core.Item, error) {
			return f.fetchViaAPI(ctx, id, opts)
		},
		fetchchain.MethodJina: func(ctx context.Context, _ string) (*core.Item, error) {
			return f.fetchViaJina(ctx, id)
		},
	}
	item, _, err := fetchchain.Run(ctx, "arxiv", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, fmt.Errorf("arxiv: %w", err)
	}
	return item, nil
}

// fetchViaAPI queries arXiv's Atom API for structured metadata, then
// enriches the body via the HTML render or PDF (best-effort — keeps
// abstract-only when both enrichment paths fail).
func (f *Fetcher) fetchViaAPI(ctx context.Context, id string, opts core.Options) (*core.Item, error) {
	q := url.Values{"id_list": {id}}
	endpoint := f.BaseURL + "?" + q.Encode()
	opts.Audit.Logf("arxiv: GET %s", endpoint)

	body, err := core.GetBytes(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("parse atom: %w", err)
	}
	if len(feed.Entries) == 0 {
		return nil, fmt.Errorf("no entry returned for %q", id)
	}
	item := entryToItem(feed.Entries[0], id)
	item.Extra["via"] = "api"

	// Best-effort body enrichment: arxiv.org/html/<id> first (post-
	// 2024 rendered papers), PDF via PDFReader as fallback. Failing
	// either keeps the abstract-only Content — the caller still has
	// title / authors / abstract from the Atom feed.
	if f.EnrichBody {
		if md, source, err := f.fetchBody(ctx, id, opts); err == nil && md != "" {
			item.Content = md
			item.Extra["body_source"] = source
		} else if err != nil {
			opts.Audit.Logf("arxiv: body enrichment failed (keeping abstract-only): %v", err)
		}
	}
	return item, nil
}

// fetchViaJina is the body-only fallback for the rare case the Atom
// API is unreachable. Routes arxiv.org/abs/<id> through r.jina.ai
// and surfaces title / canonical URL / published time from Jina's
// envelope.
func (f *Fetcher) fetchViaJina(ctx context.Context, id string) (*core.Item, error) {
	absURL := "https://arxiv.org/abs/" + id
	res, err := htmlmd.NewJinaReader().ReadFull(ctx, absURL)
	if err != nil {
		return nil, err
	}
	finalURL := absURL
	if res.URL != "" {
		finalURL = res.URL
	}
	return &core.Item{
		Source:      "arxiv",
		Kind:        "paper",
		URL:         finalURL,
		CanonicalID: id,
		Title:       res.Title,
		Summary:     res.Description,
		Published:   parseTime(res.PublishedTime),
		Content:     strings.TrimSpace(res.Content),
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"requested_url": absURL,
			"via":           "jina",
			"anonymous":     true,
		},
	}, nil
}

// fetchBody pulls the full paper body, preferring arXiv's HTML
// render at /html/<id>, falling back to the PDF at /pdf/<id> via
// PDFReader. Returns the markdown body + a source label
// ("html" / "pdf") for audit/debug purposes, or ("", "", err) when
// both paths fail.
func (f *Fetcher) fetchBody(ctx context.Context, id string, opts core.Options) (string, string, error) {
	htmlURL := f.HTMLBase + id
	if available, err := arxivHTMLAvailable(ctx, htmlURL); err == nil && available {
		opts.Audit.Logf("arxiv: HTML render available, fetching %s", htmlURL)
		// Reuse Jina for the HTML path too — the rendered HTML
		// pages are large and Jina's extraction is much cleaner
		// than running the local article extractor over arXiv's
		// MathML-heavy markup.
		md, err := htmlmd.NewJinaReader().Read(ctx, htmlURL)
		if err == nil && strings.TrimSpace(md) != "" {
			return strings.TrimSpace(md), "html", nil
		}
		opts.Audit.Logf("arxiv: HTML fetch failed (%v), falling through to PDF", err)
	}

	pdfReader := f.PDFReader
	if pdfReader == nil {
		pdfReader = htmlmd.DefaultPDFReader()
	}
	if pdfReader == nil {
		return "", "", fmt.Errorf("PDF_READER disabled and HTML render unavailable")
	}
	pdfURL := f.PDFBase + id
	opts.Audit.Logf("arxiv: HTML unavailable, fetching PDF %s", pdfURL)
	md, err := pdfReader.Read(ctx, pdfURL)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(md), "pdf", nil
}

// arxivHTMLAvailable does a cheap HEAD request to /html/<id> to see
// whether arXiv has rendered the paper. Older papers (pre-2024-ish)
// don't have HTML versions — those return 404 and we want to fall
// straight through to the PDF without paying for a body fetch first.
//
// Tight 5s timeout because this is on every arXiv paper fetch's
// critical path; if arXiv is slow we'd rather fall through to the
// PDF (the next attempt will retry HTML on the next call) than
// stall the whole chain.
func arxivHTMLAvailable(ctx context.Context, htmlURL string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, htmlURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

func entryToItem(e atomEntry, id string) *core.Item {
	authors := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if n := strings.TrimSpace(a.Name); n != "" {
			authors = append(authors, n)
		}
	}
	tags := make([]string, 0, len(e.Categories))
	for _, c := range e.Categories {
		if c.Term != "" {
			tags = append(tags, c.Term)
		}
	}
	pdfURL, htmlURL := "", ""
	for _, l := range e.Links {
		switch {
		case l.Rel == "related" && l.Title == "pdf":
			pdfURL = l.Href
		case l.Type == "text/html":
			htmlURL = l.Href
		}
	}
	if pdfURL == "" {
		pdfURL = "https://arxiv.org/pdf/" + id
	}
	if htmlURL == "" {
		htmlURL = "https://arxiv.org/abs/" + id
	}

	return &core.Item{
		Source:      "arxiv",
		Kind:        "paper",
		URL:         htmlURL,
		CanonicalID: id,
		Title:       cleanWhitespace(e.Title),
		Author:      strings.Join(authors, ", "),
		AuthorURL:   "",
		Published:   parseTime(e.Published),
		Summary:     cleanWhitespace(e.Summary),
		Content:     cleanWhitespace(e.Summary),
		Tags:        tags,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"pdf_url": pdfURL,
			"updated": e.Updated,
		},
	}
}

func cleanWhitespace(s string) string {
	// arXiv's Atom wraps abstracts at ~78 cols with newlines; collapse
	// to single spaces so the rendered markdown reads as prose.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
