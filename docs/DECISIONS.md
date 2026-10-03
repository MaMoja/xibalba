# Decisions

Newest first. One entry per decision: what, why, who decided.

## 2026-10-03

- **Tokens are stateless and signed with HMAC-SHA-256.** Agent's choice. No
  storage to run, back up or share; instances that share the key file accept
  each other's tokens. Tasks and passes are signed with different derived
  keys so one can never stand in for the other.
- **Tasks and passes are tied to the client's network (/24, /64) and user
  agent.** Agent's choice. A solved check cannot be handed to a fleet. The
  network rather than the single address, so visitors whose address moves
  within their provider are not asked again. `challenge.bind_network: false`
  drops the network part. The tie is a keyed hash; the cookie reveals neither.
- **The pass cookie is removed before a request reaches the website.**
  Agent's choice. The website has no use for it and should not log it.
- **Path without JavaScript: wait, then press a button.** Agent's choice. An
  automatic timed redirect would be simpler for the visitor but is a
  recognised accessibility failure (a time limit the visitor cannot control).
  A crawler can take this path too; `no_javascript: deny` closes it.
- **The challenge page answers with status 403.** Agent's choice. A 200 would
  let caches and search engines take the check for the real page.
- **Default difficulty 18 bits.** Agent's choice. About a tenth of a second
  on a desktop; the cost to bulk fetchers comes mostly from having to run a
  browser and from the binding, not from the arithmetic.
- **The proof-of-work script is our own SHA-256, checked against a reference
  implementation.** Agent's choice. No third-party script; one static script
  named in the Content-Security-Policy by its hash.
- **`/.xibalba/` is reserved and not configurable.** Agent's choice. One fixed
  place is easier to document and to exempt in other tools.
- **Signing key in a file, created on first start with mode 600; without a
  file the key lives only as long as the process.** Agent's choice. Keeps
  secrets out of the configuration file, which gets shared and versioned.
- **Operator handbook in German first.** Agent's choice, following the
  owner's request for documentation a customer can be given. The target
  customers are in the German-speaking region; the reference documents stay
  English.
- **Parity with Anubis's challenge was not checked this time.** The
  documentation site refused the request and the fallback was not approved in
  time. The design is our own.

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
