# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- Rule engine. Rules match on user agent, path, host, method, headers and
  client address, combined with `all`, `any` and `not`; actions are `allow`,
  `deny`, `challenge` and `weigh`. Weights add up to a score that thresholds
  turn into an action. See `docs/RULES.md`.
- New `rules` section: `dry_run`, `default_action`, `on_error`, `thresholds`,
  `files`, `list`. With the defaults nothing is blocked.
- Importable rule files (`rules.files`) and a commented example in
  `examples/rules/basic.yaml`.
- Rule mistakes are reported at start-up with file, line and fix, including
  in imported files.
- Paths are normalised before rules are tested, so `//admin`, `/x/../admin`,
  `/%61dmin` and `/admin;x` cannot dodge a rule on `/admin`.
- Dry-run mode: decisions are counted but nothing is blocked.
- `GET /decisions` on the operations listener: how often each rule decided,
  without any data about visitors.
- "Request blocked" page with status 403 and a reference that identifies the
  rule. Visitor pages now follow the visitor's `Accept-Language`, offer the
  other language on the same page, and are sent with a
  Content-Security-Policy that forbids scripts and outside resources.
- Health component `rules`.

### Changed

- A website outage is logged once when it starts and once when it ends,
  instead of once per failed request. The requested path is no longer logged.
- The action `challenge` is accepted in rules, but until the challenge is
  built such requests are counted and let through.

### Added earlier

- Reverse proxy to one upstream website, including websockets and streamed
  responses. New required setting `upstream.url`; new settings
  `upstream.preserve_host`, `upstream.dial_timeout`, `upstream.response_header_timeout`.
- Public listener with `server.listen`, `server.read_header_timeout`, `server.idle_timeout`.
- Real client address resolution with `server.trusted_proxies`. Forwarding
  headers from untrusted clients are discarded; `X-Forwarded-For`,
  `X-Forwarded-Proto`, `X-Forwarded-Host` and `X-Real-IP` are rebuilt for the website.
- Neutral, accessible, bilingual page with status 502 or 504 when the website
  cannot be reached. It reveals no internal address.
- Health components `public` and `upstream`. An unreachable website is
  reported as `degraded` without failing the health check.
- Configuration file with full validation. Errors name the file, line and
  setting and say how to fix it; all problems are reported together.
- `-check` flag to validate a configuration file without starting.
- Operations listener with `/healthz` (per-component health) and `/version`.
- Component supervisor: ordered start, reverse-order stop, rollback when a
  component fails to start, and failures attributed to the component by name.
- HTTP listeners with timeouts, header size limit and per-request panic recovery.
- Structured logging in JSON or text with a `component` attribute.
- Graceful shutdown on SIGINT and SIGTERM with a configurable timeout.
