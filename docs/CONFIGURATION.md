# Configuration

Xibalba reads one YAML file. Only `upstream.url` is required; every other
setting is optional and uses its default when left out. The smallest valid file is:

```yaml
upstream:
  url: "http://127.0.0.1:3000"
```

```sh
xibalba -config /etc/xibalba/xibalba.yaml
```

| Flag | Meaning |
|---|---|
| `-config <path>` | Configuration file. Default: `xibalba.yaml` in the current directory. |
| `-check` | Validate the file, print the result, and exit without starting. |
| `-version` | Print the version and exit. |

## How mistakes are reported

Xibalba refuses to start with a file it does not fully understand. It lists
every problem at once, with the line, the setting and the fix:

```text
configuration xibalba.yaml: 2 problems
  - line 2, log.level: "loud" is not a log level
    fix: use one of: debug, info, warn, error
  - line 5, ops.listen: "localhost" is not a listen address: it must have the form host:port
    fix: use host:port, for example "127.0.0.1:9090"
```

A password inside `upstream.url` is rejected and never printed.

A setting that does not exist (usually a typo) is also an error. Xibalba never
ignores a setting silently.

## Settings

### `upstream`

The website Xibalba protects.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `upstream.url` | none, required | `http://` or `https://` URL with a host | Where the website is reached from the machine Xibalba runs on. No query, fragment, user name or password. |
| `upstream.preserve_host` | `true` | `true`, `false` | `true` sends the visitor's `Host` header to the website. `false` sends the host from `upstream.url`. Most websites need `true` to build correct links. |
| `upstream.dial_timeout` | `5s` | Duration greater than zero | How long connecting to the website may take. |
| `upstream.response_header_timeout` | `60s` | Duration greater than zero | How long the website may take to start answering. After that the visitor gets a `504`. |

When the website cannot be reached, the visitor gets a short page in German
and English saying the website is currently unavailable, with status `502`
(not reachable) or `504` (too slow). The page shows no internal address. The
cause is shown in `/healthz` and written to the log under `component=upstream`:
once when the website stops answering and once when it answers again, not
once per request.

A status code from the website itself, including `500`, is passed through
unchanged and does not count as a failure of the upstream.

### `server`

The public side: where visitors, or the web server in front of Xibalba, connect.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `server.listen` | `127.0.0.1:8080` | `host:port` | Address to listen on. The default is reachable from the same machine only. |
| `server.trusted_proxies` | `[]` | List of IP addresses and networks, such as `127.0.0.1` or `10.0.0.0/8` | Reverse proxies and load balancers in front of Xibalba. See below. |
| `server.read_header_timeout` | `10s` | Duration greater than zero | How long a client may take to send its request headers. |
| `server.idle_timeout` | `90s` | Duration greater than zero | How long an unused keep-alive connection stays open. |

Xibalba does not terminate TLS yet. Put it behind the web server or load
balancer that holds your certificate, and list that machine in `trusted_proxies`.

Uploads, downloads, streams and websockets pass through without an overall
time limit.

#### The real client address and `trusted_proxies`

Every decision Xibalba makes about a visitor depends on knowing their real
address. The `X-Forwarded-For` header is plain text that any client can send,
so Xibalba believes it only when the connection comes from an address listed
in `trusted_proxies`.

| Situation | Setting | Client address used |
|---|---|---|
| Visitors connect to Xibalba directly | `trusted_proxies: []` | The connection's address. Forwarding headers from the client are ignored. |
| nginx or Caddy on the same machine in front of Xibalba | `trusted_proxies: ["127.0.0.1", "::1"]` | The address your web server reports. |
| Load balancer in a private network | `trusted_proxies: ["10.0.0.0/8"]` | The first address in the chain that is not one of your proxies. |

If a proxy sits in front of Xibalba and is not listed, every visitor appears
to have the proxy's address. If an address is listed that is not really your
proxy, whoever controls it can pretend to be any visitor. List exactly your own proxies.

What the website receives:

| Header | Value |
|---|---|
| `X-Forwarded-For` | From a trusted proxy: its chain plus the proxy's address. Otherwise: only the connection's address. |
| `X-Forwarded-Proto`, `X-Forwarded-Host` | From a trusted proxy: its values. Otherwise: what Xibalba observed. |
| `X-Real-IP` | The client address Xibalba resolved. A value sent by the client is never passed on. |
| `Forwarded` | Removed. |

