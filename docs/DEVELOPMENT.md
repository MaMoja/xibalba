# Development

## Requirements

- Go 1.24 or newer
- `make`
- Optional: [`golangci-lint`](https://golangci-lint.run) for the extended lint set

## Commands

| Command | What it does |
|---|---|
| `make build` | Build `bin/xibalba` for this machine |
| `make run` | Build and run with `xibalba.example.yaml` |
| `make test` | All tests with the race detector, including integration tests |
| `make lint` | Formatting check and `go vet`; `golangci-lint` too if installed |
| `make cross` | Build for linux/amd64 and linux/arm64 into `dist/` |
| `make bench` | Run the benchmarks (rule engine, crawler identification) |
| `make browser-check` | Check the visitor pages in a real browser (needs Playwright; see below) |
| `make webserver-check` | Run Xibalba behind real nginx, Caddy, Apache, HAProxy and Traefik with the example configurations; a server that is not installed is skipped |
| `make check` | `lint`, `test` and `cross`: everything CI runs |
| `make clean` | Remove build output |

Run `make check` before every commit.

## Layout

```
cmd/xibalba/        the program: wiring only
internal/           one package per job (see ARCHITECTURE.md)
test/integration/   tests that run the real binary
docs/               documentation
.claude/skills/     procedures for the coding agent
```

## Tests

- **Unit tests** sit next to the code as `*_test.go` and are table-driven.
  Failure cases come first: bad input, taken ports, panics, timeouts.
- **Integration tests** in `test/integration` build the binary, start it with
  a real configuration file, talk to it over HTTP and stop it with a signal.
  A feature is not done until an integration test shows it working from outside.
- Tests must not depend on the network or on fixed ports. Use port `0`.

## Browser check

`test/browser/check.py` opens the visitor pages in a real Chromium: it passes
the challenge with JavaScript, passes it without JavaScript using only the
keyboard, confirms that nothing is loaded from another host and that the
Content-Security-Policy is not violated, and runs the axe accessibility
checker over every page in light and dark mode. It starts its own test
website and its own Xibalba.

It is not part of `make check` or CI because it needs a browser:

```sh
pip install playwright && playwright install chromium
npm install axe-core            # optional: enables the accessibility check
make browser-check AXE=node_modules/axe-core/axe.min.js
```

Run it after every change to `internal/pages` or `internal/challenge`.

## Web server check

`test/webserver/check.py` starts a test website, Xibalba, and then nginx and
Caddy with the files from `examples/nginx` and `examples/caddy`. Through
HTTPS it checks that pages pass, that the website and the rules see the
visitor's real address even when the client sends a made-up
`X-Forwarded-For`, that the challenge works and its cookie is marked
`Secure`, that the block page arrives unchanged, and that an upgraded
connection passes both ways. Only site name, port and certificate paths of
the example files are changed for the test.

It needs `nginx`, `caddy` and `openssl` (`apt install nginx caddy`); a web
server that is not installed is skipped. Run it after any change to
`internal/clientip`, `internal/proxy` or the example files. The handbook
shows these files; a test keeps its copies equal to them.

## Sponsor licenses in tests

`pages.operator`, `pages.texts` and `pages.attribution: false` need a sponsor
license. Tests never use the project's real key: `newProject` in
`internal/config` and `licenseFile` in `test/integration` issue licenses with
key pairs made on the spot, and the integration tests build the binary to
trust theirs. See [MAINTAINING.md](MAINTAINING.md) for the real key.

## Documentation rules

Documentation is part of the change, not a follow-up.

| When you change | Also update |
|---|---|
| A setting | `internal/config`, `xibalba.example.yaml`, `docs/CONFIGURATION.md`, `docs/de/HANDBUCH.md` |
| What rules can do | `internal/rules`, `docs/RULES.md`, `examples/rules/basic.yaml` |
| A text visitors see | Every file in `internal/pages/assets/locales`, and the text-name table in `docs/CONFIGURATION.md` |
| A crawler definition or preset | `data/crawlers` or `data/presets` (use the `add-crawler` skill), the tables in `docs/CRAWLERS.md` and `docs/de/HANDBUCH.md` |
| The country database reader | Run its test against MaxMind's published test databases: clone `github.com/maxmind/MaxMind-DB` and set `XIBALBA_MMDB_TESTDATA` to the directory |
| A package or its job | Package comment, `docs/ARCHITECTURE.md` |
| A user-visible behaviour | `README.md` status table, `CHANGELOG.md` |
| A technical choice | `docs/DECISIONS.md` |
| A roadmap item | `docs/ROADMAP.md` |
| A new document in `docs/` | The list in `tools/wiki.py` and the table in `README.md` |

The [wiki](https://github.com/MaMoja/xibalba/wiki) is built from `docs/` by
`tools/wiki.py` and published by the `Wiki` workflow on every push to `main`.
Never edit the wiki itself: the next push overwrites it. `make wiki` builds
the pages into `dist/wiki` and fails on a link that leads nowhere.

Every package has a package comment that starts with its one job. Every
exported name has a comment that says what it is for, not how it works.

## Dependencies

The standard library comes first. A new dependency needs a permissive licence
(MIT, BSD, Apache-2.0), no cgo, and an entry in `docs/DECISIONS.md` saying why
it was worth it. Current dependencies:

| Module | Used for | Licence |
|---|---|---|
| `github.com/goccy/go-yaml` | Reading the configuration file with line positions | MIT |

## Commits

- One logical change per commit, with tests and documentation in the same commit.
- The message says what changed and why, in the imperative: "Reject unknown settings".

## Releases

Versions follow semantic versioning. Until 1.0, minor versions may change
configuration; every such change is listed in `CHANGELOG.md` with how to migrate.
