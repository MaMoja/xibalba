# Maintaining Xibalba

Notes for the maintainer. Operators do not need this document.

## Sponsor licenses

Sponsors receive a license file that unlocks the appearance settings
(`pages.operator`, `pages.texts`, `pages.attribution`). What it means for an
operator is described in [SPONSORS.md](SPONSORS.md).

### The key pair

Licenses are signed with an Ed25519 private key. The matching public key is
built into the program (`publicKeyHex` in `internal/license/license.go`).

- **The private key is not in the repository and must never be.** Files named
  `*.private.key` are ignored by git as a safety net. Keep the key in a
  password manager and an offline backup.
- Whoever has the private key can issue licenses.
- If the key is lost, no new licenses can be issued for existing
  installations. If it leaks, anyone can issue licenses. In both cases:
  make a new pair, build the new public key into the program, release, and
  issue new licenses to all sponsors. Existing licenses stop verifying in
  the new release.

```sh
go run ./cmd/xibalba-license keygen
```

### Issuing a license

```sh
go run ./cmd/xibalba-license issue \
    -key /path/to/xibalba-license-private.key \
    -licensee "Stadt Musterhausen" \
    -sponsor musterhausen > musterhausen.license
```

| Flag | Meaning |
|---|---|
| `-key` | File holding the private key. |
| `-licensee` | The organisation's name. Shown in the operator's log. |
| `-sponsor` | The sponsoring GitHub account. For your own records. |
| `-days` | How many days the license is valid, counted from today. Default: 365, a full year. |
| `-expires` | Instead of `-days`: the last day of validity as `YYYY-MM-DD`. |

Without either flag a license is valid for 365 days. It works 30 more days
after its last day.

Choose the expiry date to match how you want to follow up on sponsorships:
a year ahead is the least work; a shorter term means renewing more often.
The program cannot see whether a sponsorship is still running; the expiry
date is the only control.

The output is the complete license file. Send it to the sponsor as it is.

### Checking a license

```sh
go run ./cmd/xibalba-license show musterhausen.license
```

```text
licensee: Stadt Musterhausen
sponsor:  musterhausen
issued:   2026-10-03
expires:  2027-10-03
state:    valid
id:       c2bf8cb6764a0e01
```

### What the check can and cannot do

It verifies, offline, that a license was issued with the project's key and
when it expires. It cannot stop someone from building the program without
the check; the source is open. It is a courtesy lock for organisations that
want to do the right thing, and it is documented as such.

### Tests

The unit and integration tests issue licenses with key pairs of their own.
The integration tests build the binary with their public key
(`-ldflags -X …/internal/license.publicKeyHex=…`). The real private key is
never needed to run the tests.
