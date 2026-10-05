# Parity with Anubis

What Anubis offers according to its public documentation (read on 2026-10-03,
documentation only, described in our own words), and where Xibalba stands.
"Milestone" refers to [ROADMAP.md](ROADMAP.md).

## Xibalba has it

| Feature | Note |
|---|---|
| Reverse proxy in front of one website | |
| Subrequest authentication (nginx `auth_request`, Caddy `forward_auth`, Traefik `forwardAuth`) | Called "verdict". Caddy and Traefik need no second request; nginx example tested; every request is counted once. No list of redirect domains is needed: a visitor is only ever sent back to a path on the same website. See VERDICT.md |
| Link previews (Open Graph) for protected pages | Fetched in the background, never while a request waits; or fixed tags. `twitter:`, `article:` and `description` as well. See PREVIEWS.md |
| Configurable status codes for the check and the block page | From a list of sensible codes; default 403 |
| Imprint and privacy links on the pages | |
| Rules on user agent, path, host, method, headers, addresses; allow, deny, challenge, weigh; thresholds; imported rule files | Structured conditions instead of an expression language (see DECISIONS.md) |
| Proof-of-work check, signed pass cookie valid for a week | |
| Checks without JavaScript | `wait` (wait, then a button) and `refresh` (the page sends the browser on by itself) |
| A check that only asks the browser to run a script | Method `script`; Anubis uses the Preact library for it, Xibalba forty lines of its own |
| Kind of check, difficulty and extra checks per rule and per threshold | A pass counts wherever the same or less is asked |
| Extra checks: style sheet loaded, signs of an automated browser | `checks: [css, headless]`; the second is in Anubis's commercial edition |
| Signing key that survives a restart | |
| Verified search crawlers (name plus published addresses or DNS) | Xibalba also verifies AI crawlers and refuses rules that trust a name alone |
| Blocking named AI crawlers | Presets by purpose |
| German and English pages | Anubis has about 30 languages |
| Contact shown on the pages | |
| Links to imprint and privacy policy on the pages | Free, in every language of the page |
| Web interface: overview, presets, address lists, own rules with a box to try a request, earlier versions | Anubis has none; optional in Xibalba |
| Statistics on disk, per hour, optionally per network | |
| Published packages: archives, deb, container image | rpm is missing |
| Health endpoint | |
| Removing the branding for sponsors | |
| Guides and tested examples for nginx, Caddy, Apache, HAProxy, Traefik; Docker Compose; health check for containers | See ENVIRONMENTS.md |
| Prometheus metrics | |
| Checking everything that says it is a browser, with exceptions for well-known paths, robots.txt, favicon, feeds, git | Presets, opt-in; Anubis does it by default |
| Score for implausible browsers | Preset `weigh-odd-browsers` |
| Trap link and maze | Maze off by default, made of meaningless syllables |
| Country conditions from a local database file | Also reads DB-IP's free database and can download it |

## Xibalba does it differently on purpose

| Anubis | Xibalba | Why |
|---|---|---|
| Denied requests get status 200 by default | Status 403 by default; other codes can be chosen (`pages.status`) | Honest by default |
| Forwarding headers are believed as sent | Only from configured trusted proxies | Security rule |
| DNS lookups while a request is evaluated | In the background only | Performance rule |
| Mascot | None | Tone |
| Pass cookie readable by scripts, SameSite=None by default | HttpOnly, SameSite=Lax | Safer defaults |
| Challenge state stored on the server | In a signed token, nothing stored | Lightweight; see "Missing" for the consequence |

## Missing in Xibalba

| Feature in Anubis | Planned in |
|---|---|
| Ready-made exceptions for uptime monitors and Google's user-triggered fetchers (git, feeds and registries exist; small browsers pass without JavaScript) | Later |
| Weights for browser headers that depend on HTTPS (client hints) | M5 |
| Address file of trapped clients for fail2ban | Later |
| Network-operator (ASN) conditions | Later |
| Server load as a condition (stricter when the machine is busy) | Later |
| A solved task cannot be used twice | Later: needs stored state; today a solution can be reused until it expires, by clients of the same network and browser |
| Proof of work in WebAssembly (memory-hard functions) | Later: needs a WebAssembly program for the browser and its counterpart on the server, which means a build chain and a dependency |
| Storage backends for shared state: Valkey/Redis, S3 | M9 |
| rpm packages | Later |
| Token that the web server in front can verify itself (HAProxy) | Later |
| Running under a path prefix; website reached over a unix socket; TLS options towards the website | Later |
| Serving a robots.txt that disallows AI crawlers | Later |
| Tools: robots.txt to rules, IP list to rules | Later |
| Headers that tell the website which rule decided | Later |
| Log to a file with rotation | Later |
| Guides tested in a real cluster or on Windows; guides for WordPress, HTMX | M10 |
| A list of browser extensions known to break the check (a page for visitors that explains the check exists) | Later |
| DNS blocklist lookup (off by default in Anubis) | Not planned: sends visitor addresses to a third party |
| More languages | Later |

## Xibalba has it, Anubis does not (per its documentation)

- Crawler classes by purpose and verification of AI crawlers' identity.
- Rules that trust a crawler's name alone are refused.
- Request limits per client with exempt addresses.
- Rules that let through by path only apply to plainly written addresses.
- Trusted-proxy list for the client address.
- Configured answer when Xibalba itself fails (`rules.on_error`).
- Dry run.
- Configuration errors with file, line and fix, all at once.
- No address in any log or report by default.
- Accessibility checked against WCAG 2.1 AA.
- Operator handbook in German.

## Tests to take from Anubis' published security history

Their documentation lists problems they had. Each is a test Xibalba should
have: accepting any hash as a solution; reusing a solution; open redirect
through odd URL forms and schemes; path tricks in path rules; duplicate
headers; changed cookie settings causing endless checks; two tabs open at
once; a sub-request racing the check; browsers that change headers between
task and answer; paths with semicolons and repeated slashes surviving the
proxy.
