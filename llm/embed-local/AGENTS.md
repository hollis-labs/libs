# embed-local

In-process sentence embeddings with a local ONNX model, through hugot. `Embedder` satisfies `classify.Embedder` (libs `core/util/classify`).

It is not a model server, a model registry or a vector store. A change that starts a server, keeps models anywhere but the caller's directory, or stores vectors is the wrong change.

## Start Here

- `embedlocal.go` — `Options`, `New` (Go or ONNX Runtime backend), `Embed`, `Close`, `Download`.

## Commands

```sh
export GOWORK=off
scripts/check -race llm/embed-local
# With a model (CI has none, so these tests skip without it):
EMBED_LOCAL_MODEL=/path/to/all-MiniLM-L6-v2 go test -race ./...
# ONNX Runtime backend:
EMBED_LOCAL_MODEL=... EMBED_LOCAL_ORT_LIB=/path/to/libonnxruntime.dylib go test -tags ORT ./...
```

## Boundaries

- Go 1.27: hugot requires it. This module's toolchain is its own; it is not a reason to raise any other module's.
- The default backend is pure Go: no cgo and no shared library. ONNX Runtime is opt-in through the `ORT` build tag.
- Vectors are L2-normalised and returned in input order. Guarded by `TestEmbedsWithALocalModel`.
- Embed after Close fails rather than touching a released session. Guarded by `TestEmbedsWithALocalModel`.
