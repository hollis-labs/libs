# embed

Embeds texts through an AI gateway's embeddings operation (`POST /ai/embeddings`), so a `classify.Centroid` judge can use a gateway's embedding model with no local model and no cgo.

## Status

**Pre-release.** No compatibility guarantees until the first tagged release of its module.

## Usage

```go
emb := embed.NewClient("http://127.0.0.1:8080", token, "my-app", "text-embedding-3-small") // pin the model
centroid := classify.NewCentroid("centroid:act", "act", emb, classify.CentroidOptions{Phase: classify.Async})
err := centroid.Fit(ctx, pack.Examples("act"))
```

Vectors from different models are not comparable. The client refuses a change of dimension between calls, and its `Name` carries the model, so a fitted centroid records which model it used.
