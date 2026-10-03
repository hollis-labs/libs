package artifactfs_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hollis-labs/go-workflow-host/artifactfs"
	"github.com/hollis-labs/go-workflow/values"
)

func Example() {
	// The root must be a real path: New rejects any symlink component.
	tmp, _ := os.MkdirTemp("", "artifacts-")
	defer func() { _ = os.RemoveAll(tmp) }()
	root, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		panic(err)
	}

	// The authority is persisted identity: pick it once, never change it.
	store, err := artifactfs.New(root, "example-local", artifactfs.OwnerClaimAuthorizer("example-local", ""))
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	access := values.ArtifactAccess{Principal: "worker", RunID: "run-1", At: now}
	request := values.ArtifactPutRequest{
		Store: "example-local",
		Owner: values.ArtifactOwner{Scope: values.ArtifactOwnerRun, ID: "run-1"},
		Metadata: values.Metadata{
			Producer:  values.Producer{Kind: "node_output", Reference: "invocation-1", Output: "result"},
			MediaType: "text/plain", Redaction: values.RedactionPrivate, Retention: values.RetentionRun,
		},
		MaxBytes: 1 << 20, CreatedAt: now, Access: access,
	}
	value, err := values.CaptureValue(ctx, store, request, strings.NewReader("hello, artifact"), values.CapturePolicy{
		Mode: values.CaptureArtifactOnly, InlineLimit: values.DefaultInlineLimit,
	})
	if err != nil {
		panic(err)
	}

	reader, err := store.Open(ctx, access, *value.Artifact)
	if err != nil {
		panic(err)
	}
	defer func() { _ = reader.Close() }()
	body, _ := io.ReadAll(reader)
	fmt.Println(string(body), reader.Verified())
	// Output: hello, artifact true
}
