# Link previews (Open Graph)

When someone shares a link to your website in a messenger or on a social
network, that service fetches the page and shows a small preview: title,
description, picture. It takes them from `meta` tags in the page's head
(Open Graph: `og:title`, `og:description`, `og:image`; also `twitter:…` and
the plain `description`).

A page behind the security check answers such a service with the check, so
the preview reads "A quick security check" and has no picture. With previews
switched on, the challenge page carries the tags of the page that was asked
for. The service never passes the check and never gets the page; it gets
what it needs for the preview.

Off by default.

## Switching it on

```yaml
previews:
  enabled: true
```

A challenge page for `/rathaus/` then starts like this (from a real run; the
three lines after the title come from the website's own page):

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>A quick security check</title>
<meta name="description" content="Öffnungszeiten, Anschrift und Ansprechpersonen des Rathauses.">
<meta property="og:title" content="Rathaus">
<meta property="og:image" content="https://www.musterhausen.example/bilder/rathaus.jpg">
```

## How it works

- The first time a challenge page is shown for an address, Xibalba notes
  the address and answers at once, without tags. A request never waits.
- In the background Xibalba asks your website for that page, one page at a
  time and at most `previews.fetch_per_minute` pages a minute, reads the
  first 256 KiB, and keeps the tags it finds in the head.
- Later challenge pages for the address carry the tags. After
  `previews.ttl` they are fetched again; the old ones stay in use meanwhile.
- Nothing is written to disk. After a restart the tags are fetched again.

Which tags are taken: those whose name starts with `og:`, `twitter:` or
`article:`, and `description`. If the page has no `og:title`, its `<title>`
is used. At most 40 tags per page, each value at most 1000 bytes, as plain
text. Nothing else of the page is passed on.

The fetch goes straight to `upstream.url`, with the method GET, the user
agent `Xibalba/<version> (link preview; …)`, and without cookies or any
other header of the visitor. Redirects are not followed. Only an answer
with status 200 and the type `text/html` is read.

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `previews.enabled` | `false` | Put the tags on the challenge page. |
| `previews.ttl` | `24h` | How long the tags of a page are kept before they are fetched again. `1m` to `720h`. |
| `previews.max_pages` | `1000` | How many pages are remembered at most. 1 to 100000. When the table is full, pages make way for new ones. |
| `previews.fetch_per_minute` | `30` | How many pages are fetched from your website per minute at most. 1 to 600. |
| `previews.query` | `false` | `false`: the part of an address after `?` is left out, so a page is fetched once however a link was decorated (`?utm_source=…`). `true`: every query is a page of its own; needed if your pages are told apart by the query (`/artikel?id=7`). |
| `previews.tags` | `{}` | Tags used for every page. Nothing is fetched then. |

### The same preview for every page

If one preview for the whole website is enough, give the tags yourself.
Xibalba then makes no request to the website for previews at all:

```yaml
previews:
  enabled: true
  tags:
    og:title: "Stadt Musterhausen"
    og:description: "Rathaus und Bürgerservice"
    og:image: "https://www.musterhausen.example/bilder/wappen.png"
```

A name has to be `description` or start with `og:`, `twitter:` or
`article:`. A value is plain text on one line.

## Three things to check

**The status code.** Xibalba sends the challenge page with status 403. Some
services build a preview from any answer, others only from an answer with
status 200. If previews stay empty although the tags are on the page, set

```yaml
pages:
  status:
    challenge: 200
```

The page stays marked as not to be stored and not to be indexed
(`Cache-Control: no-store`, `X-Robots-Tag: noindex`, `noindex` in the page).
See [CONFIGURATION.md](CONFIGURATION.md#pages).

**The picture.** `og:image` is only an address. The service fetches the
picture itself, and if the picture lies behind the security check it gets
the check instead. Let the pictures through with a rule:

```yaml
rules:
  list:
    - name: preview-pictures
      match:
        path: {prefix: "/bilder/"}
      action: allow
```

**What your pages say about themselves.** The tags are shown to everyone
who gets the challenge page, before any check. That is their purpose, and
on a public website they say nothing the page would not. Mind two cases:

- Pages that are not public but answer Xibalba without a login, for example
  because your website trusts requests from the machine Xibalba runs on.
  Their titles would be shown. Xibalba sends no login, so pages that need
  one give nothing away.
- Rules with the action `deny`: the block page never carries tags.

## What can go wrong

| What happens | What Xibalba does | Where you see it |
|---|---|---|
| The website does not answer a fetch | No tags for that page; asked again after five minutes at the earliest. Visitors notice nothing. | `previews` is `degraded` in `/healthz` until a fetch succeeds |
| A page has no tags, is not HTML, redirects, or is not found | Remembered as "no tags" for `previews.ttl`. | Nothing; that is normal |
| Many different addresses are asked for (a crawler) | At most `fetch_per_minute` fetches a minute reach the website, 64 wait in line, the rest is dropped and tried again when asked for later. | `xibalba_preview_fetches_total{result="dropped"}` in `/metrics` |
| More pages than `max_pages` | Pages make way for new ones and are fetched again when next asked for. | Nothing |

## Privacy

A fetch carries the address of the page and, with `upstream.preserve_host`,
the host name. It carries nothing about the visitor whose request caused
it: no address, no cookie, no user agent.
