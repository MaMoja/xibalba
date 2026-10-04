# Web interface

Xibalba can show what it counted in a browser: how many requests were let
through, checked and blocked, which rules decided, which crawlers came, and
whether every part works.

The web interface is **optional and off by default**. Switched off, nothing
of it exists: no listener, no memory, no work. Switched on, it costs nothing
while nobody looks at it. It only shows; all settings stay in the
configuration file.

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
In a script, pipe the password in:
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
- **Your web server with HTTPS in front**, passing a host name or path of
  its own to `127.0.0.1:9091`, ideally restricted to your own network.

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
  seconds, then longer each time, up to 15 minutes.
- A login is a random value in a cookie that scripts cannot read and that
  the browser sends to no other site. Sessions live in memory only.
- The password is stored with PBKDF2-SHA256 and 600000 rounds. Checking one
  takes about a tenth of a second on a server and about a second on a
  Raspberry Pi; only one is checked at a time.
- The pages contain no script and load nothing from elsewhere.
- Password and sessions never appear in the log. A wrong password is logged
  as an event, without the address or what was typed.

The component is `admin` in `/healthz` and in the log.

## Planned

Changing settings in the browser (preset switches, block lists, a rule
editor) is planned for a later version. Today the web interface only reads.
