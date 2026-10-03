# Decisions

Newest first. One entry per decision: what, why, who decided.

## 2026-10-03

- **Wording of the visitor pages approved; operator and texts must be
  adjustable at set-up.** Owner's decision. Implemented as the `pages`
  section: `operator`, `contact`, `default_language`, `texts`.
- **Custom page texts are plain text with one placeholder, `{operator}`.**
  Agent's choice. No HTML and no template language in the configuration: a
  typo cannot break a page, and nothing a site owner writes can become markup.
- **The block text names the operator once, as the subject of the sentence.**
  Agent's choice. A name then fits without changing its grammatical case in
  German; names that need an article are set per language in `pages.texts`.

- **Combined conditions are structured (`all`, `any`, `not`), not a text
  expression language.** Agent's choice. They are checked field by field with
  line-accurate errors, a rule editor in the web interface can show them as a
  tree, and no expression parser or third-party interpreter enters the request
  path. A text language is on the "Later" list if this proves too limited.
- **Text tests ignore case by default.** Agent's choice. A rule that says
  `GPTBot` and silently misses `gptbot` is the worse mistake; `case_sensitive:
  true` opts out.
- **Paths are normalised before rules are tested.** Agent's choice. Decoding,
  backslashes, path parameters, repeated slashes and dot segments are
  resolved, so a rule cannot be dodged by spelling. It only widens matches;
  the website receives the path unchanged.
- **Rules on forwarding headers are rejected.** Agent's choice. They would
  look like address rules while testing client-written text. `ip` is the only
  condition Xibalba establishes itself.
- **Regular expressions are RE2 only.** Agent's choice (Go's standard
  library). Evaluation time is linear in the input, so no request can make a
  rule slow.
- **The block-page reference identifies the rule, not the visitor.** Agent's
  choice. It is derived from the rule's name, so support is possible without
  logging who was blocked.
- **Decisions are not logged per request.** Agent's choice. Counters only;
  the debug log names the rule but no address, path or user agent.
- **`deny` answers honestly with 403.** Agent's choice, in line with the
  target group. No fake success pages.
- **Own rules before imported rules.** Agent's choice. `rules.list` is
  evaluated before `rules.files`, so the site owner's exceptions win.
- **`challenge` is accepted before it exists and passes requests on.**
  Agent's choice, temporary. Rule sets can be written and counted in advance;
  Xibalba warns at start-up. Enforcement arrives with M3.
- **Visitor pages offer the second language in a `<details>` element.**
  Agent's choice. It is a language switch that needs no JavaScript, no
  cookie and no change to the URL.
- **Website outages are logged once per outage.** Agent's choice. A log line
  per failed request would flood the log exactly when it is needed.

## 2026-10-02

- **Name: Xibalba.** Owner's choice. In Maya tradition the underworld whose
  visitors must pass a series of tests.
- **Clean rebuild, not a fork of Anubis.** Owner's choice. No code, text or
  assets are taken from Anubis; its public docs may be read for behaviour.
- **Language: Go, single static binary, no cgo.** Agent's choice. Strong standard
  library for proxies, easy cross-compile to the Raspberry Pi, one file to deploy.
- **Web interface server-rendered and embedded.** Agent's choice. No build chain,
  no third-party requests, easier to make accessible.
- **Aggregated statistics by default, no raw IP log.** Agent's choice. GDPR and
  the public-sector target group.
- **Small firms first, authorities second.** Agreed with owner. Short sales
  cycles produce the references authorities ask for.

- **Licence: MIT.** Owner's choice ("open source like Anubis").
- **Repository: private on GitHub under the owner's account for now.** Owner's choice.
- **YAML library: `github.com/goccy/go-yaml`.** Agent's choice. MIT, pure Go,
  no further dependencies, and it exposes line positions, which the
  line-accurate configuration errors need. The standard library has no YAML reader.
- **Component model.** Agent's choice. Every running part implements
  `lifecycle.Component` and has a name used in logs, errors and health, so a
  failure is always attributed to one part.
- **Separate operations listener.** Agent's choice. Health and metrics stay
  reachable when the public side is under load, and are never exposed publicly by default.
- **Unknown settings are errors.** Agent's choice. A typo must never be ignored silently.

- **`upstream.url` is required and has no default.** Agent's choice. Guessing
  where someone's website runs would silently proxy to the wrong place.
- **Forwarding headers are believed only from `server.trusted_proxies`.**
  Agent's choice. The default is to trust nothing. The client address is
  resolved once, in `internal/clientip`, and passed on in the request context;
  no other package reads forwarding headers.
- **Unreachable website is `degraded`, not `down`.** Agent's choice. `/healthz`
  must not make a service manager restart Xibalba for a fault in the website.
- **No whole-request timeouts on the public listener.** Agent's choice.
  Downloads, uploads, streams and websockets have no natural upper bound. Only
  the header timeout applies. Limits on slow bodies are on the "Later" list.
- **Environment proxy settings are ignored for the upstream.** Agent's choice.
  `HTTP_PROXY` on the host must never reroute traffic meant for the website.
- **Dry-run mode moved from M1 to M2.** Agent's choice. A setting that does
  nothing yet would be misleading.
- **No TLS termination yet.** Agent's choice. Xibalba runs behind the web
  server that holds the certificate; own TLS is on the "Later" list.

## Open (owner to decide)


- Copyright holder named in `LICENSE` (currently "The Xibalba Authors").
- Whether the challenge page shows a "Protected by Xibalba" line.
