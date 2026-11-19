# Real Agy capture evidence

Captured 2026-11-19 with `agy --version` = `1.5.12`.
Each output is stdout NDJSON from a separate CLI invocation; stderr is separate.

- text: `agy --output-format stream-json --print-timeout 45s -p='Reply with exactly: capture-ok. Do not use tools.'`; exit 0.
- tool_failure: `agy --output-format stream-json --print-timeout 60s -p='Use view_file to read example.txt in the current directory. Then use view_file to read missing-capture-file.txt in the current directory so we can see the expected missing-file error. Do not create or modify files. Report the outcome briefly.'`; example.txt contained `capture fixture\n`. Both tools actually failed at a missing local telemetry hook, rather than missing-file/read success. Exit 0, result SUCCESS. This is tool-failure evidence only.
- failed_turn: `agy --output-format stream-json --print-timeout 20s --model capture-nonexistent-model -p='Reply with capture-ok. Do not use tools.'`; exit 1. Emits a result without init.
- timeout: `agy --output-format stream-json --print-timeout 1s -p='Write a long explanation of the numbers from one through one hundred. Do not use tools.'`; exit 3, finished_reason print_timeout. This is timeout evidence, not user interruption.
- cancelled_turn: `agy --output-format stream-json --print-timeout 20s -p='Explain counting from one to one thousand in detail. Do not use tools.'`; Python subprocess sent SIGINT after 2 seconds, exit 1, finished_reason interrupted.

Sanitization: conversation IDs normalized to capture-conversation; local working directory to /work/capture; missing-hook path to /work/hooks/missing-hook.py. For private paths split across text_delta frames, accumulated text was sanitized and split again using original chunk lengths (the final chunk takes the remainder). Empty deltas may therefore result. No event states/types, terminal reasons or usage values changed. JSON is pretty-printed into conformance Fixture shape. No fixtures here yet establish successful tools or actual permission denial.
