# Rules

Rules decide what happens to each request: it is let through to the website,
refused, or challenged. This document explains
how rules are written and how they are evaluated.

Rules live in the `rules` section of the configuration file and in rule files
that it imports. The settings around them (`dry_run`, `default_action`,
`on_error`, `thresholds`, `files`) are listed in
[CONFIGURATION.md](CONFIGURATION.md#rules).

## A first rule

```yaml
rules:
  list:
    - name: block-example-bot
      match:
        user_agent: {contains: "ExampleBot"}
      action: deny
```

Every rule has three parts:

| Part | Meaning |
|---|---|
| `name` | Identifies the rule in the counters and on the block page. Lower-case letters, digits, `.`, `_` and `-`; at most 64 characters; unique across all rules. |
| `match` | The conditions. The rule matches when all of them hold. |
| `action` | What happens when the rule matches: `allow`, `deny`, `challenge` or `weigh`. |

## How a request is evaluated

1. Rules are checked from top to bottom.
2. When a rule matches and its action is `allow`, `deny` or `challenge`, that
   is the decision and evaluation stops. Order therefore matters: put your
   exceptions above the rules they are exceptions to.
3. When a rule matches and its action is `weigh`, its `weight` is added to the
   request's score and evaluation continues.
4. If no rule decided, the score is compared with the `thresholds`. The
   highest threshold the score reaches decides.
5. If no threshold is reached, `default_action` applies.

Rules in the configuration file (`rules.list`) are evaluated first, then the
rules of each imported file in the order of `rules.files`. Your own rules
therefore take precedence over imported ones.

## Actions

| Action | Effect |
|---|---|
| `allow` | The request goes to the website. |
| `deny` | The visitor gets the "request blocked" page with status `403`. The website is not contacted. |
| `challenge` | The client has to pass the security check first, unless it already holds a valid pass. See [CHALLENGE.md](CHALLENGE.md). |
| `weigh` | Adds `weight` to the score. Requires `weight`, a number from -1000 to 1000 other than 0. |

The block page shows a short reference such as `8fbf25e1`. It identifies the
rule, not the visitor: the same rule always has the same reference. A visitor
who thinks they were blocked by mistake can pass it on, and you can look it up
in `/decisions` (see [CONFIGURATION.md](CONFIGURATION.md#ops)).

## Conditions

All conditions written in one `match` must hold.

| Condition | Tests | Value |
|---|---|---|
| `user_agent` | The `User-Agent` header | A text test |
| `path` | The path of the request, normalised (see below) | A text test |
| `host` | The host name the client asked for, without port | A text test |
| `method` | The HTTP method | A list such as `["POST", "PUT"]` |
| `header` | Any other header, by name | A text test or `present` per header |
| `ip` | The client's address | A list of addresses and networks |
| `crawler` | Which known crawler the request claims to be, and whether that is true | `class`, `name`, `verified`; see [CRAWLERS.md](CRAWLERS.md) |
| `all` | Groups of conditions that must all hold | A list of `match` blocks |
| `any` | Groups of conditions of which one must hold | A list of `match` blocks |
| `not` | Conditions that must not hold | A `match` block |

### Text tests

A text test has exactly one of these:

| Test | Holds when the text |
|---|---|
| `equals: "x"` | is exactly `x` |
| `contains: "x"` | contains `x` |
| `prefix: "x"` | starts with `x` |
| `suffix: "x"` | ends with `x` |
| `regex: "x"` | matches the regular expression `x` anywhere |

Tests **ignore upper and lower case** unless you add `case_sensitive: true`.
The default is this way round because a rule that says `GPTBot` and silently
misses `gptbot` is the worse mistake. Host names are never case sensitive.

Regular expressions use RE2 syntax. It has no lookahead and no
backreferences; in return an expression can never take long to evaluate, no
matter what a client sends. Anchor with `^` and `$` when you mean the whole text.

To combine several tests on the same text, use `all` or `any`.

### Headers

```yaml
match:
  header:
    Accept-Language: {present: false}
    Accept: {contains: "text/html"}
```

`present: true` holds when the header is there, `present: false` when it is
not. A header that occurs several times matches if any of its values does.

`User-Agent` and `Host` have their own conditions. Rules on `X-Forwarded-For`,
`X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Real-IP` and `Forwarded` are
rejected: these are written by the client, and a rule on them would look like
a rule on an address while testing text anyone can send. Use `ip`.

### Addresses

```yaml
match:
  ip: ["192.0.2.7", "198.51.100.0/24", "2001:db8::/32"]
```

`ip` tests the client address that Xibalba established itself (see
`server.trusted_proxies` in [CONFIGURATION.md](CONFIGURATION.md#server)).

### Paths

`path` is tested against a normalised form of the path, so that a rule cannot
be dodged by spelling the path differently. Before testing:

- percent-encoding is decoded once (`/%61dmin` is `/admin`);
- backslashes count as slashes;
- path parameters are dropped (`/admin;x=1` is `/admin`);
- repeated slashes collapse and `.` and `..` segments are resolved
  (`//admin`, `/./admin` and `/x/../admin` are all `/admin`).

Normalisation only widens what a rule matches. The website still receives the
path exactly as the client sent it.

`prefix: "/admin"` also matches `/administrator`. If you mean the directory,
write `regex: "^/admin(/|$)"`.

## What can be trusted

**Only `ip` and a verified `crawler` are established by Xibalba.** The user
agent and every header are whatever the client chooses to send.

- A `deny` rule on a user agent stops crawlers that announce themselves
  honestly. It does not stop one that lies.
- An `allow` rule on a user agent alone lets in anyone who sends that name.
  Combine it with `ip`, or write it the other way round:

```yaml
# "GoodBot" is welcome, but only from its operator's network.
- name: block-fake-goodbot
  match:
    user_agent: {contains: "GoodBot"}
    not:
      ip: ["198.51.100.0/24"]
  action: deny
```

For the crawlers of the large operators you do not need to maintain such
networks yourself: the `crawler` condition with `verified: true` checks the
operator's published addresses. Xibalba refuses a rule that would let a
crawler through, or spare it, on its name alone. See [CRAWLERS.md](CRAWLERS.md).

## Weights and thresholds

Some signals are too weak to decide alone but add up. Give each a weight and
let the thresholds decide:

```yaml
rules:
  thresholds:
    - {weight: 10, action: challenge}
    - {weight: 20, action: deny}
  list:
    - name: weigh-no-language
      match:
        header:
          Accept-Language: {present: false}
      action: weigh
      weight: 10
    - name: weigh-scripted-client
      match:
        user_agent: {prefix: "curl/"}
      action: weigh
      weight: 10
    - name: weigh-looks-like-a-browser
      match:
        user_agent: {contains: "Mozilla/"}
      action: weigh
      weight: -5
```

A request without `Accept-Language` scores 10 and is challenged. If it also
comes from curl it scores 20 and is denied. Weights may be negative.
A threshold's action is `challenge` or `deny`; its weight is 1 to 1000.

## Rule files

A rule file has one top-level key, `rules`, holding a list of rules:

```yaml
rules:
  - name: block-private
    match:
      path: {prefix: "/private"}
    action: deny
```

Import it from the configuration file. Paths are relative to the directory of
the configuration file:

```yaml
rules:
  files:
    - rules/local.yaml
```

Rule names must be unique across the configuration file and all rule files.
[`examples/rules/basic.yaml`](../examples/rules/basic.yaml) is a commented
example; a test keeps it valid.

## Trying a rule set safely

Set `rules.dry_run: true`. Xibalba then evaluates and counts every decision
but lets every request through. Watch `/decisions` for a while, and switch
dry run off when the numbers are what you expect:

```sh
curl http://127.0.0.1:9090/decisions
```

```json
{
  "dry_run": false,
  "since": "2026-10-03T08:25:31Z",
  "totals": {
    "allow": 1,
    "challenge": 1,
    "deny": 2
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
      "source": "rule:block-example-bot",
      "action": "deny",
      "reference": "8fbf25e1",
      "count": 1
    },
    {
      "source": "rule:block-admin",
      "action": "deny",
      "reference": "1c30105a",
      "count": 1
    },
    {
      "source": "threshold:10",
      "action": "challenge",
      "reference": "97e55a91",
      "count": 1
    },
    {
      "source": "threshold:20",
      "action": "deny",
      "reference": "f26a9aa0",
      "count": 0
    },
    {
      "source": "default",
      "action": "allow",
      "reference": "37a8eec1",
      "count": 1
    }
  ]
}
```

The counters say which rule decided how often. They hold nothing about who
was decided upon: no address, no path, no user agent. They start at zero each
time Xibalba starts; lasting statistics are **planned** (milestone M5).

## Mistakes are found at start-up

Xibalba checks the whole rule set before it starts and lists every mistake
with its line. It never starts with a rule it does not understand.

```text
configuration xibalba.yaml: 4 problems
  - line 4, rules.default_action: "block" is not an action a default can take
    fix: use one of: allow, deny, challenge
  - line 6, rules.list[0].name: "Block Bots" is not a valid rule name
    fix: use lower-case letters, digits, dot, underscore and hyphen, starting with a letter or digit, at most 64 characters
  - line 8, rules.list[0].match.user_agent.regex: the regular expression is not valid: missing closing ): `(GPT|Claude`
    fix: Xibalba uses RE2 syntax, which has no lookahead or backreferences; for plain text use contains
  - line 13, rules.list[1].match.header.X-Forwarded-For: X-Forwarded-For is written by the client and cannot be trusted as an address
    fix: use the ip condition, which tests the address Xibalba established itself
```

Check a file without starting anything:

```sh
xibalba -check -config xibalba.yaml
```

## Limits

| Limit | Value |
|---|---|
| Rules in total | 10,000 |
| Nesting of `all`, `any`, `not` | 8 levels |
| Entries in one `ip`, `method`, `header`, `all` or `any` list | 1,024 |
| Length of a text or regular expression | 512 characters |
| Weight of a rule or threshold | up to 1,000 |
| Rule files | 64, each up to 1 MiB |

## Cost

Evaluating a request does not allocate memory and performs no I/O. On the
development machine a request that matches none of 123 rules, and is therefore
tested against all of them, takes about 6 microseconds. Measure on your own
hardware with `make bench`.

## Known limits

- A website that decodes percent-encoding twice can be reached with a doubly
  encoded path (`%252e%252e`) that Xibalba, decoding once, does not recognise.
  Such a website has a flaw of its own; rules cannot fully cover for it.
- `ip` lists are checked one entry after the other. Very long address lists
  will get a faster structure with the managed block lists (milestone M7).
