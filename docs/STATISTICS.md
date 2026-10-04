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
| `trap\|hits`, `trap\|ignored` | The trap |

No address, no path, no user agent, no time finer than the hour. The names
come from your configuration (rule names) and from the crawler definitions.
Counts per network of origin are not kept.

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

## How it is kept

- Once a minute, and when Xibalba stops, the counters are looked at and what
  was added goes into the current hour. The current hour is written to
  `current.json` each time, as a whole, so a crash costs at most the last
  minute.
- A finished hour is appended as one line to that month's file,
  `hours-2026-10.jsonl`. The files are plain text (one JSON object per line)
  and can be read with any tool.
- Once a day, the files of months that lie wholly before `keep_days` are
  removed. Removal is by month: an hour is kept for `keep_days` and up to a
  month longer.
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
