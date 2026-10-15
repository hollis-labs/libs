#!/bin/sh
# Fixture: UserPromptSubmit/deny. Requires the host to write the UserPromptSubmit input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"UserPromptSubmit"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"decision":"deny","reason":"prompt contains a secret"}'
