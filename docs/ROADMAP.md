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

## M5: Statistics
- [ ] Aggregated counters per hour: by action, rule, crawler, network
- [ ] Embedded storage with retention
- [ ] Prometheus metrics endpoint

## M6: Web interface, read side
- [ ] Login, sessions, localhost by default
- [ ] Dashboard: allowed, challenged, denied over time; top crawlers; top rules
- [ ] Accessible and usable on a phone

## M7: Web interface, write side
- [ ] Preset switches
- [ ] IP block and allow lists with expiry and notes
- [ ] Rule editor with validation and request test box
- [ ] Versioned config with rollback

## M8: Deployment
- [ ] Verdict mode for nginx, Caddy, Traefik
- [ ] Rate limiting: limits per address or network and time window, switchable, with a list of exempt addresses (owner's request, 2026-10-03)
- [ ] Country condition in rules (block or allow by country), with a country database the operator supplies and updates; licence of the database to be checked (owner's request, 2026-10-03)
- [ ] Docker image, systemd unit, .deb package
- [ ] Shared storage backend for multiple instances

## M9: Ready for customers
- [ ] Admin documentation in German and English
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
- Detecting patterns: one client fetching very many different pages, or the same page again and again
- Trap links that only a crawler follows (hidden from people, forbidden in robots.txt)
- Plausibility checks on browser headers (a "Chrome" that sends no Chrome headers), as weights
- Serving a robots.txt generated from the crawler classes
- Preset "check everything that looks like a browser", with exceptions for `/.well-known`, `robots.txt`, `favicon.ico` and feeds (Anubis' default behaviour, per its documentation as supplied by the owner on 2026-10-03)
- Ready-made exceptions for programs that are not browsers: git clients, container registry clients, feed readers
- Tool that turns a robots.txt into rules; tool that turns an IP block list into a rule file
- Optional file of addresses that hit a trap link, for fail2ban; off by default, with a size limit (stores addresses: needs the privacy treatment of CLAUDE.md section 4)
- Maze of worthless pages for crawlers that follow trap links ("dataset poisoning" in Anubis): owner to decide whether this fits a product for public authorities
- Recognising headless browsers by their behaviour
