// Package embedlocal embeds texts in-process with a local sentence-embedding
// model (ONNX, for example all-MiniLM-L6-v2 or bge-small), through hugot.
// Embedder satisfies classify.Embedder, so a classify.Centroid judge can run at
// the keystroke or submit phase with no network call.
//
// Two backends:
//
//   - BackendGo (default): pure Go (GoMLX). No cgo, no shared library; slower,
//     fine for small models and short texts.
//   - BackendORT: ONNX Runtime. The fastest. It needs a binary built with the
//     ORT build tag (-tags ORT) and libonnxruntime on the machine
//     (Options.OnnxLibraryPath, or the platform default). Without the tag, New
//     returns an error for it.
//
// Download fetches a model from Hugging Face into a directory once. New loads
// it from there. Vectors are L2-normalised.
package embedlocal
