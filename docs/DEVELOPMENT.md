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
| `make bench` | Run the benchmarks (rule engine) |
| `make browser-check` | Check the visitor pages in a real browser (needs Playwright; see below) |
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

## Documentation rules

Documentation is part of the change, not a follow-up.

| When you change | Also update |
|---|---|
| A setting | `internal/config`, `xibalba.example.yaml`, `docs/CONFIGURATION.md`, `docs/de/HANDBUCH.md` |
| What rules can do | `internal/rules`, `docs/RULES.md`, `examples/rules/basic.yaml` |
| A text visitors see | Every file in `internal/pages/assets/locales`, and the text-name table in `docs/CONFIGURATION.md` |
| A package or its job | Package comment, `docs/ARCHITECTURE.md` |
| A user-visible behaviour | `README.md` status table, `CHANGELOG.md` |
| A technical choice | `docs/DECISIONS.md` |
| A roadmap item | `docs/ROADMAP.md` |

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
