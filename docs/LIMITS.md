# Request limits

A crawler that pretends to be a browser cannot be recognised by its name. It
can be recognised by what it does: it sends far more requests than a person.
Request limits count the requests of each client and act when a client sends
more than you allow.

Limits are off by default.

## Switching them on

```yaml
limits:
  enabled: true
  windows:
    - {requests: 300, per: 1m, action: challenge}
    - {requests: 20000, per: 24h, action: deny}
  exempt: ["192.0.2.0/24"]
```

This reads: a client that sends more than 300 requests within a minute has to
pass the security check. A client that sends more than 20 000 within a day is
refused until the count has dropped. The office network 192.0.2.0/24 is never
counted.

| Setting | Meaning |
|---|---|
| `limits.enabled` | `true` switches the limits on. |
| `limits.windows` | One to four limits, each with `requests`, `per` (1s to 24h) and `action`. |
| `limits.count_by` | What one client is. `address`: an IPv4 address; for IPv6 the /64, which is one connection. `network`: an IPv4 /24 or an IPv6 /48. |
| `limits.exempt` | Addresses and networks that are never counted. |
| `limits.max_clients` | How many clients are tracked at most. |

Defaults and allowed values are in [CONFIGURATION.md](CONFIGURATION.md#limits).

## The two actions

| Action | What a client over the limit gets | Suits |
|---|---|---|
| `challenge` | The security check. A browser solves it once, receives its pass and carries on without interruption; a program that cannot solve it is stopped. | Short periods. People who share an address (an office, a school, a mobile network) are not locked out. |
| `deny` | The page "Too many requests" with status `429` and a `Retry-After` header, whether it has a pass or not. | Long periods with a generous number, as a hard ceiling. |

If a client is over several limits, `deny` wins.

**A `challenge` limit does not hold back a client that has passed the
check.** That is its purpose for people, and its gap for a program that
solves the check once and then asks for a great deal. Close the gap with a
second, higher limit with `deny`, as in the example above.

A limit only ever makes the outcome stricter. A request that a rule denies
stays denied.

## Who is never limited

- **Addresses in `limits.exempt`.** Your own network, your monitoring, a
  partner's server. Change the list in the configuration file and restart.
- **Requests let through by an allow rule that says
  `exempt_from_limits: true`.** The presets `allow-search-engines`,
  `allow-ai-search` and `allow-ai-user-fetch` say so: a verified search
  engine is not slowed down by a limit meant for impostors. On your own
  rules you decide; see [RULES.md](RULES.md#exempting-from-the-request-limits).

Everything else is counted, also requests that an ordinary allow rule lets
through. Anyone can ask for `/robots.txt` or a feed; if that exempted them,
it would be a way round the limits.

## Choosing numbers

Everything a page loads through Xibalba counts: the page itself, its images,
style sheets and scripts. One page view can be 50 requests or more.

- Start with `action: challenge` and a generous number.
- Use `rules.dry_run: true` first. In a dry run the limits count but stop
  nobody, and `/limits` shows how many requests would have been affected.
- **Behind a web server, set `server.trusted_proxies`.** Without it every
  visitor appears as the web server's address and all share one count.
  Xibalba warns in the log at start when it looks that way.
- Several people behind one address share one count. With `challenge` that
  costs each of them one check; with `deny` it locks all of them out.
- `count_by: network` catches crawlers that spread over many addresses of one
  network, and counts unrelated people of one network together. Use it with
  `challenge`, and with care.

## How counting works

Each limit has its own count per client. The count is a sliding estimate
over the last period, so a client cannot send twice the limit by sending half
just before and half just after a boundary. `Retry-After` is the moment
from which a client that has sent nothing more is let through again; the
further over the limit it was, the longer that is, up to two periods.
Requests that are refused are counted too.

Counting one request takes well under a microsecond and does not wait for
anything.

## Looking at what happens

`GET /limits` on the operations listener (only while limits are on):

```json
{
  "clients": 1,
  "exempt_requests": 0,
  "limits": [
    {
      "requests": 3,
      "per": "1m0s",
      "action": "challenge",
      "requests_over_limit": 2
    },
    {
      "requests": 5,
      "per": "1h0m0s",
      "action": "deny",
      "requests_over_limit": 2
    }
  ]
}
```

`clients` is how many clients are being counted right now.
`requests_over_limit` is how many requests were over that limit since the
start. The report holds no address.

## Privacy

To count, Xibalba has to remember client addresses for a while.

- They are kept in memory only, never written to a file or a log.
- A client that sends nothing more is forgotten after twice the longest
  period: with a limit per day, after two days at most.
- A restart forgets everything.
- Addresses in `limits.exempt` are not remembered at all.

A limit per minute or hour therefore keeps addresses far shorter than a limit
per day. Choose the longest period with that in mind.

## Known limits

- A crawler that uses a new address for every few requests stays under every
  limit per address. `count_by: network` helps while the addresses are in one
  network; against a crawler spread over many networks, use the security
  check (`rules.default_action: challenge`).
- When more clients are active than `limits.max_clients`, older entries make
  way and their counts start again.
- Counts are per Xibalba instance and are not shared between instances.
- Answers to the security check (`/.xibalba/`) are not counted.
- The exempt list is read at start. Editing it in the web interface is
  planned (milestone M8).
