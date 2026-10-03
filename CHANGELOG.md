# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- Attribution line. Every page Xibalba shows to visitors ends with a small
  "Protected by Xibalba" line linking to the project and its sponsor page.
  New setting `pages.attribution` (default `true`).
- Sponsor license (`license.file`). A signed license file, checked offline,
  unlocks `pages.attribution: false`, `pages.operator` and `pages.texts`.
  An expired license never stops Xibalba: after a 30-day grace period the
  pages return to their standard form. See `docs/SPONSORS.md`.
- Health component `license` (only when a license file is configured).
- `cmd/xibalba-license`, the maintainer's tool for issuing and checking licenses.

- Tested example configurations for nginx (`examples/nginx/xibalba.conf`)
  and Caddy (`examples/caddy/Caddyfile`), and `make webserver-check`, which
  runs Xibalba behind both.

- Security check (challenge). A client that a rule sends to the check gets a
  page whose script solves a proof of work; visitors without JavaScript can
  wait and press a button instead. A correct answer earns a signed pass
  cookie. Tasks and passes are tied to the client's network and user agent,
  nothing is stored on the server, and the website never sees the cookie.
  See `docs/CHALLENGE.md`.
- New `challenge` section: `difficulty`, `no_javascript`, `wait`,
  `challenge_lifetime`, `pass_lifetime`, `bind_network`, `key_file`,
  `cookie_name`.
- Signing key in a file (`challenge.key_file`), created at the first start
  with mode 600, so passes survive restarts and instances can share it.
- Addresses under `/.xibalba/` are reserved for Xibalba.
- `challenge` counters (`served`, `passed`, `solved`, `failed`) in `/decisions`.
- Ten new page texts for the security check, all replaceable through `pages.texts`.
- `test/browser/check.py`: checks the visitor pages in a real browser, with
  and without JavaScript, by keyboard, and with an accessibility checker.
- German operator handbook: `docs/de/HANDBUCH.md`.

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
- New `pages` section: `operator` puts your name on the visitor pages,
  `contact` adds a contact line to the block page, `default_language` sets
  the language for visitors without a supported preference, and `texts`
  replaces any text per language.

### Changed

- **`pages.operator` and `pages.texts` now need a sponsor license.** Without
  `license.file`, a configuration that uses them is refused at start-up with
  a message that says so. `pages.contact` and `pages.default_language` stay free.

- A website outage is logged once when it starts and once when it ends,
  instead of once per failed request. The requested path is no longer logged.
- The action `challenge` is now enforced. (For one development step it was
  accepted but only counted.)

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
