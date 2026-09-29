#!/bin/sh
# Fixture: SubagentStop/basic. Requires the host to write the SubagentStop input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"SubagentStop"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"continue":false,"stopReason":"tests still failing"}'
