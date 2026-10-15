# go-llm-types

Transport-agnostic data structures for LLM chat and completion workflows —
requests, messages, tools, content and stream events — shared across the
toolchain. Data types only: no provider interfaces, no transport, no retry or
rate-budget logic. It has no dependencies at all beyond the standard library.

## Start Here

- `types.go` is the entire library.
- `README.md`'s "What's Here" / "What's Not Here" sections are the scope
  statement and name the companion modules.
- `examples/chatrequest`, `examples/streamevent` and `examples/slots` are
  runnable demos of the three main shapes.

## Commands

```bash
make vet
make test
```

`make lint` is an alias for `go vet ./...`. There is no CI workflow in this
repo.

## Boundaries

This module sits at the bottom of the LLM dependency graph — `go-llm-contracts`
and `go-providers` both import it. A dependency added here is inherited by
everything above, so keep it standard-library-only. Provider interfaces belong
in `go-llm-contracts`; adapters belong in `go-providers`.

`EffectiveSystemPrompt` is the composition rule, not a convenience: with no
slots it returns `SystemPrompt` unchanged, and with slots it returns the system
prompt followed by each non-empty slot's content in order. Callers rely on that
ordering to assemble a context window, so changing the join or the sequence
changes every prompt built through it.

Because the types are serialized across process and provider boundaries,
renaming a field or changing a JSON tag is a wire-format break even when it
compiles cleanly everywhere in this repo.