### `rules`

What happens to each request. How rules are written is explained in
[RULES.md](RULES.md); this table lists the settings around them.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `rules.dry_run` | `false` | `true`, `false` | `true` evaluates and counts every decision but lets every request through. |
| `rules.default_action` | `allow` | `allow`, `deny`, `challenge` | What happens when no rule decides and no threshold is reached. |
| `rules.on_error` | `allow` | `allow`, `deny` | What happens to a request if evaluating it fails inside Xibalba. `allow` keeps the website reachable; `deny` answers `503` until the problem is fixed. |
| `rules.thresholds` | `[]` | List of `{weight, action}`; weight 1 to 1000, action `challenge` or `deny` | Scores at which a request is challenged or denied. |
| `rules.files` | `[]` | List of paths, relative to the configuration file | Rule files to import. Evaluated after `rules.list`, in the order given. |
| `rules.list` | `[]` | List of rules | Rules written in the configuration file. Evaluated first, top to bottom. |

With the defaults nothing is blocked. The action `challenge` is accepted but
the challenge is **planned**: until it is built, requests that would be
challenged are counted and let through, and Xibalba says so in the log at start-up.

### `log`

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `log.level` | `info` | `debug`, `info`, `warn`, `error` | Lowest severity that is written. |
| `log.format` | `json` | `json`, `text` | `json` writes one object per line for log collectors. `text` writes `key=value` lines for reading by eye. |

Logs go to standard error. Every line carries a `component` attribute naming
the part of the program that wrote it.

### `ops`

The operations listener serves internal endpoints. It is separate from the
public side so that health checks keep working when the public side is under load.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `ops.listen` | `127.0.0.1:9090` | `host:port` | Address to listen on. Use port `0` to let the system pick a free port. |

Keep this listener off the public internet. The default is reachable from the
same machine only.

Endpoints:

| Path | Answer |
|---|---|
| `GET /healthz` | JSON health report. Status `200` while Xibalba can serve, `503` when a component of Xibalba is down. |
| `GET /version` | JSON with the version, commit and Go version of the running build. |
| `GET /decisions` | JSON with how often each rule, threshold and the default decided since start. Holds no address, path or user agent. Example in [RULES.md](RULES.md#trying-a-rule-set-safely). |

Example health report:

```json
{
  "state": "degraded",
  "components": {
    "ops": { "state": "ok" },
    "public": { "state": "ok" },
    "rules": { "state": "ok" },
    "upstream": {
      "state": "degraded",
      "detail": "the last request to 127.0.0.1:3000 failed: dial tcp 127.0.0.1:3000: connect: connection refused"
    }
  }
}
```

`state` is `ok`, `degraded` (working with reduced function) or `down`. The
top-level state is the worst state of any component. A component that is not
`ok` carries a `detail` text that says why.

| Component | What it is | Not `ok` when |
|---|---|---|
| `ops` | The operations listener | It stopped listening (`down`). |
| `public` | The public listener | It stopped listening (`down`). |
| `rules` | The evaluation of requests against the rule set | A request could not be evaluated in the last five minutes (`degraded`). The detail says how many, why, and whether they were allowed or refused. |
| `upstream` | The connection to your website | The most recent request to the website failed (`degraded`). It returns to `ok` with the next request the website answers. |

An unreachable website is `degraded`, not `down`, and `/healthz` stays at
`200`: Xibalba itself works, and restarting it would not help.

### `shutdown_timeout`

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `shutdown_timeout` | `10s` | A duration greater than zero, such as `5s`, `1m` | How long running requests get to finish after Xibalba is asked to stop (SIGINT or SIGTERM). |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Clean shutdown, or `-check` / `-version` succeeded. |
| `1` | Invalid configuration, a component could not start, or a component failed while running. |
| `2` | Invalid command line. |

## Complete example

[`xibalba.example.yaml`](../xibalba.example.yaml) lists every setting with its
default (and a typical value for `upstream.url`). A test keeps that file in
step with the code. [`examples/rules/basic.yaml`](../examples/rules/basic.yaml)
is a commented rule file to copy from.
