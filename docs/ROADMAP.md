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
- [ ] Enforcing `challenge`: comes with M3. Until then such requests are counted and let through.

## M3: Challenge
- [ ] Signed token and pass cookie with expiry
- [ ] Proof-of-work challenge with adjustable difficulty
- [ ] Challenge without JavaScript
- [ ] Challenge page: neutral, accessible, German and English, customer branding
- [ ] Accessibility check with automated tool plus keyboard and screen reader pass

## M4: Crawler classes and identity
- [ ] Crawler data format and loader
- [ ] Verification by published IP ranges, refreshed in the background
- [ ] Verification by reverse DNS, cached
- [ ] First data set, every entry with source and date
- [ ] Presets: block training crawlers, allow AI search, allow user fetches, allow search engines

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
- [ ] Rate limiting action
- [ ] Docker image, systemd unit, .deb package
- [ ] Shared storage backend for multiple instances

## M9: Ready for customers
- [ ] Admin documentation in German and English
- [ ] Security review by a second agent that has not seen the code being written
- [ ] Load test on a Raspberry Pi and on a small VPS
- [ ] Privacy documentation (what is stored, for how long)

## Later
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
