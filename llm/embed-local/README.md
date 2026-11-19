# embed-local

Embeds texts in-process with a local sentence-embedding model (ONNX), so a `classify.Centroid` judge can run as the user types, with no network call. It is built on [hugot](https://github.com/knights-analytics/hugot).

## Status

**Pre-release.** No compatibility guarantees until the first tagged release of this module. Requires Go 1.27.

## Usage

```go
dir, _ := embedlocal.Download(ctx, "KnightsAnalytics/all-MiniLM-L6-v2", modelsDir, "") // once
emb, err := embedlocal.New(ctx, embedlocal.Options{ModelPath: dir})                      // pure Go
defer emb.Close()
centroid := classify.NewCentroid("centroid:slot", "slot", emb, classify.CentroidOptions{Phase: classify.Keystroke})
```

- **Backends.** `BackendGo` (the default) needs no cgo and no shared library. `BackendORT` is faster: build with `-tags ORT` and have `libonnxruntime` installed (`Options.OnnxLibraryPath`).
- **What embeddings are good at.** A sentence-embedding model groups texts by **topic** more than by what they ask for. "How come the tests broke?" sits nearer "fix the failing test" than any question. Use it for topic-like facets (which module or slot, which project). For speech acts (question, task, approval), rules, n-grams or a decision model do better.
