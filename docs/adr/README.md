# Architecture Decision Records

Short records of decisions that shape more than one file: a direction was
chosen, alternatives existed, and the reasoning is worth keeping around
after the commit that made it scrolls out of `git log`. These are project-
wide calls, not subsystem design - subsystem rationale belongs in
[docs/site/content/docs/concepts/architecture.md](../site/content/docs/concepts/architecture.md)
and its sibling pages. Coding-style rules that aren't a "which direction"
decision belong in [GO_CONVENTIONS.md](../GO_CONVENTIONS.md) instead (several
ADRs here reference a GO-### rule that codifies the same call at the
language level).

Each ADR has an ID (`ADR-####`) so a review comment or a commit can point at
one directly. Numbered in the order they were written, not necessarily the
order the decisions were made.

| ID | Title |
|----|-------|
| [ADR-0001](0001-breaking-changes-over-compat-shims.md) | Breaking changes over compatibility shims, pre-1.0 |
| [ADR-0002](0002-type-safety-over-runtime-validation.md) | Type safety over runtime validation |
| [ADR-0003](0003-structured-values-over-formatted-strings-in-wire-protocol.md) | Structured values over pre-formatted strings in the wire protocol |
| [ADR-0004](0004-fail-fast-over-blocking-on-contention.md) | Fail fast over blocking on contention |
| [ADR-0005](0005-shell-out-to-clis-over-embedding-libraries.md) | Shell out to CLIs over embedding their libraries |
| [ADR-0006](0006-struct-plus-error-over-multi-value-returns.md) | A single struct plus error, not multi-value returns |

Before entering plan mode on a non-trivial design, read this index and see
[AGENTS.md](../../AGENTS.md)'s "Before planning a design" section.
