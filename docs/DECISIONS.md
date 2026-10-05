# Decisions

Newest first. One entry per decision: what, why, who decided.

## 2026-10-05

- **Link-preview tags are fetched in the background, never while a request
  waits.** Agent's choice. Rule 7 forbids a network call in the request
  path. The price: the first challenge page for an address has no tags. A
  preview service that asks once gets an empty preview once; services ask
  again, and an operator can give fixed tags instead.
- **The fetcher is bounded on every side.** Agent's choice. An address that
  anyone can ask for makes Xibalba fetch from the website, so: one fetch at
  a time, a limit per minute (default 30), 64 waiting at most, a bounded
  table, 256 KiB read per page, five seconds per fetch, no redirects, no
  proxy from the environment, paths with `..` not fetched, and by default
  the query left out so that one page cannot be asked for under endless
  names.
- **Only preview tags are passed on, as bounded plain text.** Agent's
  choice. Names starting with `og:`, `twitter:`, `article:`, and
  `description`; the title as a fallback. Other `meta` tags (generator,
  verification codes) say things about the site that a preview does not
  need.
- **Our own small reader for `meta` tags instead of an HTML parser.**
  Agent's choice. The standard library has none and a dependency for forty
  tags is not worth it. It is fuzzed; what it returns is escaped again by
  the page template.
- **The website is not told the visitor's host name by the preview fetch
  unless it is part of what the tags are kept under.** Found by the review:
  a website that builds addresses from `X-Forwarded-Host` would have put
  the first asker's host name into everyone's preview. With
  `preserve_host` each host name has its own tags; without it no name is
  sent.
- **Pages with tags are never pushed out of the table by addresses without
  tags.** Found by the review: asking for many invented addresses emptied
  the table. Making room looks at 16 entries, not at all of them, so its
  cost does not grow with the table (0.7 µs at 20000 pages).
- **Tags are copied and capped at 4096 bytes per page; `max_pages` ends at
  20000.** Found by the review: a value cut out of a page kept the whole
  256 KiB of it in memory.
- **Previews are free.** Agent's choice in line with rule 12: they are
  about being found and shared, not about appearance.

## 2026-10-04

- **A pass earned by waiting has the lowest level**, whatever the task was.
  Found by the review: the button of the path without JavaScript handed out
  the level of the calculation. And a check whose button is open asks only
  for that lowest level, since anyone may pass it by waiting.
- **`headless` does not compare the browser's name with the request's.**
  Privacy settings, extensions and proxies in organisations change one side
  only, and the visitor would have been locked out with no way back.

