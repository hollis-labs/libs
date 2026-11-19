package embedlocal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"
	"github.com/knights-analytics/hugot/pipelines"
)

// Backend is the inference engine.
type Backend string

const (
	// BackendGo is pure Go: no cgo, no shared library.
	BackendGo Backend = "go"
	// BackendORT is ONNX Runtime: build with -tags ORT, and libonnxruntime present.
	BackendORT Backend = "ort"
)

// Options configure an Embedder.
type Options struct {
	// ModelPath is the model's directory (from Download): an ONNX graph and its
	// tokenizer.
	ModelPath string
	// OnnxFilename picks one graph when the directory has several (model.onnx,
	// model_quantized.onnx); empty lets hugot choose.
	OnnxFilename string
	// Name names the model in Embedder.Name, so a centroid fitted on one model is
	// not mistaken for another's. Empty is the directory's base name.
	Name string
	// Backend is BackendGo when empty.
	Backend Backend
	// OnnxLibraryPath is libonnxruntime's path for BackendORT; empty is the
	// platform default.
	OnnxLibraryPath string
	// MaxBatch is how many texts go through the model at once; 0 is 32.
	MaxBatch int
}

// Embedder embeds texts with a local model. Safe for concurrent use: calls are
// serialised, since one session holds one model.
type Embedder struct {
	name     string
	maxBatch int

	mu      sync.Mutex
	session *hugot.Session
	pipe    *pipelines.FeatureExtractionPipeline
	closed  bool
}

// New loads the model. Close releases it.
func New(ctx context.Context, o Options) (*Embedder, error) {
	if strings.TrimSpace(o.ModelPath) == "" {
		return nil, errors.New("embedlocal: a model path is required (see Download)")
	}
	var (
		s   *hugot.Session
		err error
	)
	switch o.Backend {
	case "", BackendGo:
		s, err = hugot.NewGoSession(ctx)
	case BackendORT:
		var opts []options.WithOption
		if o.OnnxLibraryPath != "" {
			opts = append(opts, options.WithOnnxLibraryPath(o.OnnxLibraryPath))
		}
		s, err = hugot.NewORTSession(ctx, opts...)
	default:
		return nil, fmt.Errorf("embedlocal: unknown backend %q", o.Backend)
	}
	if err != nil {
		return nil, fmt.Errorf("embedlocal: %s session: %w", backendName(o.Backend), err)
	}
	pipe, err := hugot.NewPipeline(s, hugot.FeatureExtractionConfig{
		ModelPath:    o.ModelPath,
		Name:         "embedlocal",
		OnnxFilename: o.OnnxFilename,
		Options:      []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		_ = s.Destroy()
		return nil, fmt.Errorf("embedlocal: load %s: %w", o.ModelPath, err)
	}
	name := o.Name
	if name == "" {
		name = baseName(o.ModelPath)
	}
	batch := o.MaxBatch
	if batch <= 0 {
		batch = 32
	}
	return &Embedder{name: name, maxBatch: batch, session: s, pipe: pipe}, nil
}

// Name implements classify.Embedder.
func (e *Embedder) Name() string { return "local:" + e.name }

// Embed implements classify.Embedder: one L2-normalised vector per text, in order.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, errors.New("embedlocal: closed")
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += e.maxBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+e.maxBatch, len(texts))
		res, err := e.pipe.RunPipeline(ctx, texts[start:end])
		if err != nil {
			return nil, fmt.Errorf("embedlocal: %w", err)
		}
		if len(res.Embeddings) != end-start {
			return nil, fmt.Errorf("embedlocal: %d vectors for %d texts", len(res.Embeddings), end-start)
		}
		out = append(out, res.Embeddings...)
	}
	return out, nil
}

// Close releases the model. Embed fails after it.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	return e.session.Destroy()
}

// Download fetches a Hugging Face model (for example
// "sentence-transformers/all-MiniLM-L6-v2") into dir and returns its path.
// onnxFile picks a graph inside the repository ("onnx/model.onnx"); empty takes
// the repository's default.
func Download(ctx context.Context, model, dir, onnxFile string) (string, error) {
	o := hugot.NewDownloadOptions()
	if onnxFile != "" {
		o.OnnxFilePath = onnxFile
	}
	p, err := hugot.DownloadModel(ctx, model, dir, o)
	if err != nil {
		return "", fmt.Errorf("embedlocal: download %s: %w", model, err)
	}
	return p, nil
}

func backendName(b Backend) string {
	if b == "" {
		return string(BackendGo)
	}
	return string(b)
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
