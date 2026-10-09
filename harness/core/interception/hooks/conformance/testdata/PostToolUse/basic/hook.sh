#!/bin/sh
# Fixture: PostToolUse/basic. Requires the host to write the PostToolUse input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PostToolUse"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"additionalContext":"file changed, rerun tests"}'
