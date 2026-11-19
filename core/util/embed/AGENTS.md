# embed

Texts to vectors through an AI gateway: OpenAI-compatible `POST /v1/embeddings`, or `POST /ai/embeddings`. `Client` satisfies `classify.Embedder`.

It is not a model runtime or a vector store. A local model (ONNX, cgo) is a separate module; storing or searching vectors is the caller's.

## Start Here

- `embed.go` — `Client`, the wire shape, batching, the dimension check.

## Commands

```sh
export GOWORK=off
scripts/check -race core/util
```

## Boundaries

- Standard library only (its tests use `classify`).
- Vectors come back in input order whatever order the gateway sends them, and in batches of `MaxBatch`. Guarded by `TestEmbedBatchesAndKeepsOrder`.
- A change of dimension between calls is an error, not a silent mix of models. Guarded by `TestEmbedRefusesADimensionChange`.
- Neither the token nor the input text appears in an error. Guarded by `TestEmbedErrorsDoNotLeakTheToken`.
