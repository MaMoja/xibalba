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
- [x] Ready-made exception for container registry clients (`allow-registry-clients`, by the addresses of the OCI distribution specification). Small browsers need none: they get through with the path without JavaScript.
- [x] Third review (2026-10-04), of the limits on pages, the registry preset and the metrics; findings fixed: pages are told by the website's answer, the same pages across periods count once, small limits exact, trap report without walking the table, metrics hardening.
  - [ ] Open: a setting for query parameters to ignore when telling pages apart; these fixes have not been reviewed again.
- [ ] Uptime monitors as verified crawler definitions (their operators publish addresses); moved to Later
- [x] Plausibility of browser headers, as weights: no language, no Accept, headless browser
- [ ] Further plausibility checks that depend on HTTPS (client hints, fetch metadata); need a condition for the scheme first
- [x] Trap link that only a careless crawler follows; rule condition `trapped`, preset `block-trapped`
- [x] Maze of worthless pages behind the trap link, as an option, off by default
- [x] Patterns: one client fetching very many different pages (`count: pages`); the same page again and again is covered by limits on requests
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
  - [x] Medium: a `challenge` limit does not restrain a client that holds a pass. The owner chose an option: `deny_at` per limit, off by default.
  - [ ] Low, open: evict the oldest instead of any entry when the trap or limit table is full; cap the country file size lower for small machines.
- [x] Second review of the fixes (2026-10-04), and its findings fixed: strictness also for "everything except this path" and for addresses encoded twice; request targets that are not paths refused; `keep-internet-working` without a query; warning also for the trap and for listening on all addresses; `exempt_from_limits` only by address or verified crawler; file problems still reported with `countries.download`.
  - [ ] Low, open: `allow-feeds` and `allow-git-clients` match any address ending the right way (documented); regular expressions in presets fold some non-ASCII letters; a damaged country file is read again every minute until replaced.

