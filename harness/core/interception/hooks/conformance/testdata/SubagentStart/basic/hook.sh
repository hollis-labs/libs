#!/bin/sh
# Fixture: SubagentStart/basic. Requires the host to write the SubagentStart input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"SubagentStart"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"additionalContext":"you are the reviewer"}'
