# Sponsoring Xibalba and the sponsor license

Xibalba is free software under the MIT licence. Everything that protects a
website works without paying anything and without a license file: the rules,
the security check, the block page, the counters.

Sponsors keep the project alive. In return, a sponsor may make the pages
visitors see entirely their own.

## What is free and what a sponsor license adds

| | Without a license | With a sponsor license |
|---|---|---|
| Rules, security check, blocking, counters | Yes | Yes |
| Pages in German and English, accessible | Yes | Yes |
| Contact line on the block page (`pages.contact`) | Yes | Yes |
| Default language (`pages.default_language`) | Yes | Yes |
| The line "Protected by Xibalba" at the bottom of every page | Always shown | Can be removed (`pages.attribution: false`) |
| Your name on the pages (`pages.operator`) | "The operator of this website" | Your name |
| Your own wording (`pages.texts`) | Standard wording | Any text replaced |

The line at the bottom reads "Protected by Xibalba · Support the project"
(in German: "Geschützt durch Xibalba · Projekt unterstützen"). The first link
leads to the project, the second to its sponsor page. It is small, loads
nothing, and sets no cookie.

Without a license, the three sponsor settings are refused when Xibalba
starts, with a message that says so:

```text
configuration /etc/xibalba/xibalba.yaml: 2 problems
  - line 4, pages.operator: this setting needs a sponsor license, and no license file is configured
    fix: sponsors receive a license file; set license.file to it (see docs/SPONSORS.md). Without a license, remove this setting: the pages then use the standard wording and show the line "Protected by Xibalba"
  - line 5, pages.attribution: this setting needs a sponsor license, and no license file is configured
    fix: sponsors receive a license file; set license.file to it (see docs/SPONSORS.md). Without a license, remove this setting: the pages then use the standard wording and show the line "Protected by Xibalba"
```

## How to become a sponsor

1. Sponsor the project on GitHub: <https://github.com/sponsors/MaMoja>,
   with the tier of 50 € per month or more.
2. You receive a license file for your organisation.
3. Install it as described below.

## Installing the license

Put the file next to your configuration, for example
`/etc/xibalba/sponsor.license`, and name it in `xibalba.yaml`:

```yaml
license:
  file: sponsor.license

pages:
  operator: "Stadt Musterhausen"
  attribution: false
```

The path is relative to the configuration file. Check and restart:

```sh
xibalba -check -config /etc/xibalba/xibalba.yaml
```

At start-up the log names the license:

```text
level=INFO msg="sponsor license" component=license licensee="Stadt Musterhausen" valid_until=2027-10-03
```

and `/healthz` lists it:

```json
"license": {"state": "ok"}
```

A license file is plain text. Lines starting with `#` say who it was issued
to and until when; the long line below them is the license itself. Copy the
file as a whole and do not change it.

## When a license runs out

A license has an expiry date. **An expired license never stops Xibalba and
never takes your website down.**

| When | What happens |
|---|---|
| Up to and including the expiry date | Everything works. |
| For 30 days after that (grace period) | Everything still works. The log warns at start-up and `/healthz` shows `license` as `degraded` with both dates. |
| After the grace period | Xibalba starts normally. The pages return to the standard wording and show the Xibalba line again; `pages.operator`, `pages.texts` and `pages.attribution` are not applied. `pages.contact` and everything else keep working. Log and `/healthz` say why. |

The license is read when Xibalba starts. If it runs out while Xibalba is
running, nothing changes until the next restart; `/healthz` says so in advance.

In the grace period:

```json
"license": {
  "state": "degraded",
  "detail": "the sponsor license expired on 2026-09-20; it keeps working until 2026-10-20, please renew it"
}
```

After it:

```json
"license": {
  "state": "degraded",
  "detail": "the sponsor license expired on 2026-01-31; the pages use the standard wording and show the Xibalba line"
}
```

To renew, replace the file with the new one you receive and restart.

## How the check works

The license is a statement (who, until when) signed with the project's
private key. Xibalba checks the signature with the public key built into the
program, on your own machine.

- **Nothing is sent anywhere.** Xibalba does not contact the project, GitHub
  or anyone else to check a license. It works on a machine without internet
  access.
- **Nothing about your visitors is involved.**
- A file that was changed, cut off, or issued by someone else is refused at
  start-up with a message that says so.

## A word on honesty

The source code is open, and anyone who wants to can build a Xibalba without
the line or without the check; the MIT licence allows that. The sponsor
license is not copy protection. It is a simple way for organisations that
benefit from Xibalba to support the people who build it, and to say so with
a clear conscience.
