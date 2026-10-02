---
name: build-milestone
description: Use when starting, continuing or closing a Xibalba roadmap milestone, or when asked to "build the next step".
---

# Build a Xibalba milestone

1. **Orient.** Read `CLAUDE.md`, `docs/ROADMAP.md` and `docs/DECISIONS.md`.
   The current milestone is the first one with unticked items. Run the tests
   first so you know the starting state.
2. **Check parity.** If the milestone covers a feature Anubis has, read the
   matching page of the Anubis documentation for behaviour and edge cases.
   Correct `docs/SPEC.md` if it was wrong. Do not open or copy their source.
3. **Plan small.** Write the items of this milestone as a task list. Each item
   should be one commit. If an item needs a product decision, ask the owner
   before building; if it needs a technical decision, make it and log it.
4. **Build test-first for logic.** For the rule engine, tokens, identity checks
   and config validation, write the table of cases first, including hostile
   input: empty, huge, malformed, spoofed headers, expired and forged tokens.
5. **Integrate.** Add or extend an integration test that runs the real binary
   against the fake upstream and proves the milestone's behaviour end to end.
6. **Verify.** `go vet ./...`, `go test -race ./...`, integration tests,
   cross-compile for linux/arm64. For anything visible, open it in a browser
   and look at it.
7. **Review.** If the milestone touched the request path, tokens or admin
   interface, run the `security-review` skill.
8. **Close.** Tick the roadmap items, add decisions to `docs/DECISIONS.md`,
   and report to the owner: what works now, how to try it, what failed or was
   left out, and what the next milestone is.

Do not start the next milestone in the same pass unless the owner asked for it.
