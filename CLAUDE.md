# Xibalba: working rules for the coding agent

Xibalba is a self-hosted reverse proxy that protects websites from AI crawlers and
other abusive bots. Target customers are public authorities and small firms in the
German-speaking region, so it must look serious, be accessible, and be privacy-friendly.
It is a clean rebuild inspired by Anubis (TecharoHQ), not a fork.

Read `docs/SPEC.md` for what to build, `docs/ROADMAP.md` for the order, and
`docs/DECISIONS.md` for choices already made. Do not reopen a logged decision
without telling the owner why.

## 1. Clean rebuild

- Never copy code, config text, rule files, translations or images from Anubis or
  any other project. Reading their public documentation to understand behaviour is fine.
- Every third-party dependency needs a licence check (MIT, BSD, Apache-2.0 or
  similar only) and one line in `docs/DECISIONS.md`.

## 2. Stack

- Go, one static binary, no cgo, so it cross-compiles to linux/amd64 and linux/arm64
  (it must run well on a Raspberry Pi).
- Standard library first. Add a dependency only when writing it ourselves would be
  clearly worse, and log why.
- Web interface: server-rendered `html/template` plus small plain JavaScript,
  embedded in the binary with `embed`. No Node build chain, no CDN, no external fonts.
- Statistics storage: embedded, pure-Go database. No external service required.
- Configuration: one YAML file, validated at start-up, with error messages that
  name the file, the line and the fix.

## 3. Security (this program sits in front of other people's websites)

- Never trust `X-Forwarded-For` or similar headers unless the sender is a
  configured trusted proxy.
- Challenge state lives on the server or in a signed token. Never trust anything
  the client sends back without verifying the signature and expiry.
- Compare secrets in constant time. Use `crypto/rand` for all randomness.
- Validate every redirect target against the protected host to prevent open redirects.
- The admin interface listens on localhost by default and always requires login.
- No secrets, cookies or tokens in logs.
- If Xibalba itself fails (bad rule, storage error), the behaviour is a configured
  choice: `fail_open` or `fail_closed`. Never crash the proxy path on a bad request.
- Run the `security-review` skill before closing any milestone that touches the
  request path, tokens, or the admin interface.

- The private key that signs sponsor licenses is never in the repository,
  never in a log, and never needed by a test. Tests use key pairs of their own.

## 4. Privacy

- IP addresses are personal data under GDPR. Default: store aggregated counters
  only (per rule, per crawler, per network, per hour). No raw request log on disk.
- Any feature that stores raw IPs must be off by default, have a retention limit,
  and be documented.
- The challenge page makes no request to any third party.

## 5. Accessibility and tone

- The challenge page must meet WCAG 2.1 AA (BITV 2.0): works with screen readers,
  keyboard only, high contrast, and reduced motion.
- There must always be a path that works without JavaScript.
- Neutral, calm design. No mascot. Text explains in one plain sentence what is
  happening and why. German and English from the start.
- Use the `challenge-page` skill for any change to that page, and run
  `make browser-check` afterwards.
- The operator handbook (`docs/de/HANDBUCH.md`) is what customers read. A new
  or changed setting is not done until the handbook says where to set it.

## 6. Crawler data is data, not code

- Crawler definitions live in `data/crawlers/*.yaml`. Each entry has a class
  (`training`, `ai-search`, `user-fetch`, `search-engine`, `archive`, `other`), the match
  rule, how identity is verified, the source URL, and the date it was checked.
- Never write a crawler name, IP range or verification method from memory. Look
  it up in the operator's own documentation and record the source.
- A crawler is only allowed by name if its identity is verified (published IP
  ranges or reverse DNS). An unverified name match is treated as unidentified.
- Use the `add-crawler` skill for every addition or update.

## 7. Performance

- No network call in the request path. DNS verification and list downloads run
  in the background and are cached.
- The decision for one request should cost microseconds, not milliseconds.
  Add a benchmark for the rule engine and keep it in CI.

## 8. Testing

- Every package has table-driven tests. The rule engine and token code need
  tests for the hostile cases, not just the happy path.
