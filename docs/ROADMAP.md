# Xibalba: roadmap

Each milestone ends with something that runs and is tested. Do them in order.

## M0: Skeleton (done)
- [x] Go module, layout from CLAUDE.md, Makefile (`build`, `test`, `lint`, `run`)
- [x] Config loading with validation and clear errors
- [x] Health endpoint, structured logging
- [x] CI: vet, tests with race detector, cross-compile for amd64 and arm64
- [x] Component supervisor with failure attribution and rollback
- [x] Repository documentation: README, architecture, configuration, development

## M1: Reverse proxy
- [ ] Proxy to one upstream, correct handling of headers, websockets, streaming, timeouts
- [ ] Trusted-proxy handling for the client IP
- [ ] Integration test harness with a fake upstream
- [ ] Dry-run mode flag (everything passes, decisions are only counted)

## M2: Rule engine
- [ ] Rule model: matchers (user agent, path, header, IP range), actions (allow, deny, challenge, weigh)
- [ ] Weight thresholds
- [ ] Expression matcher for combined conditions
- [ ] Rule set import, validation with line-accurate errors
- [ ] Benchmark and hostile-input tests

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
- Hosted variant
- CMS plugins
- ASN-based lists and reputation feeds
