# Xibalba: roadmap

Each milestone ends with something that runs and is tested. Do them in order.

## M0: Skeleton (done)
- [x] Go module, layout from CLAUDE.md, Makefile (`build`, `test`, `lint`, `run`)
- [x] Config loading with validation and clear errors
- [x] Health endpoint, structured logging
- [x] CI: vet, tests with race detector, cross-compile for amd64 and arm64
- [x] Component supervisor with failure attribution and rollback
- [x] Repository documentation: README, architecture, configuration, development

## M1: Reverse proxy (done)
- [x] Proxy to one upstream, correct handling of headers, websockets, streaming, timeouts
- [x] Trusted-proxy handling for the client IP
- [x] Integration test harness with a fake upstream
- [x] Neutral error page when the website is unreachable; upstream health

## M2: Rule engine (done)
- [x] Dry-run mode (everything passes, decisions are only counted)
- [x] Rule model: matchers (user agent, path, host, method, header, IP range), actions (allow, deny, challenge, weigh)
- [x] Weight thresholds
- [x] Combined conditions with `all`, `any` and `not` (instead of a text expression language, see DECISIONS.md)
- [x] Rule set import, validation with line-accurate errors
- [x] Benchmark and hostile-input tests
- [x] Enforcement in the request path, accessible block page, decision counters at `/decisions`
- [x] Enforcing `challenge` (done with M3)

