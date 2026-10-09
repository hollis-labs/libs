#!/bin/sh
# Fixture: SessionStart/basic-context. Requires the host to write the SessionStart input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"SessionStart"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"additionalContext":"branch main, 2 open tasks","systemMessage":"context loaded"}'
