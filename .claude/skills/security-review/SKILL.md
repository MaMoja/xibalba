---
name: security-review
description: Use before closing any Xibalba milestone that touches the request path, challenge tokens, cookies, the admin interface or config loading, or when asked to review Xibalba's security.
---

# Security review for Xibalba

Xibalba stands in front of customers' websites. A flaw here is a flaw in every
site behind it. Review as an attacker who has read the source.

Have a separate agent that did not write the code do this review where possible.

## Checklist

**Client identity**
- Can a client set its own IP through `X-Forwarded-For`, `X-Real-IP` or `Forwarded`
  when no trusted proxy is configured?
- Can an allow rule be reached with only a spoofed user agent?
- IPv6: are ranges matched correctly, including IPv4-mapped addresses?

**Challenge and tokens**
- Is the token bound to what it should be (site, expiry, challenge parameters)?
- Can a solved challenge be replayed by many clients? Is that acceptable and documented?
- Are signatures checked before any other field is trusted? Constant-time compare?
- Can the client choose the difficulty or the algorithm?
- Is the key generated with `crypto/rand`, and what happens on restart or with several instances?

**Redirects and headers**
- Is the redirect target after a passed challenge restricted to the protected host?
- Are hop-by-hop headers stripped? Can request smuggling occur between Xibalba and the upstream?
- Cookie flags: `Secure`, `HttpOnly`, `SameSite`, correct path and domain.

**Bypass**
- Paths that skip the rules (health, static assets, well-known): can they reach the upstream?
- Does path matching survive encoding tricks, double slashes, case, and trailing dots?
- HTTP methods other than GET; HTTP/2; websocket upgrade.

**Availability**
- Can one request make Xibalba do expensive work (regex backtracking, unbounded
  body read, DNS lookups in the request path, unbounded map growth)?
- Do all servers and clients have timeouts and size limits?
- What happens when storage is full or unavailable: the configured fail mode, or a crash?

**Admin interface**
- Login required on every route, including API and static files that leak data.
- CSRF protection on every state change. Session expiry. Rate limit on login.
- Output escaped everywhere user agents, paths and headers are displayed
  (these are attacker-controlled strings).
- Not reachable through the public listener.

**Privacy**
- Search logs and storage for raw IPs, cookies and tokens.
- Does the challenge page load anything from another host?

## Output

For each finding: what an attacker does, what they gain, where in the code, and
the fix. Rank by severity. Fix high-severity findings before closing the
milestone; log the rest in the roadmap. If nothing was found in an area, say
what was tested.
