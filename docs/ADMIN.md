# Web interface

Xibalba can show what it counted in a browser: how many requests were let
through, checked and blocked, which rules decided, which crawlers came, and
whether every part works.

The web interface is **optional and off by default**. Switched off, nothing
of it exists: no listener, no memory, no work. Switched on, it costs nothing
while nobody looks at it. By default it only shows; with a second switch it
can change presets and a list of addresses
([Changing settings in the browser](#changing-settings-in-the-browser)).

## Do I want it?

| | Without | With |
|---|---|---|
| See the numbers | `curl` on the operations listener (`/decisions`, `/statistics`), or your monitoring through `/metrics` | In a browser, with a chart and tables |
| Open ports | public and operations listener | one more, on this machine only by default |
| Things to look after | none | one password |

If you watch Xibalba through a monitoring system anyway, you do not need it.

## Switching it on

**1.** Set a password. It is asked for twice and not shown while you type:

```sh
xibalba -set-password -config /etc/xibalba/xibalba.yaml
```

```text
The password is set (admin.password). Restart Xibalba for it to take effect.
```

The password needs at least 12 characters. The file holds only a
scrambled form from which the password cannot be read back; it is
readable by Xibalba's user only. Run the command as that user, or hand
the file over afterwards (`chown xibalba /etc/xibalba/admin.password`).
In a script, and on systems other than Linux (where the typing cannot
be hidden), pipe the password in:
`printf '%s\n' "$PASSWORD" | xibalba -set-password -config …`.

**2.** Switch it on in the configuration file and restart:

```yaml
admin:
  enabled: true
```

**3.** Open <http://127.0.0.1:9091/> on the machine Xibalba runs on.

If you switch it on without a password, Xibalba says so and does not start:

```text
configuration xibalba.yaml: 1 problem
  - line 3, admin.password_file: the web interface is switched on and "admin.password" does not exist
    fix: set a password with: xibalba -set-password -config <this file>
```

## Settings

| Setting | Default | Meaning |
|---|---|---|
| `admin.enabled` | `false` | `true` switches the web interface on. |
| `admin.listen` | `127.0.0.1:9091` | Where it listens. The default is reachable from this machine only. |
| `admin.password_file` | `admin.password` | The file `-set-password` writes, relative to the configuration file. |
| `admin.session_lifetime` | `12h` | How long a login lasts (`5m` to `720h`). |
| `admin.hostnames` | none | Names under which you open the web interface when a web server stands in front. `localhost` and `127.0.0.1` always work; any other name is refused unless listed. |
| `admin.allow_changes` | `false` | `true` lets the web interface switch presets and list addresses. |
| `admin.changes_file` | `admin.changes.json` | Where those changes are kept, relative to the configuration file. |
| `admin.secure_cookie` | `false` | `true` when you reach it over HTTPS: the login cookie is then only sent encrypted and only to this exact host. |

To change the password, run `-set-password` again and restart. A restart
also signs everyone out.

## Reaching it from another machine

The connection of the web interface is not encrypted. Do not open it to a
network as it is. Two safe ways:

- **An SSH tunnel**, with nothing to set up on the server:

  ```sh
  ssh -L 9091:127.0.0.1:9091 you@your-server
  ```

  Then open <http://127.0.0.1:9091/> on your own machine.
- **Your web server with HTTPS in front**, passing a host name of its own
  to `127.0.0.1:9091`, ideally restricted to your own network. Tell Xibalba
  the name and that the connection is encrypted:

```yaml
admin:
  enabled: true
  hostnames: ["xibalba.example.org"]
  secure_cookie: true
```

Behind a web server every sign-in attempt comes from the same address, the
web server's. Wrong passwords from anyone then make everyone wait, you
included. Restrict who can reach the login page in the web server.

If `admin.listen` is not a local address, Xibalba warns at start.

## What it shows

- Requests let through, sent to the security check and blocked: for the last
  24 hours, 7 days or 30 days, as numbers, as a chart, and as a table.
- What decided: the rules with the most requests.
- Crawlers: genuine, impostors, not checkable.
- The security check, the limits and the trap.
- The state of every part, as in `/healthz`.

The course over time needs the statistics on disk
([`statistics.directory`](STATISTICS.md)). Without them, the page shows the
counts since the last start.

It shows no address, no path and no user agent, because Xibalba does not
keep them.

## How it is protected

- Nothing is shown without a login. There is one password and no user name.
- After five wrong passwords from one address, that address has to wait: 30
  seconds, then longer each time, up to 15 minutes. Attempts are counted
  before the password is looked at, so sending many at once does not help.
- It answers only under `localhost`, this machine's own local addresses and
  the names in `admin.hostnames`. A foreign web page that points its own
  name at the listener gets no answer.
- A login is a random value in a cookie that scripts cannot read and that
  the browser sends to no other site. Sessions live in memory only.
- The password is stored with PBKDF2-SHA256 and 600000 rounds. Checking one
  takes about a tenth of a second on a server and about a second on a
  Raspberry Pi; only one is checked at a time, and at most four attempts
  wait for their turn.
- Xibalba warns at start if the password file can be read by other users.
- The pages contain no script and load nothing from elsewhere.
- Password and sessions never appear in the log. A wrong password is logged
  as an event, without the address or what was typed.

The component is `admin` in `/healthz` and in the log.

## Changing settings in the browser

By default the web interface only shows. With `admin.allow_changes` it can
also change things, and a change takes effect at once, without a restart:

```yaml
admin:
  enabled: true
  allow_changes: true
```

A second page, "Settings", then appears beside the overview.

**Ready-made rule groups (presets).** Every preset is listed with one
sentence on what it does, and a button to switch it on or off. A preset
switched on here is put at its usual place among the others: wanted programs
first, unwanted crawlers next, wanted crawlers after them, the general check
for browsers last. The order you wrote in the configuration file is kept.

**Addresses let through or blocked.** Enter an address (`192.0.2.7`) or a
network (`192.0.2.0/24`), choose whether it is let through or blocked, how
long the entry stays (1 hour to 1 year, or without end; 30 days unless you
choose otherwise), and a note for yourself. Things to know:

- An entry comes before every rule, **also before your own rules in the
  configuration file and before every preset**. A blocked address gets the
  block page; an address that is let through skips rules, security check
  and request limits. Let through only addresses you trust.
- Blocked beats let through: a blocked address inside a network that is let
  through stays blocked.
- An entry ends by itself when its time is over. Within a minute it is out
  of force and gone from the changes file.
- This machine's own address and the web servers in
  `server.trusted_proxies` cannot be let through. If Xibalba sees such an
  address for every visitor, letting it through would let everyone through.
  Set `server.trusted_proxies` correctly before you use the list behind a
  web server: otherwise every visitor has the web server's address.
- At most 500 entries, and no network larger than a `/16` (IPv4) or `/32`
  (IPv6): the list is for single clients and organisations. For more, write
  a rule in the configuration file.
- You cannot lock yourself out of the web interface: it has its own
  listener and is not behind the rules.

**Your own rules.** A text field holds rules in the form of a rule file
(see [Rules](RULES.md)): one key `rules` with a list.

```yaml
rules:
  # Das Intranet nur aus dem Haus
  - name: nur-intern
    match:
      path: {prefix: "/intern"}
      not:
        ip: ["192.0.2.0/24"]
    action: deny
```

- "Check and save" checks the rules together with everything else. If
  something is wrong, nothing is saved and the page lists what to correct,
  with the line.
- Rules written here come after the address list and **before** the rules
  of the configuration file and the presets.
- Names that start with `web-interface.` or `preset.` are kept for
  Xibalba's own rules. At most 32 KiB of text.
- The text is plain YAML: anchors and aliases (`&name`, `*name`, `<<`),
  tags (`!!type`) and a second document (`---`) are refused. Write the
  conditions out.
- The [limits](RULES.md#limits) of every rule set apply, among them the
  size of regular expressions.
- An empty field means no own rules.

**Trying a request.** Below the text field you describe a request: method,
address on your website, the client's IP address, user agent, and further
headers if a rule looks at them. "Try it" shows what the rules in the field
would do with it, saved or not, and which rule decides:

```text
Result: blocked, decided by nur-intern, score 0
```

Nothing is changed, nothing is counted, and nothing is sent anywhere, not
even a name lookup. What the answer leaves out:

- Request limits and a visitor's pass for the security check: they depend
  on what a client did before.
- For a crawler that is verified by name lookup, the box only knows what
  was looked up for real requests before. An address never seen counts as
  "not known yet", so a rule that asks for a verified crawler does not match.
- Addresses Xibalba answers itself (everything under `/.xibalba/`) never
  reach the rules; the box shows what the rules would say all the same.

**Earlier versions.** Every change of presets or own rules keeps what was
in force before, up to ten versions, each with the time and the change that
replaced it. "Go back to this" puts that version in force at once; going
back can itself be undone. The address list is not part of a version: an
address you removed must not live on in a history. An address you write
into a rule (`ip:`) is part of the rule text, and so stays in the earlier
versions until ten later changes have pushed it out.

### Where the changes are kept

In their own small file, `admin.changes.json` beside the configuration file
(`admin.changes_file`). **The configuration file is never rewritten.** The
changes file is plain text:

```json
{
  "presets": {
    "block-ai-training": true
  },
  "addresses": [
    {
      "network": "203.0.113.0/24",
      "action": "deny",
      "note": "Scraper, Meldung vom 4.10.",
      "added": "2026-10-04T16:16:40Z",
      "expires": "2026-11-03T16:16:40Z"
    }
  ]
}
```

- What the file holds is in force **as long as the file exists**, also when
  `allow_changes` or the whole web interface is switched off again.
  Switching `allow_changes` off stops further changes; it does not undo
  earlier ones. To drop them all, delete the file and restart.
- `xibalba -check` checks the configuration together with the changes file.
- Xibalba's user must be able to write to the directory of the file. Under
  the systemd service that is only `/var/lib/xibalba`: set
  `admin.changes_file` and `admin.password_file` to files there, as the
  configuration of the Debian package does.
- A change that would give a rule set that does not work is refused and
  explained on the page; for example switching on `block-trapped` while the
  trap is off. A change that cannot be written to the file is taken back.
- The names `web-interface.allow` and `web-interface.deny` are the two
  rules made from the list; they appear under these names in the counts.
  Your own rules cannot use names that start with `web-interface.`.
- The part is `changes` in `/healthz`. It turns `degraded` if an expired
  entry could not be taken out.
- With `allow_changes`, Xibalba fetches the crawlers' address lists from the
  start, also if no rule asks for a crawler yet, because a preset switched
  on later needs them at once.

The log notes every change, without the address:

```text
level=INFO msg="rule set replaced" component=changes rules=4 presets=block-ai-training,allow-search-engines,challenge-browsers addresses_allowed=0 addresses_blocked=1
level=INFO msg="a setting was changed in the web interface" component=admin what=address
```

### How changes are protected

Besides the login: a change is only accepted from the interface's own form
(the browser must say so, and the form carries a value tied to your login),
never from a link and never from another website.

### Privacy

The addresses you list are stored in the changes file until you remove them
or their time is over, at which point they are deleted from the file. They are your own, deliberate entries, like an
address in a rule. Give entries an end date where you can.
