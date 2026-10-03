# Xibalba: product spec

## Goal

A self-hosted program that sits in front of a website, blocks AI crawlers and
abusive bots, lets wanted crawlers through, and shows the owner what it did.
Everything Anubis offers, plus a web interface, purpose-based crawler handling,
and a challenge page that authorities can deploy.

## Customers

1. Small firms and the agencies and hosters that serve them (first).
2. Public authorities, universities and their IT service providers (second, with references).

## Part A: parity with Anubis

This list is written from memory of the Anubis documentation. At the start of each
milestone, check the matching part of the Anubis docs and correct this list.
Behaviour may be matched; code and text may not be copied.

- Reverse proxy in front of one or more upstream sites.
- Verdict mode for nginx, Caddy and Traefik (the web server asks, Xibalba answers allow or deny).
- Rule engine: match on user agent, path, host, method, headers and IP ranges, combined with `all`, `any` and `not`. (As far as we know Anubis offers a text expression language for this; Xibalba uses structured conditions, see DECISIONS.md.)
- A denied request gets an honest "blocked" page with status 403. (According to its documentation Anubis answers denied scrapers with a response that looks like success; Xibalba does not pretend.)
- Actions per rule: allow, deny, challenge, add weight. Thresholds map total weight to an action.
- Importable rule sets.
- Challenge types: proof of work with adjustable difficulty, and a variant that works without JavaScript.
- Signed pass cookie with expiry, so a visitor is checked once.
- Storage backends for challenge state: memory, local file, external store for multi-instance setups.
- Metrics endpoint in Prometheus format, health endpoint.
- Translations of the challenge page.
- robots.txt handling and well-known paths passed through.
- Docker image and packages.

Added on 2026-10-03 from excerpts of the Anubis documentation supplied by the owner:

- By default Anubis challenges every request whose user agent contains "Mozilla", except `/.well-known`, `robots.txt`, `favicon.ico` and feeds. Xibalba challenges nothing until told to.
- Honeypot: a link only a faulty parser follows, leading into generated worthless pages; clients seen there gain weight; their addresses can be written to a file for fail2ban.
- GeoIP conditions with MaxMind GeoLite databases (from version 1.28).
- Tools: `robots2policy` (robots.txt to rules), `iplist2rule` (IP block list to rules).
- Ready-made rule files for non-browser clients such as container registry clients.
- A commercial unbranded edition.

## Part B: what Xibalba adds

- **Web interface.** Requests allowed, challenged and denied over time, by rule,
  by crawler and by network. No raw IPs by default.
- **Rule editor** in the web interface, with validation and a test box
  ("what would happen to this request?"). Changes are versioned and can be rolled back.
- **Two layers.** Presets and switches for normal users; the full rules behind
  them for experts. A switch is just a named rule set.
- **Crawler classes** with separate defaults:
  - `training`: collects content for model training. Default deny.
  - `ai-search`: builds an index for answers that link back. Default allow.
  - `user-fetch`: fetches a page because a person just asked. Default allow.
  - `search-engine`: classic search. Default allow.
  - `archive`: builds a public copy of the web. No default yet.
  - `other`: everything else.

  As built in M4, these defaults are presets the site owner switches on;
  nothing is on by default. Whether a fresh installation should start with
  presets switched on is an open product decision.
- **Verified identity.** A crawler is trusted by name only if it comes from the
  operator's published IP ranges or passes a reverse DNS check. Otherwise it is
  treated as unidentified.
- **IP lists.** Block and allow lists for addresses, ranges and networks (ASN),
  managed in the interface, with optional expiry and a note per entry.
- **Challenge page for authorities.** Accessible (WCAG 2.1 AA / BITV 2.0), no
  JavaScript requirement, neutral design, customer logo and colours, German and English.
- **Privacy by default.** Aggregated statistics, no third-party requests, short
  retention, documented for a data processing agreement.
- **Rate limits** per client and per network as a rule action.
- **Dry-run mode.** Log what would have happened without blocking anyone, so a
  new customer can see the effect before switching it on.

## Not in scope for version 1

- Hosted service (multi-tenant, billing).
- CMS plugins (WordPress, Shopware, TYPO3).
- Browser fingerprinting or any tracking of visitors.
- Machine-learning classification.
