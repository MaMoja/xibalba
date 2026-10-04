# Statistics

Xibalba can keep its counters on disk, by the hour, so that they survive a
restart and can be looked at over weeks and months. Without it, the numbers
under `/decisions`, `/crawlers`, `/limits` and `/trap` start at zero with
every start.

Statistics are off until you name a directory.

```yaml
statistics:
  directory: /var/lib/xibalba/statistics
  keep_days: 400
```

| Setting | Meaning |
|---|---|
| `statistics.directory` | Where the files are kept. The directory must exist and be writable for Xibalba's user. Empty: nothing is kept. |
| `statistics.keep_days` | How long an hour's counts are kept. Default 400, a little over a year. |

## What is stored

Counts, under names. Nothing else.

| Name | Counts |
|---|---|
| `decision\|<source>\|<action>` | Decisions by what decided (`rule:<name>`, `threshold:<weight>`, `default`) and the action |
| `challenge\|served`, `passed`, `solved`, `failed` | The security check |
| `failures` | Requests that could not be evaluated |
| `crawler\|<name>\|verified`, `impostor`, `unverifiable`, `pending` | Requests that carried a known crawler's name |
| `limit\|<per>\|<count>\|<action>` | Requests that were over a limit |
| `limit\|<per>\|<count>\|deny_at` | Of those, the requests refused because of `deny_at` |
| `trap\|hits`, `trap\|ignored` | The trap |

No address, no path, no user agent, no time finer than the hour. The names
come from your configuration (rule names) and from the crawler definitions.
Counts per network of origin are kept only if you switch them on; see
[Counts per network](#counts-per-network).

## Looking at them

`GET /statistics` on the operations listener returns the last 24 hours;
`?hours=N` returns the last N.

```sh
curl "http://127.0.0.1:9090/statistics?hours=24"
```

```json
{
  "from": "2026-10-03T08:00:00Z",
  "to": "2026-10-04T07:00:00Z",
  "totals": {
    "challenge|served": 1,
    "crawler|GPTBot|pending": 2,
    "decision|default|allow": 1,
    "decision|rule:preset.block-ai-training|deny": 2,
    "decision|rule:preset.challenge-browsers|challenge": 1,
    "decision|rule:preset.keep-internet-working|allow": 1
  },
  "hours": [
    {
      "hour": "2026-10-04T07:00:00Z",
      "counts": {
        "challenge|served": 1,
        "crawler|GPTBot|pending": 2,
        "decision|default|allow": 1,
        "decision|rule:preset.block-ai-training|deny": 2,
        "decision|rule:preset.challenge-browsers|challenge": 1,
        "decision|rule:preset.keep-internet-working|allow": 1
      }
    }
  ]
}
```

Hours are in UTC. An hour in which nothing was counted is left out, and so is
a name to which nothing was added. A web interface that shows these numbers
as charts is **planned** (milestone M7).

## Counts per network

Which networks do most requests come from, and what happened to them? This
is an option and off by default.

```yaml
statistics:
  directory: /var/lib/xibalba/statistics
  networks:
    enabled: true
    top: 50
    keep_days: 30
```

| Setting | Meaning |
|---|---|
| `statistics.networks.enabled` | `true` switches the counts on. Needs `statistics.directory`. |
| `statistics.networks.top` | How many networks are kept per hour (1 to 1000). All others are summed up as `other`. |
| `statistics.networks.keep_days` | How many days these counts are kept (1 to 400). Shorter than the general statistics on purpose. |

A network is an IPv4 `/24` (256 neighbouring addresses) or an IPv6 `/48`
(what a provider typically hands to one customer site or more). **No single
address is stored**, and the counts are kept apart from the general
statistics, in the subdirectory `networks`, with their own time limit.

`GET /statistics/networks` on the operations listener returns them, with
`?hours=N` as above:

```sh
curl "http://127.0.0.1:9090/statistics/networks?hours=1"
```

```json
{
  "from": "2026-10-04T07:00:00Z",
  "to": "2026-10-04T07:00:00Z",
  "totals": {
    "network|127.0.0.0/24|allow": 3,
    "network|127.0.0.0/24|deny": 1
  },
  "hours": [
    {
      "hour": "2026-10-04T07:00:00Z",
      "counts": {
        "network|127.0.0.0/24|allow": 3,
        "network|127.0.0.0/24|deny": 1
      }
    }
  ]
}
```

The names are `network|<network>|<outcome>`; the outcome is `allow`,
`challenge` or `deny`, as decided for the request (a request refused by a
limit counts as `deny`). With `rules.dry_run` the outcome is what would have
happened.

Things to know:

- **Privacy.** A network is not a person, but it is closer to one than a
  rule name: a small organisation can have a `/24` or `/48` to itself. That
  is why the option is off by default, why only the largest networks of an
  hour are kept, and why the time limit is short. Mention it in your privacy
  notice if you switch it on.
- Behind a web server or load balancer, `server.trusted_proxies` must be
  set, or every request appears to come from that server's network.
- The top is taken per hour, when the hour is over. A network that is never
  among the largest of an hour only appears in `other`. The hour that is
  still running can hold more networks: up to three times `top`; beyond
  that it is reduced at once. Nothing counted is lost by this, it moves to
  `other`; counts of small networks are then approximate.
- Within one minute, at most 10000 different networks are told apart; in a
  flood from more networks, the rest of that minute counts as `other`.
- **Switching the option off removes the counts per network** that were
  kept, at the next start. Data that nothing looks after must not stay.
- The component is `statistics-networks` in `/healthz`.

## How it is kept

- Once a minute, and when Xibalba stops, the counters are looked at and what
  was added goes into the current hour. The current hour is written to
  `current.json` each time, as a whole, so a crash costs at most the last
  minute.
- A finished hour is appended as one line to that month's file,
  `hours-2026-10.jsonl`. The files are plain text (one JSON object per line)
  and can be read with any tool.
- Once a day, and at every start, hours older than `keep_days` are removed:
  whole files of months that are over, and the old lines of the month the
  limit falls into. An hour is kept for `keep_days` and at most a day longer.
- The files are readable by Xibalba's user only.

A year of statistics is a few megabytes. No database is involved.

## When something goes wrong

Statistics are beside the path of requests. If the directory is gone or the
disk is full, requests are served as before; `statistics` turns `degraded`
in `/healthz` and says why, and one warning is logged. What was counted in
the meantime is written when writing works again, as long as Xibalba keeps
running. A damaged line in a file costs that hour, not the file.

## Known limits

- Counts are per Xibalba instance. Two instances need two directories.
- A change of a rule's name starts a new series; the old one stays under the
  old name.
- The counters are also served live for a monitoring system, see
  [METRICS.md](METRICS.md). The two are independent.
