#!/bin/sh
# Fixture: PreToolUse/ask. Requires the host to write the PreToolUse input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PreToolUse"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"decision":"ask","reason":"needs a human"}'
