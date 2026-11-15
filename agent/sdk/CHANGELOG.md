# Changelog

## Unreleased

- Reserve single-use logical run IDs atomically in host-owned retained state
  before model/memory/tool activity; carry the immutable reservation through each
  effect admission and refuse uncertain/replayed inputs across replacements.


- Add the experimental headless definition-to-operative facade over the existing
  runloop, model-turn reader and canonical context window.
- Add explicit model routing, pinned local/MCP tools, policy projection, native
  once-scoped approval, and injected environment/atomic effect admission ports.
- Add bounded scoped memory retrieval, correlated envelope-1 events, cancellation
  and failure reporting, real-loop fake-host conformance and a runnable example.
