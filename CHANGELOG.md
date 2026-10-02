# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

- Configuration file with full validation. Errors name the file, line and
  setting and say how to fix it; all problems are reported together.
- `-check` flag to validate a configuration file without starting.
- Operations listener with `/healthz` (per-component health) and `/version`.
- Component supervisor: ordered start, reverse-order stop, rollback when a
  component fails to start, and failures attributed to the component by name.
- HTTP listeners with timeouts, header size limit and per-request panic recovery.
- Structured logging in JSON or text with a `component` attribute.
- Graceful shutdown on SIGINT and SIGTERM with a configurable timeout.