## M6: Statistics
- [x] Aggregated counters per hour: by action, rule, crawler, limit and trap
- [x] Counters per network of origin (IPv4 /24, IPv6 /48), as an option, off by default, own time limit (owner's decision 2026-10-04)
- [x] English guides (getting started, operations, privacy, FAQ, visitors) and the wiki, built from `docs/`
- [x] Storage on disk with a time limit (plain files, one line per hour; no database)
- [x] Security review of the milestone by a second agent (2026-10-04); no high finding. Fixed: counts lost in a flood of networks, network counts left on disk after switching off, time limit exact to the day instead of the month, running hour reduced earlier, `Retry-After` the longest of the exceeded limits, `deny_at` visible in metrics and statistics, an hour written twice after a failed sync, an over-long line ending the reading of a file, warning for an operations listener that is not local, workflow token not kept by the checkout.
  - [ ] Low, open: GitHub actions pinned by commit instead of tag; a fairer table than first-come for networks within one minute.
- [x] Prometheus metrics endpoint (format checked by tests; not yet tried against a running Prometheus)

## M7: Web interface, read side
- [x] An option, off by default; switched off, nothing of it exists (owner's decision 2026-10-04)
- [x] Login, sessions, localhost by default; password set with `xibalba -set-password`
- [x] Overview: let through, checked, blocked over time (24 hours, 7 days, 30 days); rules; crawlers; security check; limits and trap; state of the parts
- [x] Accessible and usable on a phone: no script, chart with the same numbers as a table, checked with axe (WCAG 2.1 A and AA) in light and dark, at 375 px width
- [x] Security review of the milestone by a second agent (2026-10-04); no high finding. Fixed: wrong passwords sent side by side all got checked (now counted first, and at most four wait); requests under a foreign host name were answered (now refused, `admin.hostnames`); cookie for HTTPS (`admin.secure_cookie`); Ctrl-C at the password prompt left the terminal without echo; listeners on the same port under different spellings; warning for a password file others can read.
  - [ ] Low, documented: behind a web server all sign-in attempts share one address and one wait.

## M8: Web interface, write side
- [x] Its own switch, `admin.allow_changes`, off by default (owner's decision 2026-10-04)
- [x] Preset switches, in force at once
- [x] IP block and allow lists with expiry and notes
- [x] Security review of this part by a second agent (2026-10-04); no high finding. Fixed: a listed allow beat a more specific listed block (block now comes first); expired entries stayed in the file; reserved rule names; this machine's and the trusted proxies' addresses cannot be let through; notes counted in characters, control characters refused; `changes` in `/healthz`.
- [x] Changes kept in their own file; the configuration file is never rewritten; rule set replaced while running
- [x] Security review of the editor by a second agent (2026-10-04). Two high findings, both fixed before any release: a small rule text with YAML aliases could use gigabytes of memory, and the changes file could grow past the size it is read back with. Also fixed: slow rule sets through large regular expressions (limits for every rule set), a crash of the YAML reader on a tagged value, going back to the wrong version after another change, name lookups started from the box to try a request.
- [x] Rule editor with validation (problems named with their line) and a box to try a request, saved or not
- [x] Earlier versions of presets and own rules (ten), with a way back; the configuration file itself is never rewritten, so there is nothing of it to version

## M9: Deployment
- [ ] Verdict mode for nginx, Caddy, Traefik
- [x] Dockerfile and Compose example (built and started in CI), systemd unit, Kubernetes example (not run in a cluster)
- [ ] Published images; .deb and .rpm packages
- [ ] Trusted proxies loaded from a CDN's published list
- [ ] Shared storage backend for multiple instances

## M10: Ready for customers
- [ ] Admin documentation in German and English, as a documentation site with the breadth of the Anubis documentation and better (owner's request, 2026-10-03):
  - [ ] Design: how the check works, with diagrams
  - [x] Guides per environment (`docs/ENVIRONMENTS.md`): nginx, Caddy, Apache, HAProxy, Traefik with tested examples; Docker Compose tested in CI; systemd; Kubernetes, CDN and Windows described but not tested
  - [ ] Guides per application: WordPress, pages that load parts of themselves (HTMX and similar), Git hosting (Gitea/Forgejo), container registries
  - [ ] Advice for sites with their own Content-Security-Policy
  - [ ] For visitors: "Why do I see this check?", questions and answers, browser extensions known to break the check
  - [ ] Questions and answers for operators
- [x] Link previews (Open Graph): the challenge page carries the preview tags of the page that was asked for (`previews`, off by default); fetched in the background, or fixed in the configuration
- [x] HTTP status of the security check and the block page can be chosen (`pages.status`)
- [x] Imprint and privacy links on the visitor pages
- [ ] Allowed redirect domains for setups with several host names
- [ ] Security review by a second agent that has not seen the code being written
- [ ] Load test on a Raspberry Pi and on a small VPS
- [ ] Privacy documentation (what is stored, for how long)
- [x] Releases without Go on the target machine: archives and Debian packages for amd64, arm64 and armhf, a container image for the same three, checksums; built by a workflow that installs the package and starts the service under systemd before anything is published. Two builds of one commit give identical files.

## M9: Kinds of security check (owner's request 2026-10-04)
- [x] Methods `script`, `wait`, `refresh` besides `pow`
- [x] Extra checks `css` and `headless`
- [x] Kind of check, difficulty, wait and extra checks per rule and per threshold; a pass counts for what it was earned with
- [x] Tried in a real browser: every method, both extra checks (the automated test browser is caught by `headless`), accessibility
- [x] Security review by a second agent (2026-10-04); no high finding. Fixed: a pass earned by waiting at the button counted like one earned by calculating; a wait could be up to a second short; the page after a report of automation offered no way to try again, and the comparison of the browser's name could lock out real browsers (removed); a blocked style sheet made the page try for ever; a key of its own for the style sheet value.
- [ ] Conditions by network operator (AS number) and address lists from files, for VPN and hosting networks
- [ ] Proof of work in WebAssembly: decided against for now, see DECISIONS.md

## Later
- Challenge method and difficulty selectable per rule or threshold
- Counts per provider (AS number) besides counts per network; needs a second database file
- Packages in the rpm format; signed releases (provenance)
- A path test that means "this directory and everything under it", so `/admin` does not also match `/administrator`
- A text expression language for rules, if `all`/`any`/`not` turn out not to be enough
- Reloading rules without a restart
- TLS termination on the public listener
- Limits on request body size and on slow request bodies
- Several upstreams, selected by host name
- `Forwarded` (RFC 7239) as an alternative to `X-Forwarded-For`
- Customer branding and translations for the "website unavailable" page (with the challenge page in M3)
- Hosted variant
- CMS plugins
- ASN-based lists and reputation feeds
- Uptime monitors (UptimeRobot and similar) as crawler definitions of a class of their own, verified by their published addresses, with an allow preset
- From the comparison in `docs/PARITY.md`: server load as a condition; a solved task usable only once; memory-hard proof of work; check that a style sheet was loaded; token the web server in front can verify; path prefix; unix socket and TLS options towards the website; headers telling the website which rule decided; log file with rotation; more languages
- Go through the list "Tests to take from Anubis' published security history" in `docs/PARITY.md`
- Serving a robots.txt generated from the crawler classes
- Tool that turns a robots.txt into rules; tool that turns an IP block list into a rule file
- Optional file of addresses that hit a trap link, for fail2ban; off by default, with a size limit (stores addresses: needs the privacy treatment of CLAUDE.md section 4)
- Recognising headless browsers by their behaviour
