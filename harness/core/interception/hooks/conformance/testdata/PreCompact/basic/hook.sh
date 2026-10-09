#!/bin/sh
# Fixture: PreCompact/basic. Requires the host to write the PreCompact input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PreCompact"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"additionalContext":"keep the task list"}'
