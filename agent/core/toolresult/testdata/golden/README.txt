These files are the output of Nanite's own code (internal/tool ResultCache
PresentResult / ReadPage / SearchPage and the chat_tool_executor.go fetch and
search handlers, copied verbatim into a scratch module outside any repo) for
the cases in golden_cases_test.go, with the pointer id and expiry replaced by
<ID> and <EXPIRES>. TestGoldenParityWithNanite asserts this module emits the
same bytes. The harness is not committed. Regenerate only on purpose: a diff
here means the agent-facing prompt text changed.
