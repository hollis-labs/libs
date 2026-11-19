# Agy stream-json captures

Captured 2026-11-19 from the installed Linux x86-64 Agy CLI, executable SHA256
`94104f028e6962fa2f71d393a7ae27d835ceba1c4898422a103e22c6a5fd6828`.
The binary does not expose a version flag in its help or Go build metadata.
Each NDJSON file is a real stdout recording. The sole redaction is init.cwd,
replaced by `/work`; conversation IDs, deltas, usage and tool fields are retained.
Sibling stderr files record the independent stderr stream. Captures ran in an
empty scratch directory with no repository content supplied to the model.

Commands (each redirects stdout/stderr to the matching sibling capture files):

- plain: `agy --output-format stream-json --print-timeout 45s -p='Reply with exactly hello.'`
- text: `agy --output-format stream-json --print-timeout 45s -p='Reply with exactly two sentences about a blue river. Do not use tools.'`
- permission: `agy --output-format stream-json --print-timeout 30s -p='Use run_command to run printf agy_permission_capture. Do not ask questions or use other tools.'`
- failed: `agy --output-format stream-json --print-timeout 1ms -p='Explain the numbers from one through one thousand in detail.'`
- cancelled: `agy --output-format stream-json --print-timeout 60s -p='Write a very long story about a river.'`; send SIGINT after three seconds.

The failed capture is a real deadline failure, not a fabricated provider error.
Cancellation is `status: ERROR`, `finished_reason: interrupted`, not a separate
cancel event. Permission denial is `result.denied_actions` even though status is
SUCCESS; tool completion alone therefore does not prove successful execution.

The sibling frames fixtures wrap each stdout line as conformance fixture data.
`malformed.frames.json` inserts the actual cancellation stderr line
`error: interrupted` into plain stdout. This intentionally tests a mixed stream;
no malformed JSON was observed on stdout, and this fixture must not be mistaken
for a naturally malformed stdout capture.

Additional tool captures use existing read-only permissions, without changing
permission configuration:

- tool_failure: `agy --output-format stream-json --print-timeout 60s -p='Use list_dir to list the current directory. Then use view_file to open the nonexistent file /work/agy_capture_nonexistent_7.txt so that the tool fails. Do not use any command tools. Report both outcomes.'`
  The CLI resolved these requests to read_file. Reading the directory returned
  TOOL_ERROR (not a permissions error); the separate nonexistent path was denied.
  Every occurrence of the scratch working directory in parameters is `/work`.

Cache evidence: the tool-success turn's second model step reports input_tokens
2565 and cache_read_tokens 9619. Cache therefore cannot be a subset of input;
Agy reports uncached input separately. output_tokens includes thinking_tokens.
Its total_tokens counter excludes cache reads; it is retained in the capture,
not used to override the chatstream disjoint-component total.

- tool_success: create `agy_capture_text.txt` containing `agy capture success` plus
  newline (20 bytes), then run `agy --output-format stream-json --print-timeout 45s -p='Use view_file to read agy_capture_text.txt in the current directory. Do not use any other tools. After reading it, reply only OK. Do not include file paths or links in your response.'`
  Agy maps view_file to read_file. Successful tool_info.output is a summary,
  `1 lines, 20 bytes`, not the full file contents. This adapter preserves exactly
  that upstream output, and never invents file content. Scratch working-directory
  prefixes in tool parameters are redacted to `/work` as in the failure capture.

- cancelled_stream: `agy --output-format stream-json --print-timeout 45s -p='Write a very long story about a river. Do not use tools.'`; a line-reading harness sends SIGINT immediately after the first nonempty text_delta. The result repeats the partial response and reports ERROR/interrupted. No DONE update arrives.
- truncated.frames.json is derived from cancelled_stream by removing the result
  frame, simulating transport EOF. It is not represented as a separate live run.
