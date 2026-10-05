# Asking instead of passing through (subrequest authentication)

Xibalba can work in two ways.

1. **In the path** (the usual way): your web server hands every request to
   Xibalba, and Xibalba hands the good ones to the website.
2. **Asked for a verdict**: your web server keeps talking to the website
   itself and only asks Xibalba before each request whether it may pass.
   nginx calls this `auth_request`, Caddy `forward_auth`, Traefik
   `forwardAuth`.

Use the second way if you want to leave your web server's configuration as
it is and add Xibalba beside it, or if the website is not reached over HTTP
at all (PHP through FastCGI, files served by the web server itself). In
every other case the first way is simpler and does a little more; the
differences are listed [below](#what-differs).

Off by default.

## Switching it on

```yaml
server:
  listen: "127.0.0.1:8080"
  trusted_proxies: ["127.0.0.1"]   # the web server that asks
verdict:
  enabled: true
```

`upstream.url` may be left out: Xibalba then gives verdicts only and
answers "not found" at every other address. With `upstream.url` set, both
ways work side by side.

Questions are answered only for a trusted proxy. Without
`server.trusted_proxies` Xibalba does not start:

```
  - line 2, verdict.enabled: checks are answered only for a trusted proxy, and server.trusted_proxies is empty
    fix: name the web server that asks, for example server.trusted_proxies: ["127.0.0.1"]
```

## The web server's side

| Web server | Example file | Tested |
|---|---|---|
| nginx | [`examples/nginx/xibalba-verdict.conf`](../examples/nginx/xibalba-verdict.conf) | Yes, with nginx 1.24 |
| Caddy | [`examples/caddy/Caddyfile.verdict`](../examples/caddy/Caddyfile.verdict) | Yes, with Caddy 2.6 |
| Traefik | [`examples/traefik/dynamic-verdict.yml`](../examples/traefik/dynamic-verdict.yml) | No: written from Traefik's documentation, not run yet |

"Tested" means `make webserver-check` starts the real web server with the
example file and tries: an allowed request, a denied one, a rule on the
visitor's address with a made-up `X-Forwarded-For`, a rule on the request
method, the security check from the page to the pass, and that every
request is counted once.

Each example does three things:

- It sends requests for `/.xibalba/…` to Xibalba. That is where a visitor's
  answer to the security check goes.
- It asks `/.xibalba/check` before every other request.
- It keeps visitors from asking that question themselves.

## What Xibalba is asked and what it answers

The question is a request to `/.xibalba/check`. It carries the visitor's
own headers (user agent, cookies, language) and:

| Header | Meaning |
|---|---|
| `X-Forwarded-Uri` | The path and query the visitor asked for. Required. |
| `X-Forwarded-Method` | The visitor's request method. `GET` if left out. |
| `X-Forwarded-Host` | The host name the visitor asked for. The `Host` header if left out. |
| `X-Forwarded-For` | The visitor's address, as everywhere else. |

Caddy and Traefik set these by themselves; the nginx example sets them.
The rules then see the visitor's request, not the question.

The answer (from a real run):

```
$ curl -s -o /dev/null -D - -H 'X-Forwarded-Uri: /page' http://127.0.0.1:8080/.xibalba/check
HTTP/1.1 204 No Content
X-Xibalba-Verdict: pass
$ curl -s -o /dev/null -D - -H 'X-Forwarded-Uri: /wiki/Start' http://127.0.0.1:8080/.xibalba/check
HTTP/1.1 401 Unauthorized
X-Xibalba-Verdict: challenge
$ curl -s -o /dev/null -D - -H 'X-Forwarded-Uri: /admin' http://127.0.0.1:8080/.xibalba/check
HTTP/1.1 403 Forbidden
X-Xibalba-Verdict: deny
```

| Status | `X-Xibalba-Verdict` | Meaning | Body |
|---|---|---|---|
| 204 | `pass` | Let the request through. | none |
| 401 | `challenge` | The security check is due. | the challenge page |
| 403 | `deny` | Refused by a rule. | the block page |
| 403 | `limited` | Refused by a request limit; `Retry-After` says for how long. | the "too many requests" page |
| 503 | `unavailable` | Xibalba could not evaluate the request and `rules.on_error` is `deny`. | the "unavailable" page |

Caddy and Traefik show the visitor the body that came with the answer.
nginx does not pass on the body of such an answer, so the nginx example
fetches the page with a second request, to `/.xibalba/page`. That request
is not decided about or counted again: rules, limits and statistics see
every visitor request once.

A visitor who passes the security check gets the pass cookie from
`/.xibalba/verify` and is sent back to the page they wanted, on the same
website. Xibalba never sends a visitor to another host, so there is no list
of allowed redirect domains to keep.

## What differs

| | In the path | Asked for a verdict |
|---|---|---|
| Rules, crawler identity, countries, the trap, request limits by `count: requests`, the security check | Yes | Yes |
| Request limits by `count: pages` | Yes | No: Xibalba does not see the website's answers. Refused at start-up if `upstream.url` is empty. |
| Link previews | Yes | Only with `upstream.url` set, or with fixed tags (`previews.tags`) |
| The website sees the pass cookie | No, Xibalba removes it | Yes |
| "Website unavailable" page | Xibalba's | Your web server's |
| Status of the security check and the block page | `pages.status` | nginx: `pages.status`. Caddy, Traefik: 401 for the check, 403 for a block, because that is what makes them stop the request |
| Status of "too many requests" | 429 | nginx: 429. Caddy, Traefik: 403, with `Retry-After` |
| Cost per request | One pass through Xibalba | One extra local request |

## Things to mind

- **Do not set `pages.status.challenge: 200` and expect it in the answer to
  the question.** A web server reads 2xx as "pass". Xibalba therefore
  always answers the question with 401 or 403, whatever `pages.status`
  says.
- **Leave out what needs no check.** Every location that does not ask
  (`auth_request off` in nginx, a route without the middleware elsewhere) is
  not protected. That is useful for static files and has to be a decision.
- **The question must come from the web server only.** The nginx example
  marks it `internal`, the Caddy example answers visitors "not found". If a
  visitor could ask it, they would learn what Xibalba thinks of a request
  from their own address, and no more.
- **Numbers.** `/metrics` has `xibalba_verdicts_total` by outcome.
  `refused` counts questions that were not answered: not from a trusted
  proxy, or without `X-Forwarded-Uri`. If it grows, the web server's
  configuration is not the one from the examples.
