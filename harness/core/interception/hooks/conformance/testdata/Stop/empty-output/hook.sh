#!/bin/sh
# Fixture: Stop/empty-output. Requires the host to write the Stop input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"Stop"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
:
