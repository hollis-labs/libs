# Changelog

All notable changes to the `embed-local` module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the module adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Releases are tagged
`llm/embed-local/vX.Y.Z`.

## [Unreleased]

## v0.1.0 — 2026-11-19

### Added

- `Embedder`: in-process sentence embeddings with a local ONNX model through hugot, L2-normalised, in batches, safe for concurrent use. It satisfies `classify.Embedder`. The pure-Go backend is the default; ONNX Runtime is behind the `ORT` build tag (`Options.OnnxLibraryPath`). `Download` fetches a Hugging Face model into a directory. Requires Go 1.27.
