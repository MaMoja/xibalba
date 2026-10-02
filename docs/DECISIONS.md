# Decisions

Newest first. One entry per decision: what, why, who decided.

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