- **Four kinds of security check, two extra checks, selectable per rule**
  (owner's request). Names are our own: `pow`, `script`, `wait`, `refresh`;
  `css`, `headless`. The kind lives in the signed task, never in the form:
  what a client claims about the kind of check counts for nothing.
- **A pass has a level.** It records how demanding the check was and which
  extra checks it met, and counts wherever the same or less is asked. A new
  pass keeps what the old one had, so a visitor who moves between rules is
  not checked back and forth.
- **`script` uses no library.** Anubis's counterpart loads Preact; what is
  proven is only that a script ran, and forty lines prove that.
- **`css` keeps no state.** The style sheet carries a keyed value for the
  task's number, and the page's script reads it back from the applied
  style. Anubis records on the server that the style sheet was fetched; we
  keep nothing, and additionally learn that the style was applied.
- **`headless` reports only hard signs** (the browser says it is automated,
  or a known tool left marks). No guesswork about plugins, fonts or window
  sizes: such guesses lock out real people with unusual browsers. The limit
  is stated in the docs: a client can lie.
- **`refresh` is offered although it fails WCAG 2.2.1**, because the owner
  asked for it and Anubis has it. It is not the default, the docs and the
  handbook say so, and the accessibility check asserts that this is its
  only finding.
- **No WebAssembly proof of work for now.** It needs a WebAssembly program
  for the browser (a build chain: Rust or TinyGo), the same function on the
  server (a dependency such as argon2), and a megabyte-sized download or
  hand-written WebAssembly. That is against "one small program, no build
  chain". Soteria is part of Anubis's commercial edition and experimental.
  What a memory-hard function buys (it resists graphics cards) matters only
  against an attacker who builds a solver for this one program. Reopen if
  such solvers appear.
- **Checks for "outside Europe" are a recipe, not a built-in group.** A
  list of country codes in the rule is explicit and the operator decides
  what Europe means.

- **Imprint and privacy links are free, not a sponsor feature.** German
  operators are obliged to offer both from every page; that is something a
  visitor needs, not decoration (CLAUDE.md section 12). They are links, not
  pages hosted by Xibalba: the operator already has both documents, and a
  second copy would go stale.

- **Packages are built by a shell script and `dpkg-deb`, not by a packaging
  tool** (goreleaser, nfpm): no new dependency, forty lines, and the same
  script runs on a developer's machine. rpm is left for when someone asks.
- **The Debian package starts nothing.** The configuration it brings is
  valid but points at a placeholder website; starting a proxy in front of
  nothing helps nobody. An upgrade restarts a service that was running.
- **The packaged configuration keeps everything Xibalba writes under
  `/var/lib/xibalba`** (key, crawler cache, web interface password and
  changes), because the service file makes the rest of the system read-only.

- **Regular expressions are bounded by compiled size and by input length**
  (400 steps each, 2,000 per rule set, 1,024 characters of input). Found by
  the security review of the rule editor: within the old limit of 512
  characters per pattern, a rule set could cost seconds per request for a
  visitor who sends a long header. Go's expressions are linear, but linear
  in size times input. A value too long to search counts against the
  request (match for a restricting rule, no match for an allowing one), the
  same polarity as for roundabout paths, so padding neither evades a deny
  rule nor earns an allow. This changes behaviour for existing rule sets
  with very large expressions; they are refused with a message.
- **Rule text from the web interface is plain YAML only**: no anchors,
  aliases, merge keys, tags or second documents. A few hundred bytes of
  aliases expanded to gigabytes. The configuration files are not restricted
  this way (their author owns the machine), but the new limit of 20,000
  condition groups per rule set applies to every source.
- **Versions are named by a hash of their content**, not by their position,
  so a click on an older page cannot restore a different version.
- **The box to try a request asks the crawler registry without side
  effects** (`Peek`): no counting, no name lookup for an address someone
  typed into a form.

- **The rule editor is a text field with the rule file format, not a form
  builder.** A builder for nested conditions (`all`, `any`, `not`) would be
  the largest piece of the interface and need script. The text field reuses
  the format, the validator and the line-accurate messages that exist, and
  the box to try a request gives the feedback a builder would.
- **Versions cover presets and own rules, not the address list**, and there
  are ten. A removed or expired address must not survive in a history
  (privacy). "Versioned config" on the roadmap meant the configuration; as
  the file is never rewritten by the program, only the changes need versions.
- **Rules from the web interface come before those of the configuration.**
  Otherwise a rule added there would often be shadowed by a preset or a
  file rule and seem to do nothing.

- **Changes from the web interface go into their own file and sit on top of
  the configuration; the YAML file is never rewritten.** Rewriting would
  lose the owner's comments and layout and make the program a writer of the
  file it validates. The changes file holds only what was changed (presets
  on or off, listed addresses) and is plain JSON.
- **The changes file is in force whenever it exists**, also with
  `allow_changes: false`. Otherwise locking the interface down would
  silently unblock every blocked address. `allow_changes` governs editing,
  the file governs effect; deleting the file drops the changes.
- **Listed addresses become two ordinary rules in front of all others**
  (`web-interface.allow`, `web-interface.deny`), and the whole rule set is
  compiled again and swapped in one step (`gate.Swap`). One mechanism for
  presets and addresses, the same validation as at start, and the decision
  path stays as it was: one pointer load more per request. The list is
  capped at 500 entries because a rule's addresses are compared one by one.
- **Addresses let through in the web interface are exempt from the request
  limits**, like `limits.exempt`. An owner who lists the office expects it
  not to be limited. Stated on the page.
- **With `allow_changes`, crawler verification runs from the start** even if
  no rule uses it yet, so that a preset switched on later works at once.
  The cost is the outgoing requests for the address lists.
- **Forms that change something carry a value tied to the login**, on top of
  the same-origin check and the SameSite cookie.

- **Sponsor names and pictures in the README are taken from GitHub
  automatically, once a day; licenses stay manual.** The owner asked for
  automation. Name and picture come from the sponsor's GitHub account, so
  nothing has to be uploaded and nobody can put a foreign link or image into
  the README. Private sponsors are never listed. Signing licenses in a
  workflow would put the private key on GitHub; that is the owner's call
  and is not done.
- **The sponsor license tier is $50 a month** (owner's sponsor page; the
  documents said 50 €).

- **The web interface answers only under known host names** (`localhost`,
  local addresses, the listen host, `admin.hostnames`). Without that, a web
  page could point its own name at 127.0.0.1 and reach the login as its own
  site. Costs one setting for those who put a web server in front.

- **The web interface is an option, off by default, and as small as it can
  be** (owner's decision). Its own listener, so that switched off nothing of
  it exists and the operations listener stays free of logins. Pages are
  rendered on the server without any script: the chart is an SVG drawn by
  the server, with the same numbers as a table. About 600 lines of Go and
  6 kB of embedded files.
- **Password with PBKDF2-SHA256 from the standard library** (Go 1.24 has
  `crypto/pbkdf2`), 600000 rounds, instead of bcrypt or argon2, which would
  be a dependency. Checks run one at a time, and an address waits after five
  failures, so the cost of a check cannot be used to keep the machine busy.
- **One password, no user names; sessions in memory.** Small installations
  have one operator. A restart signs everyone out, which is acceptable and
  saves a session store. User accounts go to Later if someone asks.
- **`-set-password` hides the typing through a terminal call from the
  standard library** (`syscall`, Linux), instead of `golang.org/x/term`.
  On other systems the password has to be piped in.
- **`Referrer-Policy: same-origin` on the web interface, not `no-referrer`.**
  With `no-referrer`, browsers send `Origin: null` with forms, and the check
  that a form comes from our own page refused every login. Found by trying
  it in a real browser.

- **Counts per network are an option, off by default, with their own short
  time limit and only the largest networks per hour.** The owner asked for
  statistics per network. A `/24` or `/48` is not an address, but a small
  organisation can have one to itself, so it is treated as closer to
  personal data than rule counters: separate files (`networks/`), 30 days
  by default, top 50 per hour, the rest summed as `other`. Networks by
  address prefix and not by provider (AS number): the latter needs another
  database; noted under Later.
- **`internal/origin` hands its counts over and forgets them** (`Drain`),
  instead of running totals like the other parts. A table of totals would
  grow with every network ever seen; draining once a minute bounds it.
  `internal/stats` got an input for such counts and a reduce step, and
  stays ignorant of what a network is.

- **Escalation of a `challenge` limit is a number, `deny_at`, on the limit
  itself.** The owner wanted it as an option at set-up. One more number on
  the window the operator already wrote is easier to explain than a second
  mechanism with its own memory of offenders, costs no extra state, and ends
  by itself when the client slows down. Off (`0`) by default.
- **The wiki is built from `docs/`** by `tools/wiki.py` and published by a
  workflow. One source; the wiki cannot drift from the repository, and the
  same script fails CI on a link between documents that leads nowhere.

- **Statistics are kept in plain text files, one line per hour, not in an
  embedded database.** Agent. This departs from the stack rule in CLAUDE.md
  ("embedded, pure-Go database") and is logged for that reason: a year of
  hourly counts is a few megabytes and is only ever appended to and read in
  order. Files need no dependency, can be read with any tool, and cannot be
  corrupted as a whole. If the web interface later needs queries a file
  cannot answer, a database can be put behind the same package.
- **No counters per network of origin for now.** Agent, for the owner to
  decide. They would be the first data about visitors on disk.
- **The statistics package is handed running totals and works out the hours
  itself.** Agent. It knows nothing about rules or crawlers, and the parts
  that count know nothing about storage.

- **Metrics in the Prometheus text format are written by a package of our
  own, without the Prometheus client library.** Agent. The format is a few
  lines of text; the library would bring a dozen dependencies. The package
  knows nothing about what is measured; the parts hand it their numbers.
- **`/metrics` needs no setting and is on the operations listener only.**
  Agent. It shows nothing the other endpoints there do not show.

- **Different pages are estimated with a bit field of 256 bits per client,
  limit and period** (linear counting). Agent. Exact counting would need a
  list of addresses per client; the estimate needs 64 bytes, stores no
  address a client asked for, and is within about a tenth up to 500 pages.
- **A page is told by the website's answer: a successful answer of type
  `text/html`.** Agent, after the review of the first version, which went by
  the ending of the address and could be dodged with `/page.php/x.css`.
  Request headers that say what is being loaded are chosen by the client.
  Costs a thin wrapper around the answer, only when a limit counts pages.
- **Pages read again in the next period are not counted twice**: the sliding
  estimate adds only what the previous period holds beyond the current one.

- **Rules that favour a request and test the path only apply to addresses
  sent in plain form.** Agent, after the security review. Normalising makes
  more spellings match, which is right for rules that restrict and a hole
  for rules that let through, because the website receives the address as
  sent and may read it differently.
- **Strictness is decided per path condition by polarity** (rule favours
  XOR condition negated), the same test as for crawler names. Agent, after
  the second review: "deny everything except /public/" favours the path as
  much as "allow /public/" does.
- **Roundabout also means: a `%` left after decoding, control characters,
  invalid UTF-8.** Agent. Look-alike characters (fullwidth dots and slashes)
  are not treated specially: no current web server folds them.
- **Request targets that are not paths get 400.** Agent.
- **`keep-internet-working` requires an address without a query**, except
  for webfinger. Agent, for the owner to note: a default visitors meet.
  Websites that route by query would otherwise serve any page there.
- **Exemption from the request limits is a choice per rule
  (`exempt_from_limits`), not a consequence of `allow`.** Agent, after the
  review. Replaces "a request a rule explicitly allows is not counted".
- **Trap links are made per client and day with a keyed check value.**
  Agent, after the review. Replaces one link per start. Nothing is stored
  for it, and nobody can get another client caught.
- **A `challenge` limit does not restrain a client that holds a pass;
  documented, with the advice to add a `deny` limit.** Agent. An automatic
  escalation would need a factor that is a guess; open for the owner if a
  setting for it is wanted.

## 2026-10-03

- **Limits and detection (new milestone M5) come before statistics.** Owner.
- **The maze of worthless pages for crawlers will be an option, off by
  default.** Owner.
- **Own reader for the country database format instead of a library.**
  Agent. The format is published; the reader needs one value (the country
  code), is 300 lines, and is checked against the test databases MaxMind
  publishes with the format description (run with `XIBALBA_MMDB_TESTDATA`).
  No new dependency, and a reader that treats the file as untrusted and does
  not allocate per lookup.
- **No database is shipped; the site owner supplies it.** Agent, as agreed
  with the owner. Xibalba stays small, the data does not age with the
  program, and the licence of the data is met by whoever uses it. Reads
  DB-IP's and MaxMind's country databases alike.
- **Optional download of DB-IP's free database, off by default.** Owner
  wanted the user to choose. DB-IP's download page states Creative Commons
  Attribution 4.0 and no account (checked 2026-10-03). MaxMind needs an
  account, so its file is fetched with MaxMind's own tool.
- **While no country database is loaded, rules with a country condition are
  skipped.** Agent. "Deny everyone outside Germany" must not deny everybody
  because a file is missing.
- **Trap link inside a `template` element.** Agent. Its content is inert in
  every browser: not rendered, not in the accessibility tree, not focusable.
  The page stays accessible, and nothing has to be hidden with styles that a
  screen reader might ignore.
- **Being caught is a fact for rules (`trapped`), not an action.** Agent. The
  site owner chooses deny, check or score; the preset `block-trapped` is the
  short form.
- **Caught clients are remembered by address (IPv6 /64), not by wider
  network.** Agent. Neighbours of a caught client must not be denied.
- **The maze is made of meaningless syllables, marked `lang="zxx"`.** Agent.
  Nothing readable as a statement is served under the domain of an authority.
  Off by default (owner).
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
  *Reopened 2026-10-05:* the status can now be chosen (`pages.status`),
  because the owner wants what Anubis offers and because some link-preview
  services read only answers with status 200. The default stays 403, the
  choice is from a fixed list (no redirects, no 5xx that looks like an
  outage of Xibalba itself except 503), and the page is marked `no-store`
  and `noindex` whatever the status, which removes the reason given above.
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


- Attribution for DB-IP: its terms ask for a link on pages that use the
  results. Should Xibalba offer a setting that shows this link on its pages,
  or is a note in the documentation enough? Today: documentation only.
- Statistics per network of origin (which networks send the most denied
  requests): wanted? They would store networks of visitors on disk, by the
  hour; if wanted, as an option that is off by default.
- Should a fresh installation start with crawler presets switched on (the
  spec says training "default deny", search "default allow")? Today nothing is
  on until the site owner lists presets.
- Is the class `archive` (Common Crawl) wanted, and should it be blocked by a
  default preset?
- Copyright holder named in `LICENSE` (currently "The Xibalba Authors").
- Whether the challenge page shows a "Protected by Xibalba" line.
