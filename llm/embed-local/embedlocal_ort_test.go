//go:build ORT

package embedlocal

import (
	"context"
	"os"
	"testing"
)

// With -tags ORT, EMBED_LOCAL_MODEL and EMBED_LOCAL_ORT_LIB (libonnxruntime's path) set,
// the ONNX Runtime backend embeds the same way.
func TestEmbedsWithONNXRuntime(t *testing.T) {
	lib := os.Getenv("EMBED_LOCAL_ORT_LIB")
	if lib == "" {
		t.Skip("EMBED_LOCAL_ORT_LIB is not set")
	}
	e, err := New(context.Background(), Options{ModelPath: modelPath(t), Backend: BackendORT, OnnxLibraryPath: lib})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	vecs, err := e.Embed(context.Background(), []string{"fix the failing build", "what a lovely sunny day"})
	if err != nil || len(vecs) != 2 {
		t.Fatalf("vectors %d: %v", len(vecs), err)
	}
}
