# Metrics

`GET /metrics` on the operations listener serves Xibalba's numbers in the
Prometheus text format, for a monitoring system to collect. It needs no
setting. The numbers are the same that `/decisions`, `/crawlers`, `/limits`,
`/trap` and `/healthz` show; they hold no address, path or user agent.

The operations listener is reachable from the same machine only by default
(`ops.listen`). Keep it off the public internet.

```sh
curl http://127.0.0.1:9090/metrics
```

```text
# HELP xibalba_component_state State of each part: 0 ok, 1 degraded, 2 down.
# TYPE xibalba_component_state gauge
xibalba_component_state{component="limits"} 0
xibalba_component_state{component="ops"} 0
xibalba_component_state{component="public"} 0
xibalba_component_state{component="rules"} 0
xibalba_component_state{component="upstream"} 0
# HELP xibalba_decisions_total Decisions by what decided (rule, threshold or default) and the action taken.
# TYPE xibalba_decisions_total counter
xibalba_decisions_total{source="default",action="allow"} 0
```

## What is served

| Metric | Kind | Labels | Meaning |
|---|---|---|---|
| `xibalba_build_info` | gauge | `version` | The running build. Always 1. |
| `xibalba_start_time_seconds` | gauge | | When Xibalba started. |
| `xibalba_component_state` | gauge | `component` | State of each part: 0 ok, 1 degraded, 2 down. The parts are those of `/healthz`. |
| `xibalba_decisions_total` | counter | `source`, `action` | Decisions by what decided (`rule:<name>`, `threshold:<weight>`, `default`) and the action. |
| `xibalba_evaluation_failures_total` | counter | | Requests that could not be evaluated. |
| `xibalba_challenge_total` | counter | `result` | The security check: `served` pages, requests `passed` on a valid pass, answers `solved` and `failed`, and answers rejected because the browser reported automation (`automated`, with the `headless` check). |
| `xibalba_dry_run` | gauge | | 1 if decisions are only counted and not enforced. |
| `xibalba_crawler_requests_total` | counter | `crawler`, `class`, `status` | Requests that carried a known crawler's name: `verified`, `impostor`, `unverifiable`, `pending`. Counted only while a rule uses a `crawler` condition. |
| `xibalba_limit_clients` | gauge | | Clients being counted by the request limits. Only while limits are on. |
| `xibalba_limit_exempt_requests_total` | counter | | Requests from exempt addresses. |
| `xibalba_limit_over_total` | counter | `per`, `count`, `action` | Requests that were over a limit. |
| `xibalba_limit_over_deny_at_total` | counter | `per`, `count` | Of those, the requests refused because of `deny_at`. Only for limits that have one. |
| `xibalba_trap_hits_total` | counter | | Requests that followed the hidden link. Only while the trap is on. |
| `xibalba_trap_ignored_total` | counter | | Requests to the trap's addresses that were no catch. |
| `xibalba_trap_clients` | gauge | | Clients remembered as caught. May lag by up to a minute. |
| `xibalba_metrics_failures_total` | counter | | How often a part failed while its numbers were collected; its numbers are then missing from that answer. |

Counters start at zero with every start of Xibalba; a monitoring system
handles that. Xibalba can also keep its counters on disk by the hour
itself; see [STATISTICS.md](STATISTICS.md).

## Useful questions

| Question | Expression |
|---|---|
| Is anything wrong? | `max(xibalba_component_state) > 0` |
| How many requests are denied per minute? | `sum(rate(xibalba_decisions_total{action="deny"}[5m])) * 60` |
| Which rule denies most? | `topk(5, rate(xibalba_decisions_total{action="deny"}[1h]))` |
| Are checks being failed (a crawler stuck at the check)? | `rate(xibalba_challenge_total{result="served"}[10m])` against `{result="solved"}` |
| Is someone impersonating a crawler? | `sum by (crawler) (rate(xibalba_crawler_requests_total{status="impostor"}[1h]))` |

## Collecting

In the configuration of Prometheus:

```text
scrape_configs:
  - job_name: xibalba
    static_configs:
      - targets: ["127.0.0.1:9090"]
```

This has not been tried against a running Prometheus; the format is checked
by tests.
