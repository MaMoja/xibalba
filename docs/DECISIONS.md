# Decisions

Newest first. One entry per decision: what, why, who decided.

## 2026-10-03

- **Limits and detection (new milestone M5) come before statistics.** Owner.
- **The maze of worthless pages for crawlers will be an option, off by
  default.** Owner.
- **Request limits: sliding estimate from two fixed periods per client.**
  Agent. Two counters per limit, no list of timestamps; a client cannot
  double its allowance at a period boundary. Sharded table with a fixed upper
  size; when full, entries make way instead of new clients going uncounted.
- **A request that a rule explicitly allows is not counted or limited.**
  Agent. The site owner already said it is trusted; verified crawlers let
  through by a preset are covered without a setting of their own.
- **A limit can only make an outcome stricter**, and `deny` by a limit has
  its own page with status 429. Agent.
- **IPv6 clients are counted per /64.** Agent. One connection owns a /64.
- **Default limit when switched on: 300 requests per minute, action
  `challenge`.** Agent, for the owner to confirm. With `challenge` a browser
  loses nothing but one check.
- **Wording of the "Too many requests" page.** Agent's draft, for the owner
  to confirm.
- **Crawler identity is its own package (`internal/crawlers`); rules test a
  plain `Crawler` value.** Agent. The two packages do not import each other;
  `cmd/xibalba` translates. A fault in list downloads or DNS therefore cannot
  reach the rule engine.
- **"Unknown" is a third state besides genuine and impostor.** Agent. A
  crawler whose list has not arrived, whose DNS lookup is running, or whose
  operator publishes no verification is neither let through nor denied for
  its name. Treating it as an impostor would block real search engines during
  a network outage; treating it as genuine would be a hole.
- **Rules that favour a crawler by name alone do not compile.** Agent. Applies
  to `allow`, negative `weigh`, and restricting rules with the condition
  under `not`. Found incomplete by the security review (the `not` form) and
  fixed before the milestone closed.
- **Address lists are parsed without knowing any operator's layout.** Agent.
  Every text in the JSON that is an address or network is taken. One parser
  for all operators, and no breakage when one renames a key. Guarded by
  refusing whole lists with implausible content (larger than /8 or /24,
  private addresses, over 100 000 entries, over 2 MiB).
- **Only https for address lists, also after redirects; plain http for
  loopback only** (tests). Agent.
- **Lists older than a week (or three refresh intervals) are no longer used.**
  Agent. Operators give up address space.
- **Reverse DNS: negative results are kept per IPv4 address and per IPv6
  /64; confirmed addresses in a table of their own; full tables evict instead
  of refusing.** Agent, after the security review.
- **Crawler machinery is idle unless a rule uses a `crawler` condition.**
  Agent. An installation without such rules makes no outgoing connection.
- **New class `archive`** for Common Crawl. Agent, for the owner to confirm:
  it is neither a search engine nor (by itself) a training crawler, and site
  owners will want to decide about it separately.
- **Presets are opt-in; evaluation order is list, presets, files.** Agent.
  Whether new installations should start with presets on is the owner's call
  (see Open).
- **`crawlers.builtin: false`** lets a site run on its own definitions only;
  also keeps the integration tests off the internet. Agent.
- **Amazon and Meta crawlers have no verification.** Agent. The pages cited
  give no method whose format could be confirmed; they can be denied by name
  and are never allowed as verified.
- **Only the first 512 bytes of a user agent are searched for crawler names.**
  Agent. Bounds the cost per request.
- **Attribution line on all visitor pages; removing it and customising the
  pages needs a sponsor license (50 € per month on GitHub Sponsors).** Owner's
  decision. Locked: `pages.attribution: false`, `pages.operator`, `pages.texts`.
- **`pages.contact` and `pages.default_language` stay free.** Agent's choice,
  for the owner to confirm. A visitor who is blocked by mistake needs a way
  to reach the site owner whether or not the site owner sponsors the project.
- **The license is a signed file checked offline (Ed25519), with the public
  key built into the program.** Agent's choice. No call home: it would
  contradict the privacy promises, fail on servers without internet access,
  and make every installation depend on a server of ours.
- **The check is a courtesy lock and documented as such.** Agent's choice.
  The source is MIT-licensed; anyone can build without it. Saying so plainly
  is better than pretending otherwise.
- **An expired license never stops Xibalba.** Agent's choice. Missing or
  forged license file: error at start-up, like any wrong setting. Expired:
  30 days of grace, then the pages fall back to the standard form, with
  warnings in the log and the health report. A website must not go down
  because a renewal is late.
- **The license is read at start-up only.** Agent's choice. Pages do not
  change their appearance in the middle of operation; the health report
  announces what the next restart will bring.
- **The wording of the attribution line cannot be replaced.** Agent's
  choice. Otherwise `pages.texts` would be a way around it.
- **Test binaries are built with their own public key.** Agent's choice. The
  real private key is never needed for tests and never enters the repository.

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

- Whether `pages.contact` should also need a sponsor license (currently free).
- GitHub Sponsors bills in US dollars; the tier that corresponds to
  "50 € per month" has to be created on the sponsor page.
- How long licenses are issued for (the tool takes any expiry date).


- Should a fresh installation start with crawler presets switched on (the
  spec says training "default deny", search "default allow")? Today nothing is
  on until the site owner lists presets.
- Is the class `archive` (Common Crawl) wanted, and should it be blocked by a
  default preset?
- Copyright holder named in `LICENSE` (currently "The Xibalba Authors").
- Whether the challenge page shows a "Protected by Xibalba" line.
