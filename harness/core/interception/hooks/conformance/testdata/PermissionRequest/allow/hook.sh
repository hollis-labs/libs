#!/bin/sh
# Fixture: PermissionRequest/allow. Requires the host to write the PermissionRequest input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PermissionRequest"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"decision":"allow","reason":"read-only command"}'
