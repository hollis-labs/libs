#!/bin/sh
# Fixture: UserPromptSubmit/basic. Requires the host to write the UserPromptSubmit input as JSON on stdin.
input=$(cat)
case "$input" in *'"hook_event_name":"UserPromptSubmit"'*) ;; *) echo "wrong or missing hook_event_name" >&2; exit 99 ;; esac
printf %s '{"additionalContext":"today is a weekday"}'
