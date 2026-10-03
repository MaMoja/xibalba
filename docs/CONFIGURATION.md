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
Tested configurations for nginx and Caddy are in
[`examples/nginx/xibalba.conf`](../examples/nginx/xibalba.conf) and
[`examples/caddy/Caddyfile`](../examples/caddy/Caddyfile).

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
| `rules.presets` | `[]` | List of: `block-fake-crawlers`, `block-ai-training`, `block-archive-crawlers`, `allow-search-engines`, `allow-ai-search`, `allow-ai-user-fetch` | Ready-made rule groups about crawlers. Evaluated after `rules.list` and before `rules.files`, in the order given. See [CRAWLERS.md](CRAWLERS.md#presets). |
| `rules.files` | `[]` | List of paths, relative to the configuration file | Rule files to import. Evaluated after `rules.list` and the presets, in the order given. |
| `rules.list` | `[]` | List of rules | Rules written in the configuration file. Evaluated first, top to bottom. |

With the defaults nothing is blocked and nobody is challenged.

### `crawlers`

How crawlers are recognised and verified; explained in [CRAWLERS.md](CRAWLERS.md).
These settings only take effect if a rule or preset has a `crawler` condition.
Without one, Xibalba makes no outgoing connection.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `crawlers.builtin` | `true` | `true`, `false` | Use the crawler definitions that ship with Xibalba. `false`: only the crawlers from `crawlers.files` are known. |
| `crawlers.refresh` | `true` | `true`, `false` | Download the address lists that crawler operators publish. `false`: no downloads; crawlers verified by such a list are never counted as genuine. |
| `crawlers.refresh_interval` | `24h` | `1h` to `720h` | How often the lists are downloaded again. |
| `crawlers.cache_dir` | empty | Path of an existing directory, relative to the configuration file | Keeps the downloaded lists across restarts. Empty: memory only. Must be writable for Xibalba's user and for nobody else. |
| `crawlers.files` | `[]` | List of paths, relative to the configuration file; at most 64 | Your own crawler definition files. A crawler defined there replaces the built-in one of the same name. |

### `limits`

Request limits per client; explained in [LIMITS.md](LIMITS.md).

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `limits.enabled` | `false` | `true`, `false` | Switches the limits on. |
| `limits.count_by` | `address` | `address`, `network` | What one client is. `address`: an IPv4 address, an IPv6 /64. `network`: an IPv4 /24, an IPv6 /48. |
| `limits.windows` | one limit: 300 requests per `1m`, `challenge` | One to four entries `{requests, per, action}`; requests 1 to 10000000; per `1s` to `24h`, each period once; action `challenge` or `deny` | The limits. Over a `challenge` limit a client has to pass the security check; over a `deny` limit it gets status `429`. |
| `limits.exempt` | `[]` | List of IP addresses and networks | Clients that are never counted. |
| `limits.max_clients` | `100000` | 1000 to 5000000 | How many clients are tracked at most. About 15 MB per 100000. |

The settings are checked even while `limits.enabled` is `false`. Requests
that a rule explicitly allows are never counted.

### `challenge`

The security check a client has to pass when a rule or threshold decides
`challenge`. [CHALLENGE.md](CHALLENGE.md) explains how it works and what to
consider; this table lists the settings.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `challenge.difficulty` | `18` | `8` to `24` | How much the visitor's browser has to calculate. Each step up doubles the work. |
| `challenge.no_javascript` | `button` | `button`, `deny` | What visitors without JavaScript get: wait and press a button, or a note that JavaScript is needed. |
| `challenge.wait` | `3s` | `1s` to `1m` | How long a visitor without JavaScript has to wait before the button counts. |
| `challenge.challenge_lifetime` | `5m` | `30s` to `1h`, longer than `wait` | How long a client has to finish before it gets a new task. |
| `challenge.pass_lifetime` | `168h` | `1m` to `8760h` | How long a client is not asked again after passing. Use hours: a week is `168h`. |
| `challenge.bind_network` | `true` | `true`, `false` | Tie the pass to the visitor's network as well as their browser. |
| `challenge.key_file` | empty | Path, relative to the configuration file | File holding the signing key; created at the first start. Empty means a new key at every start, so every visitor is checked again after a restart. **Set this for real use.** |
| `challenge.cookie_name` | `xibalba-pass` | Letters, digits, `-`, `_`; up to 64 characters | Name of the cookie that holds the pass. |

Addresses under `/.xibalba/` are answered by Xibalba itself and never reach
your website.

### `pages`

The pages Xibalba itself shows to visitors: the security check, "request
blocked" and "website unavailable". They work without any setting. Use this section to put your
name on them or to change the wording.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `pages.attribution` | `true` | `true`, `false` | Show the line "Protected by Xibalba" with links to the project at the bottom of every page. **`false` needs a sponsor license.** |
| `pages.operator` | empty | Text, up to 200 characters | **Needs a sponsor license.** Who runs the website, as it should read in a sentence, for example `"Stadt Musterhausen"`. Replaces the neutral phrase "The operator of this website" in every language. |
| `pages.contact` | empty | Text, up to 200 characters | How to reach you: an e-mail address, a telephone number, an office. Shown as a line on the block page. Empty shows no contact line. |
| `pages.default_language` | `de` | `de`, `en` | Language for visitors whose browser states none of the supported languages. Every page offers the other language as well. |
| `pages.texts` | `{}` | Language, then text name, then text (up to 1000 characters) | **Needs a sponsor license.** Replaces single texts. Texts you do not list keep their built-in wording. |

Text names for `pages.texts`:

| Name | Where it appears | Built-in English text |
|---|---|---|
| `operator` | Inside other texts, where they say `{operator}` | The operator of this website |
| `blocked_title` | Heading and window title of the block page | This request was blocked |
| `blocked_text` | Paragraph of the block page | {operator} does not allow requests of this kind. If you think this is a mistake, please get in touch and quote the following reference. |
| `limited_title` | Heading and window title of the page for a client over a `deny` limit | Too many requests |
| `limited_text` | Paragraph of that page | A large number of requests came from your connection in a short time. {operator} is therefore limiting access for a while. Please try again a little later. |
| `reference_label` | In front of the reference on the block page | Reference: |
| `contact_label` | In front of `pages.contact` on the block page | Contact: |
| `unavailable_title` | Heading and window title of the unavailable page | The website is currently unavailable |
| `unavailable_text` | Paragraph of the unavailable page | Please try again in a few minutes. |
| `language_name` | Label of the language switch | English |
| `challenge_title` | Heading and window title of the security check | A quick security check |
| `challenge_text` | First paragraph of the security check | {operator} protects these pages against automated mass requests. Your browser is solving a short calculation for this. It usually takes only a few seconds; you do not need to do anything. |
| `challenge_cookie` | Second paragraph of the security check | Afterwards a cookie is stored that only records that the check was passed. |
| `challenge_working` | Status while the browser calculates | The check is running … |
| `challenge_done` | Status when the browser has finished | Check passed. You are being forwarded. |
| `challenge_manual` | Shown to visitors without JavaScript | Your browser does not run JavaScript. Please wait a few seconds and then choose “Continue”. |
| `challenge_button` | The button for visitors without JavaScript | Continue |
| `challenge_needs_script` | Shown instead of the button when `challenge.no_javascript` is `deny` | JavaScript must be switched on for this check. Please switch JavaScript on and reload the page. |
| `challenge_too_early` | Notice when the button was pressed before the waiting time was over | That was a little too fast. Please wait a few seconds and then choose “Continue” again. |
| `challenge_retry` | Notice when an answer was not accepted and the check started again | The check could not be completed and has been started again. |

`{operator}` inside a text is replaced by the operator: `pages.operator` if
set, or the `operator` text of that language. It is the only placeholder.

The two texts of the Xibalba line (`attribution_text`, `attribution_sponsor`)
are fixed and cannot be replaced; the line can only be shown or hidden.

Example: a name that needs a different form in each language, and a reworded
German paragraph.

```yaml
pages:
  contact: "webmaster@musterhausen.example"
  texts:
    de:
      operator: "Die Stadt Musterhausen"
      blocked_text: "{operator} erlaubt keine automatisierten Abrufe. Bei Fragen nennen Sie bitte die folgende Referenz."
    en:
      operator: "The City of Musterhausen"
```

Everything you write here is shown as plain text. HTML in a text is displayed
literally and never interpreted, so a typo cannot break the page.

### `license`

Xibalba is free and complete without a license. Sponsors of the project
receive a license file that unlocks `pages.operator`, `pages.texts` and
`pages.attribution: false`. [SPONSORS.md](SPONSORS.md) explains what that
means, how the check works, and what happens when a license runs out.

| Setting | Default | Allowed values | Meaning |
|---|---|---|---|
| `license.file` | empty | Path, relative to the configuration file | The sponsor license file. Empty means no license. |

A license file that is missing, damaged or not issued by the project is an
error at start-up. A license that has expired is not: Xibalba starts, the
pages return to their standard form, and the log and `/healthz` say why.

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
| `GET /limits` | Only while `limits.enabled` is `true`. JSON with the limits, how many clients are being counted and how many requests were over each limit. Holds no address. Example in [LIMITS.md](LIMITS.md#looking-at-what-happens). |
| `GET /crawlers` | JSON with every known crawler: operator, class, source, how it is verified, the state of its address list, and how many requests claimed to be it. Holds no client address. Example in [CRAWLERS.md](CRAWLERS.md#looking-at-what-happens). |
| `GET /decisions` | JSON with how often each rule, threshold and the default decided since start, and what became of challenged requests. Holds no address, path or user agent. Examples in [RULES.md](RULES.md#trying-a-rule-set-safely) and [CHALLENGE.md](CHALLENGE.md#watching-it-work). |

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
| `crawlers` | Crawler verification. Listed only if a rule or preset has a `crawler` condition. | An address list is missing, out of date or too old (`degraded`). The detail names the list and the reason. The crawlers concerned are not counted as genuine until it is back. |
| `license` | The sponsor license. Listed only if `license.file` is set. | It has expired (`degraded`). The detail gives the dates and says what applies. Xibalba keeps running. |
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
