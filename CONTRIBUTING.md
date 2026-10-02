# Contributing

Thank you for looking at Xibalba. The project is in early development, so the
most useful contributions right now are bug reports, questions about unclear
documentation, and reviews of the design in `docs/ARCHITECTURE.md`.

## Before you write code

1. Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) and
   [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
2. Check the [roadmap](docs/ROADMAP.md). Work happens one milestone at a time.
3. For anything larger than a small fix, open an issue first so the approach
   can be agreed before you spend time on it.

## What a change needs

- Tests, with the failure cases covered.
- Documentation in the same change (the table in `docs/DEVELOPMENT.md` says which files).
- `make check` passes.

## Ground rules

- **No code from other projects** unless its licence allows it and the source
  is credited. In particular, nothing is copied from Anubis.
- **Crawler data needs a source.** Every crawler definition links to the
  operator's own documentation and carries the date it was checked.
- **Visitor pages must stay accessible** (WCAG 2.1 AA) and must not load
  anything from a third party.
- **No personal data by default.** A feature that stores IP addresses must be
  off by default and documented.

## Security problems

Do not open a public issue. See [SECURITY.md](SECURITY.md).
