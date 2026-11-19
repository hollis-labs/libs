# Changelog

## [Unreleased]

## [0.1.0] - 2026-11-16

- First version: an embedded agent host an application mounts over HTTP. It runs the model
  conversation through a gateway, keeps threads in SQLite, streams chatstream/v1 events, and
  relays tool calls to the page that declared them. The wire is in `WIRE.md`.
