- Homebrew / install

- publish as marketplace - docs
- notes on why a mcbp - 

- analytics use
- extension permissions limit
- turn it into a library

- set/list secrets / vault
- images ? media ?

- firecrawl as parallel Reader to Jina — `internal/render/htmlmd/firecrawl.go`
  next to `jina.go`. Triggers: `HTML2MD_READER=firecrawl` for primary, OR
  `FIRECRAWL_API_KEY` set → chain as last-resort fallback after HTTP /
  bridge / Jina. Why: harder paywalls / JS-heavy sites / aggressive
  anti-bot than Jina handles. 500 credits free, then per-credit. Skip
  `/crawl` multi-page endpoint for v1. Defer until a concrete site
  fails Jina too — otherwise it's a new SaaS dep for marginal gain.

- npx skills add support — rename skill/ → skills/ (Vercel CLI convention),
  unify with extensions/claude-code/skills/social-fetch/ (single source of truth,
  bare `social-fetch` on PATH), document binary-on-PATH prerequisite

- man packages ? linux distro pkgs ?
- curl installer ?
- passwrod browser connection/secret
- backup ledger