## M3: Challenge (done except branding and a screen reader pass)
- [x] Signed token and pass cookie with expiry
- [x] Proof-of-work challenge with adjustable difficulty
- [x] Challenge without JavaScript
- [x] Challenge page: neutral, accessible, German and English, operator name and all texts adjustable
- [x] Accessibility check with an automated tool (axe: no findings) and by keyboard only, in a real browser
- [x] German operator handbook
- [x] Attribution line and sponsor license (owner's request, added after M3)
- [x] Tested nginx and Caddy configurations (owner's request, added after M3)
- [ ] Customer branding: logo and accent colour on the visitor pages (sponsor feature)
- [ ] A pass with a real screen reader (NVDA or VoiceOver). Not possible in the development environment; needs a person.

## M4: Crawler classes and identity
- [x] Crawler data format and loader
- [x] Verification by published IP ranges, refreshed in the background
- [x] Verification by reverse DNS, cached
- [x] First data set, every entry with source and date
- [x] Presets: block training crawlers, allow AI search, allow user fetches, allow search engines
- [x] `crawler` rule condition; rules that favour a crawler by name alone are refused
- [x] Own crawler definition files
- [ ] Confirm the downloads against the operators' live lists (not reachable from the development environment)
- [ ] Amazon and Meta crawlers: find a verification method Xibalba can use
- [ ] Per-network limit on queued reverse DNS lookups

## M5: Limits and detection
Bots that pretend to be browsers cannot be told by their name. Owner's
decision (2026-10-03): all of this comes before statistics.
- [x] Request limits per client and period, switchable, with a list of exempt addresses
- [x] Preset "check everything that looks like a browser", with exceptions for `/.well-known`, `robots.txt`, `favicon.ico` and feeds; ready-made exceptions for git clients and feed readers
- [ ] Ready-made exceptions for container registry clients, small browsers and uptime monitors (their user agents must be looked up, not written from memory)
- [x] Plausibility of browser headers, as weights: no language, no Accept, headless browser
- [ ] Further plausibility checks that depend on HTTPS (client hints, fetch metadata); need a condition for the scheme first
- [x] Trap link that only a careless crawler follows; rule condition `trapped`, preset `block-trapped`
- [x] Maze of worthless pages behind the trap link, as an option, off by default
- [ ] Patterns: one client fetching very many different pages, or the same page again and again
- [x] Country condition in rules, with a country database the operator supplies or the free DB-IP database downloaded on request
- [ ] Confirm the DB-IP download against the live server (not reachable from the development environment)
- [x] Security review of the milestone by a second agent (2026-10-04), and its findings fixed:
  - [x] High: allow presets could be bypassed with roundabout addresses. Allowing rules with a path condition now need an address sent in plain form; feed and git presets tightened.
  - [x] High: the trap could be poisoned by other websites. Links are now per client with a keyed check value.
  - [x] Medium: requests let through by an allow rule were not counted by the limits. Now a choice per rule.
  - [x] Medium: `Retry-After` too short.
  - [x] Medium: a damaged country file stopped the start.
  - [x] Medium: warning when all visitors would share one limit.
  - [x] Low: trap lookup only when a rule asks, read lock; reload right after a download; bounded read of the country file; download address not echoed; health entries for `limits` and `trap`; privacy text names the 30 days.
  - [ ] Medium, documented only: a `challenge` limit does not restrain a client that holds a pass (advice: add a `deny` limit). Owner to say whether an automatic escalation is wanted.
  - [ ] Low, open: evict the oldest instead of any entry when the trap or limit table is full; cap the country file size lower for small machines.
- [x] Second review of the fixes (2026-10-04), and its findings fixed: strictness also for "everything except this path" and for addresses encoded twice; request targets that are not paths refused; `keep-internet-working` without a query; warning also for the trap and for listening on all addresses; `exempt_from_limits` only by address or verified crawler; file problems still reported with `countries.download`.
  - [ ] Low, open: `allow-feeds` and `allow-git-clients` match any address ending the right way (documented); regular expressions in presets fold some non-ASCII letters; a damaged country file is read again every minute until replaced.

## M6: Statistics
- [ ] Aggregated counters per hour: by action, rule, crawler, network
- [ ] Embedded storage with retention
- [ ] Prometheus metrics endpoint

## M7: Web interface, read side
- [ ] Login, sessions, localhost by default
- [ ] Dashboard: allowed, challenged, denied over time; top crawlers; top rules
- [ ] Accessible and usable on a phone

## M8: Web interface, write side
- [ ] Preset switches
- [ ] IP block and allow lists with expiry and notes
- [ ] Rule editor with validation and request test box
- [ ] Versioned config with rollback

## M9: Deployment
- [ ] Verdict mode for nginx, Caddy, Traefik
- [ ] Docker image, systemd unit, .deb package
- [ ] Shared storage backend for multiple instances

## M10: Ready for customers
- [ ] Admin documentation in German and English, as a documentation site with the breadth of the Anubis documentation and better (owner's request, 2026-10-03):
  - [ ] Design: how the check works, with diagrams
  - [ ] Guides per environment: nginx and Caddy (exist), Apache, HAProxy, Traefik, Docker Compose, Kubernetes, behind Cloudflare, Windows
  - [ ] Guides per application: WordPress, pages that load parts of themselves (HTMX and similar), Git hosting (Gitea/Forgejo), container registries
  - [ ] Advice for sites with their own Content-Security-Policy
  - [ ] For visitors: "Why do I see this check?", questions and answers, browser extensions known to break the check
  - [ ] Questions and answers for operators
- [ ] Imprint (Impressum) and privacy notice links on the visitor pages, set in the configuration (Anubis has this; German operators need it)
- [ ] Link previews (Open Graph): let preview fetchers see title and description of a protected page
- [ ] Allowed redirect domains for setups with several host names
- [ ] Security review by a second agent that has not seen the code being written
- [ ] Load test on a Raspberry Pi and on a small VPS
- [ ] Privacy documentation (what is stored, for how long)

## Later
- Challenge method and difficulty selectable per rule or threshold
- English edition of the operator handbook
- Packaged releases, so operators do not need Go to install (part of M8)
- A path test that means "this directory and everything under it", so `/admin` does not also match `/administrator`
- A text expression language for rules, if `all`/`any`/`not` turn out not to be enough
- Configurable status code and text for the block page
- Reloading rules without a restart
- TLS termination on the public listener
- Limits on request body size and on slow request bodies
- Several upstreams, selected by host name
- `Forwarded` (RFC 7239) as an alternative to `X-Forwarded-For`
- Customer branding and translations for the "website unavailable" page (with the challenge page in M3)
- Hosted variant
- CMS plugins
- ASN-based lists and reputation feeds
- From the comparison in `docs/PARITY.md`: server load as a condition; a solved task usable only once; memory-hard proof of work; check that a style sheet was loaded; token the web server in front can verify; path prefix; unix socket and TLS options towards the website; headers telling the website which rule decided; log file with rotation; more languages
- Go through the list "Tests to take from Anubis' published security history" in `docs/PARITY.md`
- Serving a robots.txt generated from the crawler classes
- Tool that turns a robots.txt into rules; tool that turns an IP block list into a rule file
- Optional file of addresses that hit a trap link, for fail2ban; off by default, with a size limit (stores addresses: needs the privacy treatment of CLAUDE.md section 4)
- Recognising headless browsers by their behaviour
