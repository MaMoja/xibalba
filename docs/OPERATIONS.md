# Operations

Checking, watching and changing a running Xibalba, and what to do when
something is wrong. Everything here uses the operations listener
(`ops.listen`, by default `127.0.0.1:9090`), which is reachable from the
same machine only.

## Health

```sh
curl http://127.0.0.1:9090/healthz
```

The report names every part by itself. `ok` is fine, `degraded` is working
with reduced function, `down` is failed. With a problem, `detail` says what
is wrong. The parts and what makes each of them not `ok` are listed in
[Configuration](CONFIGURATION.md#ops).

If your website cannot be reached, Xibalba stays up: visitors get the page
"The website is currently unavailable", and `upstream` shows `degraded` with
the cause. `/healthz` answers with an error status (503) only when Xibalba
itself has failed. That is the value a monitoring system should watch.

## Counters

```sh
curl http://127.0.0.1:9090/decisions
```

```json
{
  "dry_run": false,
  "since": "2026-10-03T08:57:15Z",
  "totals": {
    "allow": 0,
    "challenge": 1,
    "deny": 0
  },
  "failures": 0,
  "challenge": {
    "served": 1,
    "passed": 0,
    "solved": 0,
    "failed": 0
  },
  "sources": [
    {
      "source": "rule:challenge-search",
      "action": "challenge",
      "reference": "5f54bba9",
      "count": 1
    },
    {
      "source": "threshold:10",
      "action": "challenge",
      "reference": "97e55a91",
      "count": 0
    },
    {
      "source": "default",
      "action": "allow",
      "reference": "37a8eec1",
      "count": 0
    }
  ]
}
```

`sources` lists every rule, threshold and the default with how often it
decided. Under `challenge`, `served` is how often the check page was shown,
`solved` how many answers were accepted, `passed` how many requests were let
through on an earlier pass. Many `served` and few `solved` is the picture of
a crawler that fails the check.

More endpoints, each only while its feature is on:

| Endpoint | Shows |
|---|---|
| `/crawlers` | every known crawler, the state of its address list, and how many requests claimed to be it |
| `/limits` | the request limits and how many requests were over each |
| `/trap` | how many requests followed the hidden link |
| `/statistics` | the counters of the last hours, kept on disk; see [Statistics](STATISTICS.md) |
| `/metrics` | everything above for a monitoring system; see [Metrics](METRICS.md) |

None of them holds an address, a path or a user agent.

## A visitor says they were blocked by mistake

The block page shows a reference such as `5f54bba9`. It names the rule, not
the visitor. Look for the code in `/decisions`; the rule's name is next to
it. That is how you find the cause without Xibalba having to record who was
blocked.

## Log

Xibalba writes to standard error; under systemd that ends up in the journal.
Every line names, with `component=`, the part that wrote it. For reading by
eye:

```yaml
log:
  format: text
```

Single requests and decisions are not logged, also not blocked ones. An
outage of your website is reported once when it begins and once when it
ends, not for every request.

## Changing something

1. Change the file.
2. `xibalba -check -config /etc/xibalba/xibalba.yaml`
3. Restart Xibalba.

Xibalba reads the configuration at start only; reloading while running is
**planned**. With a key file set, visitors keep their pass across the
restart. Two things are picked up without a restart: a replaced country
database, and the crawler address lists.

## Updating

Build the new version, replace the program file, restart. Read
`CHANGELOG.md` first: as long as there is no version 1.0, settings can
change; every such change is listed there.

## Trying what a client experiences

```sh
curl -i -A "ExampleBot/1.0" http://127.0.0.1:8080/
```

```text
HTTP/1.1 403 Forbidden
```

## Troubleshooting

| What you see | Cause | What to do |
|---|---|---|
| Xibalba does not start | A mistake in the configuration, a port in use, or a problem with the key file | The message names file, line and fix. `xibalba -check -config …` shows all configuration mistakes at once. |
| `start-up failed … address already in use` | The port is taken | Choose another port in `server.listen` or `ops.listen`, or stop the other program. |
| `this setting needs a sponsor license` | One of `pages.operator`, `pages.texts` or `pages.attribution: false` is used without a license | Remove the setting, or name the license file under `license.file`. See [Sponsors](SPONSORS.md). |
| `is not a usable license … not genuine` | The license file was changed, copied incompletely or is not from the project | Copy the file you received again, unchanged. |
| Your own texts are gone and the Xibalba line is back | The sponsor license has expired | `/healthz` gives the date under `license`. Put the new license file in place and restart. |
| Visitors see "The website is currently unavailable" | Your website does not answer, or too slowly | Look at `/healthz`: the cause is under `upstream`. Check `upstream.url`. For a slow website raise `upstream.response_header_timeout`. |
| An address rule matches everybody or nobody | `server.trusted_proxies` is missing; Xibalba sees only your web server's address | [Getting started, step 5](GETTING-STARTED.md#step-5-tell-xibalba-which-web-server-to-believe). |
| After every restart everybody is checked again | No key file | Set `challenge.key_file`. The log has a warning about it. |
| Visitors are checked again at every page | The browser does not accept the cookie or does not send it back | Check whether a cache in front of Xibalba stores the check page. Check that your web server sends `X-Forwarded-Proto` and is listed in `trusted_proxies`. If your visitors' addresses change all the time, consider `challenge.bind_network: false`. |
| A form loses its input after the check | The form's target is checked, the page with the form is not | Have the page with the form checked as well. See [Challenge](CHALLENGE.md). |
| An API client or monitor suddenly gets 403 | It falls under a `challenge` or `deny` rule | Look in `/decisions` which rule counts. Put an `allow` rule above it. |
| `crawlers` is `degraded`, `list_error` in `/crawlers` | The server cannot reach the operators' address lists (firewall, proxy, no internet access) | Allow outgoing HTTPS to the addresses named in `detail`. Until then the crawlers concerned are not counted as genuine. |
| A genuine search engine is checked or blocked | Its address list is missing, or the DNS lookup has not been answered yet | Look at `/crawlers`: `addresses` and `requests.pending`. With DNS verification the first request from a new address is always "unknown". |
| Visitors see "Too many requests" | A `deny` limit is too low, or many people share one address | Raise the limit, switch to `challenge`, or put the address into `limits.exempt`. `/limits` shows which limit is hit. |
| `countries` is `degraded` | The country database is missing, damaged, out of date or could not be downloaded | `detail` gives the reason. Replace the file or allow outgoing HTTPS to the provider. Until then rules about countries are skipped. |
| `statistics` is `degraded` | The statistics directory cannot be written to | `detail` gives the reason. Requests are not affected. |
| A rule does not take effect | A rule further up decides first, or the condition does not hold | `/decisions` shows which rule counts instead. Reproduce with `curl -A "…"`. |
| A rule blocks too much | `prefix` or `contains` matches more than intended | Make it more exact; `prefix: "/admin"` also matches `/administrator`. Test in a dry run first. |
| Not sure what a change will do | | `rules.dry_run: true`, watch the counters, then switch it off. |
