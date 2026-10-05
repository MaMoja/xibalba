# Guides for applications

How to put Xibalba in front of particular kinds of website. For the web
server in front, see [ENVIRONMENTS.md](ENVIRONMENTS.md).

- [Pages that load parts of themselves (htmx, fetch)](#pages-that-load-parts-of-themselves)
- [WordPress](#wordpress)
- [Starting from a robots.txt](#starting-from-a-robotstxt)

## Pages that load parts of themselves

Many pages fetch more from the website after they have loaded: a list that
is filtered, a form that is checked, the next entries. If such a request is
answered with the security check, the script that asked gets a page where
it expected data or a piece of a page.

When does that happen? Not while the visitor has a pass: the requests of a
page carry the same cookie as the page. It happens when the pass runs out
or is not accepted while the page is open (`challenge.pass_lifetime`, a
week by default; a change of network with `challenge.bind_network`), or
when the parts fall under a rule that asks for more than the page did.

### htmx

Nothing to set up. A request made by htmx says so (the header
`HX-Request`), and Xibalba answers such a request with the header
`HX-Refresh: true` on the challenge page. htmx then loads the whole page
anew instead of putting the check into the middle of it; the check appears
as a page, and after it the visitor is back where they were.

For this to work, **the page and its parts have to fall under the same
check**: if only the parts are challenged and the page itself is let
through, reloading the page shows no check, and the parts stay refused.

Tried in a real browser with htmx 2.0.11: with the pass removed while the
page was open, a click on an htmx button led to the check and back to the
page, and the next click loaded the part.

### Other scripts (fetch, XMLHttpRequest)

Xibalba cannot tell a script what to do. What helps:

- **Keep page and data under the same rules**, as above, so that a visitor
  who has the page also has what the page needs.
- **Do not put data that programs fetch behind the check.** An API used by
  an app or by another server cannot pass it. Let it through by path, and
  protect it with request limits instead:

```yaml
rules:
  list:
    - name: api
      match:
        path: {prefix: "/api/"}
      action: allow
limits:
  enabled: true
  windows:
    - {requests: 300, per: 1m, action: deny}
```

- **In your own script, treat an answer with status 403 and the type
  `text/html` as "load the page again"** (`location.reload()`); the check
  then appears as a page.

## WordPress

This section is written from how WordPress is known to work; **it has not
been tried with a real WordPress installation.**

**What needs no check, and why.**

| Path | What it is | Suggestion |
|---|---|---|
| `/wp-cron.php` | WordPress calls it itself, from the server, to run scheduled jobs | Allow from the server's own address |
| `/wp-json/` | The REST API: used by the editor and by pages in the browser (which have the pass), and by apps and other services (which cannot pass a check) | If outside services use it: allow, with a request limit. Otherwise leave it under the check |
| `/wp-admin/admin-ajax.php` | Requests of pages in the browser | Leave it under the same check as the pages |
| `/feed/`, `/comments/feed/` | Feeds, read by programs | The preset `allow-feeds`, or `keep-internet-working` |
| `/xmlrpc.php` | An old interface for remote publishing, often attacked | `deny`, unless you use it |
| `/wp-login.php` | The login | A harder check and a request limit |

```yaml
rules:
  presets: [keep-internet-working, block-ai-training, challenge-browsers]
  list:
    - name: wordpress-own-jobs
      match:
        path: {equals: "/wp-cron.php"}
        ip: ["127.0.0.1", "::1"]
      action: allow
    - name: no-xmlrpc
      match:
        path: {equals: "/xmlrpc.php"}
      action: deny
    - name: login
      match:
        path: {equals: "/wp-login.php"}
      action: challenge
      challenge: {method: pow, difficulty: 20, no_javascript: deny}
```

- **The visitor's address.** WordPress sees Xibalba's address as the
  sender. Xibalba passes the real one on in `X-Forwarded-For` and
  `X-Real-IP`; WordPress and security plugins have to be told to read it
  from there, and to believe it only from Xibalba's address.
- **Page caches.** Xibalba's own pages say they must not be stored
  (`Cache-Control: no-store`). A cache that stands in front of Xibalba must
  honour that, or it hands the check to everyone; a cache behind Xibalba
  (a caching plugin) never sees them.
- **If WordPress calls itself by its public name** (for `wp-cron.php` or
  health checks), the call comes through your web server and Xibalba like
  any visitor's. Allow it by the server's public address as well.

## Starting from a robots.txt

A `robots.txt` asks crawlers to stay out of some paths. Xibalba can turn
the file into rules that enforce it:

```
$ xibalba -robots robots.txt > robots-rules.yaml
note: rules on a crawler's name stop crawlers that say who they are; one that lies about its name is not stopped by them
```

For this `robots.txt`:

```
User-agent: *
Disallow: /admin/
Disallow: /private
Allow: /private/press/
Disallow: /*.pdf$

User-agent: BadBot
Disallow: /
```

the rule file is (from a real run):

```yaml
# Made from a robots.txt by "xibalba -robots". Read it before you use it:
# a robots.txt asks, these rules enforce. Import it with rules.files.
rules:
  - name: robots-1
    match:
      path: {prefix: '/admin/'}
    action: challenge
  - name: robots-2
    match:
      path: {prefix: '/private'}
      not:
        path: {prefix: '/private/press/'}
    action: challenge
  - name: robots-3
    match:
      path: {regex: '^/.*\.pdf$'}
      not:
        path: {prefix: '/private/press/'}
    action: challenge
  - name: robots-4
    match:
      user_agent: {contains: 'BadBot'}
    action: deny
```

Use it with `rules: {files: [robots-rules.yaml]}` and check with
`xibalba -check`.

| Option | Default | Meaning |
|---|---|---|
| `-robots FILE` | | The `robots.txt` to read; `-` reads standard input. The file is read from disk: fetch it yourself first (`curl -o robots.txt https://www.example.org/robots.txt`). |
| `-robots-action` | `challenge` | What happens to requests for a disallowed path: `challenge` or `deny`. |
| `-robots-crawlers` | `deny` | What happens to a crawler the file shuts out of the whole site (`Disallow: /`): `deny` or `challenge`. |

What to know:

- **The rules apply to everyone, not only to crawlers.** `Disallow: /admin/`
  for `*` becomes a check for every visitor of `/admin/`. That is usually
  what you want from Xibalba; if not, remove the rule.
- `Allow` lines inside a disallowed path become exceptions (`not`). `*` and
  `$` in a path become a regular expression. `Crawl-delay` is left out;
  request limits do that job. `Sitemap` is ignored.
- A crawler named in the file is matched by its name in the user agent,
  which a crawler can change. For the crawlers Xibalba knows and can
  verify, the presets are the better tool ([CRAWLERS.md](CRAWLERS.md)).
- At most 500 rules are written, and the notes on standard error say what
  was left out.
