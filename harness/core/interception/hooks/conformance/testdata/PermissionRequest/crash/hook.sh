#!/bin/sh
# Fixture: PermissionRequest/crash. Requires the host to write the PermissionRequest input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"PermissionRequest"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
echo "boom" >&2; exit 17