- Integration tests run the real binary against a fake upstream server.
- Before reporting work as done: `go vet ./...`, `go test -race ./...`, and the
  integration tests all pass. If something fails, say so with the output.

## 9. How to work

- One milestone at a time, in the order of `docs/ROADMAP.md`. Use the
  `build-milestone` skill.
- No features outside the current milestone. Put ideas in the "Later" list.
- Technical choices: decide, then log them in `docs/DECISIONS.md` with the reason.
- Product choices (naming, pricing, what customers see, defaults that change
  behaviour for visitors): ask the owner.
- Small commits with a message that says why. Tick off roadmap items as they land.
- Staging and production of the owner's other projects are never test targets
  unless he says so.

## 10. Modularity and failure isolation

- One job per package, stated in the first line of its package comment.
- `cmd/xibalba` only wires parts together. Feature packages meet through small
  interfaces and do not import each other sideways.
- Everything that runs is a `lifecycle.Component` with a name. The same name
  appears in logs, errors and `/healthz`.
- Every part that can fail at run time has a health check and a defined answer
  to failure. See the table in `docs/ARCHITECTURE.md` and extend it with each new part.
- No package-level mutable state and no `init()` side effects.

## 11. Documentation is part of the change

- A change is not done until its documentation is. `docs/DEVELOPMENT.md` has
  the table of what to update for which kind of change.
- Every setting is in `internal/config`, `xibalba.example.yaml` and
  `docs/CONFIGURATION.md`. A test keeps the example file equal to the defaults.
- Documentation states what exists. Anything not built yet is marked "planned".
- Examples in the docs (commands, output, error messages) are copied from a
  real run, never written by hand.

## 12. Customisation

- Appearance settings (`pages.operator`, `pages.texts`, `pages.attribution`,
  and later logo and accent colour) need a sponsor license. Everything that
  protects a website, and everything a visitor needs, stays free. Do not put
  a protective or accessibility feature behind the license.
- A license problem never takes a website down: an expired license falls
  back to the standard pages.
- Behaviour that a site owner may reasonably want to change is a setting with a
  safe default, a validator, and a documented meaning. No magic constants in
  the request path.
- Rules, crawler definitions, translations and branding are data files.
- New challenge types and storage backends plug in through an interface and a
  name, without edits to the core.

## Layout

```
cmd/xibalba/          main program: wiring only
cmd/xibalba-license/  maintainer's tool: issue and check sponsor licenses
internal/config/      load and validate the configuration
internal/logging/     build the logger
internal/lifecycle/   start, stop and supervise components
internal/health/      per-component health report
internal/httpserver/  HTTP listener with safe defaults
internal/clientip/    real client address behind trusted proxies
internal/rules/       compile a rule set, decide about a request
internal/crawlers/    know the crawlers, tell genuine from impostor
data/                 built-in crawler definitions and presets (embedded)
internal/limit/       count requests per client, say when one is over a limit
internal/trap/        hidden link that catches crawlers; optional maze
internal/gate/        enforce decisions on live requests, count them
internal/token/       sign and verify tokens, keep the signing key
internal/challenge/   the security check: tasks, answers, pass cookie
internal/license/     verify sponsor licenses, offline
internal/pages/       pages shown to visitors; texts in assets/locales
internal/proxy/       forward to the website, answer when it is unreachable
internal/buildinfo/   version of the running build
test/integration/     tests that run the real binary
test/browser/         checks of the visitor pages in a real browser (not in CI)
test/webserver/       checks behind real nginx and Caddy (not in CI)
examples/rules/       example rule files, kept valid by a test
examples/nginx/       tested nginx configuration; the handbook shows it
examples/caddy/       tested Caddy configuration; the handbook shows it
docs/                 spec, roadmap, decisions, architecture, configuration, rules, crawlers, limits, trap, challenge, development
docs/de/              operator handbook in German
```

Planned packages are listed in `docs/ARCHITECTURE.md`.

## Environment notes

- In the cloud workspace the Go module proxy is not reachable. Dependencies are
  fetched with `GOPROXY=direct GONOSUMDB=*` straight from GitHub. This is a
  workspace setting only; do not commit it anywhere.
