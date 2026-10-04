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

## The list of sponsors in the README

The workflow `Sponsors` asks GitHub once a day who sponsors the project and
rewrites the list between the two marker lines in `README.md`
(`tools/sponsors.py`). Nothing has to be collected from sponsors:

| Tier | What happens by itself |
|---|---|
| from $10 a month | The name of the sponsor's GitHub account appears under "Backers", linked to the account. |
| from $25 a month | The picture of the sponsor's GitHub account appears, linked to the account. An organisation's account picture is its logo. |
| Sponsor chose "private" on GitHub | Never listed, whatever the tier. |
| Sponsorship ends | The entry disappears at the next run. |

One-time payments are not listed. A sponsor who wants a different picture
changes the picture of the GitHub account.

**Setting it up, once.** The workflow needs a token of the account that
receives the sponsorships, because only that account may see the tiers:

1. On GitHub: Settings, Developer settings, Personal access tokens, Tokens
   (classic), generate a token with the scopes `read:user` and `read:org`.
2. In the repository: Settings, Secrets and variables, Actions, new
   repository secret named `SPONSORS_TOKEN` with that token.
3. Actions, workflow "Sponsors", "Run workflow", to try it.

Without the secret the workflow runs and does nothing. The first real run
is the test of the question put to GitHub; it has only been tested against
made-up answers so far.

**What stays by hand: the license file.** It is signed with the private key,
and that key stays off GitHub (see above). GitHub sends an e-mail for every
new sponsorship; for the $50 tier and above, issue the license with
`xibalba-license issue` and send it to the sponsor.

## Publishing a version

1. Move the entries under "Unreleased" in `CHANGELOG.md` to a heading with
   the version and the date, and commit.
2. Try it: Actions, workflow "Release", "Run workflow". This builds
   everything, installs the Debian package on the build machine, starts the
   service under systemd, builds the image for all three kinds of
   processor, and publishes nothing. The files are kept with the run for
   seven days.
3. Publish: run the workflow again with the version (without the `v`) and
   "publish" ticked, or push a tag: `git tag v1.2.3 && git push origin
   v1.2.3`. If everything passes, the workflow creates the release with the
   files (and the tag, if it is not there) and pushes the image
   `ghcr.io/mamoja/xibalba:1.2.3` (and `:latest`). Only a version that has
   its heading in `CHANGELOG.md` is published. A version with a hyphen,
   such as `1.2.3-rc1`, becomes a pre-release and does not move `latest`.

What a release holds, built by `tools/release.sh` (`make release
RELEASE=1.2.3` does the same on your machine):

| File | For |
|---|---|
| `xibalba_<version>_linux_{amd64,arm64,armv7}.tar.gz` | any Linux: program, example configuration, service file |
| `xibalba_<version>_{amd64,arm64,armhf}.deb` | Debian, Ubuntu, Raspberry Pi OS |
| `SHA256SUMS` | checking a download |

The first time an image is pushed, GitHub creates the package as private:
open it under the repository's "Packages" and set its visibility to public.
