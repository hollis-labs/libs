package manifest_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/hollis-labs/plugin-sdk/manifest"
)

// Immutable build-v1 payload bytes/modes are shared with the Node build helper.
func TestBuildVectorTree(t *testing.T) {
	raw, err := os.ReadFile("testdata/build-v1/tree/plugin.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.TreeDigest(m.Artifact.Files)
	if err != nil || digest != m.Artifact.TreeSHA256 {
		t.Fatal(digest, err)
	}
	if err := m.VerifyBundle("testdata/build-v1/tree"); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := manifest.Encode(&encoded, m); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded.Bytes(), raw) {
		t.Fatal("fixture is not canonical Go manifest encoding")
	}
}
