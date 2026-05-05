// Package github fetches repository metadata, the README, and recent
// releases via GitHub's public REST API. If a GITHUB_TOKEN env var is set
// it is used for higher rate limits, but auth is not required.
//
// Single-method today (`api`); routes through fetchchain for env-var
// consistency. SOCIAL_FETCH_CHAIN_GITHUB exists as a future-proof slot.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/fetchchain"
)

const defaultBaseURL = "https://api.github.com"

// Fetcher pulls a repo overview from GitHub.
type Fetcher struct {
	BaseURL string
	// Token, if non-empty, is sent as a Bearer token. Defaults to
	// $GITHUB_TOKEN at fetch time.
	Token string
}

func New() *Fetcher {
	return &Fetcher{BaseURL: defaultBaseURL}
}

func (Fetcher) Name() string { return "github" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Host != "github.com" && u.Host != "www.github.com" {
		return false
	}
	owner, repo := splitOwnerRepo(u.Path)
	return owner != "" && repo != ""
}

var ownerRepoRE = regexp.MustCompile(`^/?([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)`)

func splitOwnerRepo(path string) (string, string) {
	m := ownerRepoRE.FindStringSubmatch(path)
	if len(m) != 3 {
		return "", ""
	}
	return m[1], strings.TrimSuffix(m[2], ".git")
}

type repoInfo struct {
	Name        string   `json:"name"`
	FullName    string   `json:"full_name"`
	Description string   `json:"description"`
	Homepage    string   `json:"homepage"`
	HTMLURL     string   `json:"html_url"`
	Default     string   `json:"default_branch"`
	Language    string   `json:"language"`
	Topics      []string `json:"topics"`
	License     *struct {
		SPDX string `json:"spdx_id"`
	} `json:"license"`
	Stars      int    `json:"stargazers_count"`
	Forks      int    `json:"forks_count"`
	OpenIssues int    `json:"open_issues_count"`
	Watchers   int    `json:"watchers_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	PushedAt   string `json:"pushed_at"`
	Private    bool   `json:"private"`
	Fork       bool   `json:"fork"`
	Archived   bool   `json:"archived"`
	Owner      struct {
		Login   string `json:"login"`
		Type    string `json:"type"`
		HTMLURL string `json:"html_url"`
	} `json:"owner"`
}

type readmeInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type releaseInfo struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Body        string `json:"body"`
}

var defaultChain = []fetchchain.Method{fetchchain.MethodAPI}
var supportedMethods = map[fetchchain.Method]bool{fetchchain.MethodAPI: true}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	ctx = core.WithAudit(ctx, opts.Audit)
	chain := fetchchain.Resolve(fetchchain.FromEnv("github"), defaultChain, supportedMethods)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodAPI: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaAPI(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "github", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	return item, nil
}

func (f *Fetcher) fetchViaAPI(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	owner, repo := splitOwnerRepo(u.Path)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("cannot extract owner/repo from %q", raw)
	}

	// The three GitHub endpoints are independent — repo info, README,
	// and recent releases — so fetch them concurrently. GitHub's REST
	// API rate-limits per hour (60 anon / 5000 authed), not per second,
	// so 3 parallel requests cost the same budget as 3 sequential and
	// cut latency to the slowest of the three. README + releases stay
	// best-effort; only repo-info failure aborts. Three goroutines is
	// well below the transport's per-host connection cap.
	var (
		info     repoInfo
		readme   readmeInfo
		releases []releaseInfo
		repoErr  error
		wg       sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		repoErr = f.api(ctx, fmt.Sprintf("/repos/%s/%s", owner, repo), &info)
	}()
	go func() {
		defer wg.Done()
		_ = f.api(ctx, fmt.Sprintf("/repos/%s/%s/readme", owner, repo), &readme)
	}()
	go func() {
		defer wg.Done()
		_ = f.api(ctx, fmt.Sprintf("/repos/%s/%s/releases?per_page=5", owner, repo), &releases)
	}()
	wg.Wait()

	if repoErr != nil {
		return nil, fmt.Errorf("repo: %w", repoErr)
	}
	readmeText := decodeReadme(readme)

	published := parseGHTime(info.CreatedAt)
	tags := append([]string(nil), info.Topics...)
	if info.Language != "" {
		tags = append(tags, info.Language)
	}

	licenseID := ""
	if info.License != nil {
		licenseID = info.License.SPDX
	}

	item := &core.Item{
		Source:      "github",
		Kind:        "repo",
		URL:         info.HTMLURL,
		CanonicalID: info.FullName,
		Title:       info.FullName,
		Author:      info.Owner.Login,
		AuthorURL:   info.Owner.HTMLURL,
		Published:   published,
		Summary:     info.Description,
		Content:     readmeText,
		Score:       info.Stars,
		Tags:        tags,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"homepage":        info.Homepage,
			"default_branch":  info.Default,
			"forks":           info.Forks,
			"open_issues":     info.OpenIssues,
			"watchers":        info.Watchers,
			"license":         licenseID,
			"private":         info.Private,
			"fork":            info.Fork,
			"archived":        info.Archived,
			"updated_at":      info.UpdatedAt,
			"pushed_at":       info.PushedAt,
			"recent_releases": summarizeReleases(releases),
		},
	}
	return item, nil
}

func summarizeReleases(rs []releaseInfo) []map[string]any {
	out := make([]map[string]any, 0, len(rs))
	for _, r := range rs {
		out = append(out, map[string]any{
			"tag":          r.TagName,
			"name":         r.Name,
			"published_at": r.PublishedAt,
			"prerelease":   r.Prerelease,
		})
	}
	return out
}

func decodeReadme(r readmeInfo) string {
	if r.Content == "" {
		return ""
	}
	if r.Encoding == "base64" {
		// GitHub wraps base64 every 60 chars.
		clean := strings.ReplaceAll(r.Content, "\n", "")
		b, err := base64.StdEncoding.DecodeString(clean)
		if err == nil {
			return string(b)
		}
	}
	return r.Content
}

func parseGHTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

// api builds a request, attaches auth + accept headers, decodes JSON.
// We don't reuse core.GetJSON because GitHub has its own Accept value and
// optional bearer auth.
func (f *Fetcher) api(ctx context.Context, path string, v any) error {
	u := f.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := f.token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s: HTTP %d: %s", u, resp.StatusCode, core.HTTPErrorBody(resp))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (f *Fetcher) token() string {
	if f.Token != "" {
		return f.Token
	}
	return os.Getenv("GITHUB_TOKEN")
}
