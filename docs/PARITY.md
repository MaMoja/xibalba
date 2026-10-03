# Parity with Anubis

What Anubis offers according to its public documentation (read on 2026-10-03,
documentation only, described in our own words), and where Xibalba stands.
"Milestone" refers to [ROADMAP.md](ROADMAP.md).

## Xibalba has it

| Feature | Note |
|---|---|
| Reverse proxy in front of one website | |
| Rules on user agent, path, host, method, headers, addresses; allow, deny, challenge, weigh; thresholds; imported rule files | Structured conditions instead of an expression language (see DECISIONS.md) |
| Proof-of-work check, signed pass cookie valid for a week | |
| Check without JavaScript | Xibalba: wait and button. Anubis: a page that refreshes itself. |
| Signing key that survives a restart | |
| Verified search crawlers (name plus published addresses or DNS) | Xibalba also verifies AI crawlers and refuses rules that trust a name alone |
| Blocking named AI crawlers | Presets by purpose |
| German and English pages | Anubis has about 30 languages |
| Contact shown on the pages | |
| Health endpoint | |
| Removing the branding for sponsors | |
| nginx and Caddy guides | |

## Xibalba does it differently on purpose

| Anubis | Xibalba | Why |
|---|---|---|
| Denied requests get status 200 by default | Status 403 and an honest page | Decided: no pretending |
| Forwarding headers are believed as sent | Only from configured trusted proxies | Security rule |
| DNS lookups while a request is evaluated | In the background only | Performance rule |
| Mascot | None | Tone |
| Pass cookie readable by scripts, SameSite=None by default | HttpOnly, SameSite=Lax | Safer defaults |
| Challenge state stored on the server | In a signed token, nothing stored | Lightweight; see "Missing" for the consequence |

## Missing in Xibalba

| Feature in Anubis | Planned in |
|---|---|
| Default behaviour "weigh everything that looks like a browser, then check by score" with exceptions for well-known paths, robots.txt, favicon | M5 |
| Ready-made exceptions: git clients, container registry clients, small browsers, uptime monitors, Google's user-triggered fetchers | M5 |
| Weights for implausible browser headers (old Chrome without client hints and similar) | M5 |
| Trap link and maze of worthless pages; clients seen there gain weight; optional address file for fail2ban | M5 (maze off by default) |
| Country and network-operator (ASN) conditions from local MaxMind GeoLite2 files, optional automatic update | M5 (country), Later (ASN) |
| Server load as a condition (stricter when the machine is busy) | Later |
| A solved task cannot be used twice | Later: needs stored state; today a solution can be reused until it expires, by clients of the same network and browser |
| Check that the client really loaded a style sheet | Later |
| Memory-hard proof of work (WebAssembly) | Later |
| Challenge method and difficulty per rule or threshold | Later |
| Prometheus metrics | M6 |
| Storage backends: file, Valkey/Redis, S3 | M6 (file), M9 (shared) |
| Verdict mode for nginx, Caddy, Traefik (subrequest authentication) with allowed redirect domains | M9 |
| Docker image, deb, rpm, systemd unit | M9 |
| Token that the web server in front can verify itself (HAProxy) | Later |
| Running under a path prefix; website reached over a unix socket; TLS options towards the website | Later |
| Imprint and privacy page on the visitor pages | M10 |
| Link previews (Open Graph) for protected pages | M10 |
| Configurable status codes | Later |
| Serving a robots.txt that disallows AI crawlers | Later |
| Tools: robots.txt to rules, IP list to rules | Later |
| Headers that tell the website which rule decided | Later |
| Log to a file with rotation | Later |
| Guides: Apache, Traefik, HAProxy, Kubernetes, Docker Compose, Cloudflare, Windows, WordPress, HTMX | M10 |
| Pages for visitors: why the check appears, known broken browser extensions | M10 |
| DNS blocklist lookup (off by default in Anubis) | Not planned: sends visitor addresses to a third party |
| More languages | Later |

## Xibalba has it, Anubis does not (per its documentation)

- Crawler classes by purpose and verification of AI crawlers' identity.
- Rules that trust a crawler's name alone are refused.
- Request limits per client with exempt addresses.
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
