#!/bin/sh
# Fixture: PostCompact/basic. Requires the host to write the PostCompact input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PostCompact"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"systemMessage":"compacted"}'
