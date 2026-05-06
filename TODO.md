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
- skills installer

- fast parallel via MCP 
- set parallel gets quote per reader/platform?

- jina as json return
- jina faster browser
- jina search - pagination . json
- jina force read not cached

- anon linkedin , medium  - playwtight ?
- check available / applicable chain names

- linkedin timeline , x timeline , comments
- duplicate body linkedin / filter 
- reformat jina - markdown to article / author etc..
- jina custom prmompt , interprete images
- exclude domains for jina (internal)
- playwright / 
- ytdlp dependency

- chromdp, playwright - reuse
- linkedi Cookie ?

- strip off linkedin - ? ....
- healddes tweet ? not
- better cleaner of html ? jina model , html2md

- medium, article fallbac to og -> hint bridge?
- fail on github  too much queue

- queue . rretries ?
- multiple headless urls - mcp !
- ngrok headless
- should we group bridge, headless, http, jina into transports ?

- Daytone docker / chrome XXX
- Pasword for headless port/ bridge port

- MCP example for bulk in parallel / but what if one hangs
- not write to ledger if fail ?
- yt-dlp as transport ?

- return urls as they finish? Can MCP stream ?
- Support for ledger projects - default - social_fetch via env var
- live events SSE ?
- a little ask for help app 
- progress reporting build download ?

=====
sync to read / wrtite / bookmarks / folder 
tagged manual / aibookmarked
offer ledger etc.. as seperate mcps/skills ?
=====
- Subscription-aware search: social-fetch search --by Karpathy
     resolves to that person's tracked socials and queries each.
     Cleaner UX but doesn't fit MVP scope.
- disable provider - ex chrome

- So social_fetch_fetch isn't short-circuiting on the ledger; it always re-hits the network and overwrites the cached entry.
- Needs testing to make sure

- Find subscritpons - ~ semantic

- Active / Deactivate - it's actually not same subscriptions / topics
- Profiles ?

- Subscrtiptions are runs we want to do Z times

- Topics ? / More for reacher
- should it direct ledger ? influencers and not social fetch?
- Google search via browser possible ? not good idea
=======
- SOcial reviews / web app / agent controlled / kanban review voard for content?
- MCP questions ? add / no
====
- export list influencers ...
- ask questions before adding influencers or others review
====
caching fetch ?
====
sscrrenshot via brdige (logged in)
====
docker container for different deamons
====
- single container, debian+chromium, headless + ledger daemons exposed on 0.0.0.0:5556/5557, bridge skipped, MCP-as-SSE on 5558
  - volume mount at /data for ledger persistence
  - Dockerfile + docker-compose.yml (compose for local dev convenience even if it's one service) + .dockerignore
  - README section + make docker-build / make docker-run targets

====
/monitor , /health endpoints on daemons
====
ledger fetch autostore ?????
=====
youtube summaries over mcp - too long or too much text on output

===
defaulkt mem/cpu of daytona

====
namespace daytona 
====
browser pool not via mcp ?
expose
====
cleanup bookmarks
====
social-agent
====
social-researcher
====
bridge -> social-browser ?
socia-fetch --browser headless,bridge
local -> chromedp
====
up / shutdown / resume
====
daytnoa autoscale
====
queue requests instead of dropping - jobs/ids
====
I now want to build a social-agent : it can run sandboxed claude-code sessions.
First provider is docker for this. It will have its own dockerfile to include the claude-code requirements.
We might switch coding agents, let's for now call those harness = claude-code

Later we might do the same for daytona provider.
Also should we rename the existing one to Dockerfile-browser or simlar? Dockerfile-agent

That social-agent image will contain our social skills binaries.
I should be able to set the env vars for i such as the social-browser pool
I need to also set the anthropic-api-envs / or it reads it from the .env

Look at addt in @~/dev/dclaude/ for claude logic for running it in a container. Including how to extract claude-code credentials

I will later extend this into a social-researcher / or other more specialize agents which will extend this to specific context/system prompt and agent tooling.

======
socual-browser test/live test
====
social-agent / sandbox local provider
====
ledger also store converstatopn of agents
memory ???
graph ???
====

also workspace , for debugging ?
recycle, session reumse