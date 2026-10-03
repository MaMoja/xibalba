# Crawlers

Xibalba knows the crawlers of the large AI and search operators: what each one
is for, and how to tell a genuine one from a program that only borrows its
name. This page explains the classes, the ready-made presets, how identity is
verified, and how to add crawlers of your own.

The German handbook covers the same ground for operators:
[HANDBUCH.md, "Crawler erkennen und prüfen"](de/HANDBUCH.md#crawler-erkennen-und-prüfen).

## The problem with names

A crawler says who it is in its user agent. Anyone can send any user agent. A
rule that lets "Googlebot" through by name lets through everyone who types
that name. Xibalba therefore answers two separate questions about a request:

1. **Which crawler does it claim to be?** Decided by the user agent.
2. **Is that true?** Decided by where the request comes from: the operator's
   published addresses, or a reverse DNS check. Never by the name.

A request ends up in one of these states:

| State | Meaning | `verified: true` matches | `verified: false` matches |
|---|---|---|---|
| verified | It carries the name and comes from the operator. | yes | no |
| impostor | It carries the name and was checked: it does not come from the operator. | no | yes |
| unknown | It carries the name, but the claim cannot be checked (the operator publishes no way) or is not checked yet (the list has not arrived, the DNS lookup is still running). | no | no |
| none | It does not carry the name of a known crawler. | no | no |

"Unknown" is deliberately neither one nor the other. Such a request is not let
through as a crawler and not punished as an impostor; it gets whatever your
other rules and the default say.

## Classes

| Class | What the crawler is for |
|---|---|
| `training` | Collects pages for training AI models. |
| `ai-search` | Builds an index for AI answers that link back to their sources. |
| `user-fetch` | Loads a page because a person just asked an assistant about it. |
| `search-engine` | Builds the index of a classic search engine. |
| `archive` | Builds a public copy of the web that anyone can download, including for AI training. |
| `other` | Everything else: link previews, advertising checks, site tools. |

The class follows the operator's own description of the crawler. Operators
with several purposes run several crawlers, and each one is listed separately.

## Presets

A preset is a small rule file that ships inside Xibalba. Switch presets on in
the configuration:

```yaml
rules:
  default_action: allow
  presets:
    - block-fake-crawlers
    - block-ai-training
    - allow-search-engines
    - allow-ai-search
    - allow-ai-user-fetch
```

| Preset | What it does |
|---|---|
| `block-fake-crawlers` | Denies requests that carry a known crawler's name but were checked and do not come from that crawler's operator. |
| `block-ai-training` | Denies crawlers of class `training`. The name alone is enough: nobody is harmed if an impostor is turned away too. |
| `block-archive-crawlers` | Denies crawlers of class `archive`. |
| `allow-search-engines` | Lets verified crawlers of class `search-engine` through. |
| `allow-ai-search` | Lets verified crawlers of class `ai-search` through. |
| `allow-ai-user-fetch` | Lets verified fetchers of class `user-fetch` through. |

Nothing is switched on by default.

Order: the rules in `rules.list` come first, then the presets in the order you
list them, then the files in `rules.files`. A rule of your own in `rules.list`
therefore overrides a preset. The `allow-` presets matter when later rules or
the default challenge or deny: they let the wanted crawlers pass before that.
A crawler cannot solve the security check.

The rules of a preset are named `preset.<name>` and are counted in
`/decisions` like every other rule. The preset files are in
[`data/presets`](../data/presets); each is a normal rule file.

## Crawler conditions in your own rules

```yaml
rules:
  list:
    # Only OpenAI's search crawler, and only the genuine one.
    - name: allow-openai-search
      match:
        crawler: {name: [OAI-SearchBot], verified: true}
      action: allow

    # Keep AI search crawlers out of one area.
    - name: no-ai-in-archive
      match:
        path: {prefix: "/archiv"}
        crawler: {class: [ai-search, user-fetch]}
      action: deny
```

| Key | Meaning |
|---|---|
| `class` | List of classes. The crawler must be of one of them. |
| `name` | List of crawler names from the table below (or your own files). Upper and lower case do not matter. |
| `verified` | `true`: only the genuine crawler. `false`: only impostors. Left out: the name alone is enough. |

At least one key must be given. Xibalba refuses a rule that would treat a
request better because of a name alone. That covers `allow`, a negative
`weigh`, and also the roundabout form "deny everyone except ...":

```text
configuration bad.yaml: 2 problems
  - line 7, rules.list[0].match.crawler.verified: a rule that lets a crawler through must make sure it is genuine, because anyone can send a crawler's name
    fix: add verified: true to the crawler condition
  - line 11, rules.list[1].match.crawler.name[0]: "SuperBot" is not a known crawler name
    fix: the known crawlers are listed in docs/CRAWLERS.md and at /crawlers on the operations listener; for any other program use a user_agent condition
```

A `user_agent` condition is not covered by this protection; see
[RULES.md](RULES.md#what-can-be-trusted).

## The crawlers Xibalba knows

Every entry was taken from the operator's own documentation; the definition
file names the page and the day it was checked. `GET /crawlers` on the
operations listener shows the list of the running build with these details.

| Operator | Crawler | Class | Verified by |
|---|---|---|---|
| Amazon | Amazonbot | training | not possible |
| Amazon | Amzn-SearchBot | ai-search | not possible |
| Amazon | Amzn-User | user-fetch | not possible |
| Anthropic | ClaudeBot | training | address list |
| Anthropic | Claude-SearchBot | ai-search | address list |
| Anthropic | Claude-User | user-fetch | address list |
| Apple | Applebot | search-engine | reverse DNS |
| Common Crawl | CCBot | archive | address list |
| DuckDuckGo | DuckAssistBot | user-fetch | address list |
| Google | Googlebot | search-engine | address list |
| Google | GoogleOther | other | address list |
| Google | Google-CloudVertexBot | other | address list |
| Meta | meta-externalagent | training | not possible |
| Meta | meta-webindexer | ai-search | not possible |
| Meta | meta-externalfetcher | user-fetch | not possible |
| Meta | facebookexternalhit | other | not possible |
| Microsoft | bingbot | search-engine | reverse DNS |
| OpenAI | GPTBot | training | address list |
| OpenAI | OAI-SearchBot | ai-search | address list |
| OpenAI | ChatGPT-User | user-fetch | address list |
| OpenAI | OAI-AdsBot | other | address list |
| Perplexity | PerplexityBot | ai-search | address list |
| Perplexity | Perplexity-User | user-fetch | address list |

Things to know:

- **"Not possible"** means the page the definition was taken from names no
  way to verify the crawler that Xibalba can use. Such a crawler can be denied
  by name, but an `allow-` preset never lets it through.
- **Google-Extended is not in the list.** It is a robots.txt token that
  controls whether Google may use pages for Gemini; it has no user agent of
  its own. In a request it cannot be told apart from Googlebot, so no proxy
  can block it. Set it in your robots.txt.
- **Applebot-Extended** is likewise a robots.txt token, not a crawler.
- **One address list can cover several crawlers of an operator.** Xibalba
  then verifies "this request comes from the operator's crawler addresses",
  not "from this one crawler".
- **The list is not complete and goes out of date.** Crawlers that are not
  listed are handled with ordinary `user_agent` rules.

**Not yet tested against the live lists.** The development environment cannot
reach the operators' servers, so the downloads were tested against a local
server only. After the first start, look at `/crawlers`: every crawler that is
verified by an address list should show `"addresses"` greater than 0 and no
`"list_error"`.

## How verification works

**Address lists.** Operators publish the addresses of their crawlers as a
file. Xibalba downloads these files in the background: right after the start,
then every `crawlers.refresh_interval`. A failed download is tried again after
1, 5 and 30 minutes.

- Only `https` is accepted, also after a redirect, so the list cannot be
  replaced on the way.
- A list is refused as a whole if it is empty, is not a list of addresses,
  has more than 100 000 entries or 2 MiB, contains a network larger than /8
  (IPv4) or /24 (IPv6), or contains private addresses. The previous good list
  stays in use.
- A list that could not be renewed for more than a week (or three intervals,
  if that is longer) is no longer used. Its crawlers become "unknown".
- With `crawlers.cache_dir` set, the lists survive a restart.

**Reverse DNS.** For crawlers verified this way, the client's address must
resolve to a name under the operator's domain, and that name must resolve back
to the same address. The second step is what counts: anyone can give their own
address any name, but only the owner of the domain can make the name point
back. The lookups run in the background. The first request from a new address
is "unknown"; the answer is remembered (24 hours when confirmed, one hour when
refused).

**Nothing waits for the network.** Identifying a request reads from memory
only. On the development machine it takes less than a microsecond.

**No outgoing connection unless you ask for it.** The downloads and DNS
lookups only run if a rule or preset has a `crawler` condition.

### What leaves your server

| What | To whom | Contains |
|---|---|---|
| Download of the address lists | The crawler operators (OpenAI, Anthropic, Perplexity, Google, DuckDuckGo, Common Crawl) | Your server's address and the user agent `Xibalba/<version>`. Nothing about your visitors. |
| Reverse DNS lookup | The DNS resolver your server uses | The address of a client that claimed to be a crawler verified by reverse DNS (Bingbot, Applebot). Not the addresses of ordinary visitors. |

Client addresses are kept in memory for the DNS cache only (at most 24 hours,
at most 40 000 entries) and are never written to disk or to the log.
`crawlers.refresh: false` stops the downloads; crawlers verified by an address
list are then never counted as genuine.

## Looking at what happens

`GET /crawlers` on the operations listener:

```json
{
  "name": "GPTBot",
  "operator": "OpenAI",
  "class": "training",
  "purpose": "Collects pages that may be used to train OpenAI's models.",
  "source": "https://developers.openai.com/api/docs/bots",
  "checked": "2026-10-03",
  "verified_by": "addresses",
  "addresses": 0,
  "list_error": "Forbidden",
  "requests": {
    "verified": 0,
    "unverified": 0,
    "unverifiable": 0,
    "pending": 0
  }
}
```

(This is from the development environment, where the download is blocked:
hence `"addresses": 0` and the `list_error`.) `requests` counts, since the
start, how many requests claimed to be this crawler and what was found out.
`unverified` are impostors. The counters only run while a `crawler` condition
is in use.

The component `crawlers` in `/healthz` is `degraded` when a list is missing,
out of date or too old, and says which.

## Your own crawlers

Add definition files with `crawlers.files`. This is also the way to correct a
built-in definition without waiting for a release: a crawler in your file
replaces the built-in crawler of the same name.

```yaml
operator: City of Musterhausen
source: https://www.musterhausen.example/monitoring
checked: 2026-10-03
crawlers:
  - name: CityMonitor
    class: other
    user_agent: CityMonitor
    purpose: Checks that the website is reachable.
    verify:
      ranges: ["192.0.2.10"]
```

| Key | Meaning |
|---|---|
| `operator` | Who runs the crawlers in this file. |
| `source` | The page this information comes from. |
| `checked` | The day the source was last checked, `YYYY-MM-DD`. |
| `crawlers[].name` | Name used in rules and reports. Letters, digits, `.`, `_`, `-`. |
| `crawlers[].class` | One of the classes above. |
| `crawlers[].user_agent` | Text that appears in the crawler's user agent, 3 to 100 characters. Case does not matter. Only the first 512 characters of a user agent are searched. If several crawlers match, the longest text wins. |
| `crawlers[].purpose` | One sentence on what the crawler is for. |
| `crawlers[].note` | Optional. Anything else a site owner should know. |
| `crawlers[].verify.ranges` | Addresses and networks of the crawler, written out. |
| `crawlers[].verify.ranges_url` | `https` address of a published list: JSON of any layout, or plain text with one address or network per line. |
| `crawlers[].verify.reverse_dns` | Domain endings with a leading dot, such as `.search.msn.com`. |
| `crawlers[].verify.source` | Optional. The page that describes the verification. |

Several `verify` keys may be combined; passing one is enough. Without any,
the crawler can never be verified.

Whoever can write to these files, or to `crawlers.cache_dir`, decides which
addresses count as a genuine crawler. Keep both writable for Xibalba's user only.

## Known limits

- A crawler that sends no telling user agent, or a browser's, is not
  recognised as a crawler at all. That is what the security check is for.
- The first request from a new address claiming a reverse-DNS-verified crawler
  is "unknown" until the lookup is done, so it is neither let through by an
  `allow-` preset nor denied by `block-fake-crawlers`.
- If one user agent names two crawlers, only one is considered.
- Under a flood of requests that all claim a reverse-DNS-verified crawler from
  many networks, lookups queue up (16 at a time, 3 seconds each at most) and a
  genuine crawler can stay "unknown" longer.
- Counters per crawler are not kept across restarts (statistics: milestone M5).
