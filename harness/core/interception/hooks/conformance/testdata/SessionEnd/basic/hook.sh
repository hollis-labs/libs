#!/bin/sh
# Fixture: SessionEnd/basic. Requires the host to write the SessionEnd input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"SessionEnd"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"systemMessage":"session logged"}'
