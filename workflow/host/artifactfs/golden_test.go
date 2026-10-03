package artifactfs

import (
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/workflow/values"
)

// The expected ids were computed once from the consuming apps' own
// implementations at the time of extraction (Hadron 34341a1, Nanite 0eaf01d1)
// for the fixed put request below, and are pinned here: they prove that
// persisted references and existing data directories stay valid.
const goldenOwnerHash = "d34fcd56467bdad32e6e991378ca9a085c1ed5e1f52652be77b45fd1748835bd" // sha256("run-golden")

func TestGoldenArtifactIDsMatchExistingDeployments(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		authority string
		expires   time.Time
		want      string
	}{
		{"hadron-local", time.Time{}, "3ba99e61fffb38724606891a9b9d118b9db25a2cd40c943b429c190dee1c213b"},
		{"hadron-local", expires, "eb7e070499787319b072c06f80544f86c1102cf8c514fa9205e85a4c81021a7c"},
		{"nanite-content-addressed-artifacts", time.Time{}, "4378372a235bde8fb3d0fcc81dcbedf76a42e15a5326fb10a9a7b2fa294260da"},
		{"nanite-content-addressed-artifacts", expires, "109d10f438852b66010d4572f5ce7cae744a915f77cf03a6711ba44bbdcca003"},
	}
	if got := artifactOwnerHash("run-golden"); got != goldenOwnerHash {
		t.Fatalf("owner hash=%s want %s", got, goldenOwnerHash)
	}
	for _, tc := range cases {
		name := tc.authority
		if !tc.expires.IsZero() {
			name += "/expiring"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			identity := artifactIdentity{
				Store: tc.authority, OwnerScope: values.ArtifactOwnerRun, OwnerHash: goldenOwnerHash,
				Digest: values.SHA256Digest([]byte("golden")), MediaType: "application/octet-stream", SizeBytes: 6,
				Producer:  values.Producer{Kind: "node_output", Reference: "invocation-1", Output: "result"},
				Redaction: values.RedactionPrivate, Retention: values.RetentionRun,
				CreatedAt: created, ExpiresAt: tc.expires,
			}
			got, err := artifactIDFor(identity)
			if err != nil || got != tc.want {
				t.Fatalf("artifactID=%s err=%v want %s", got, err, tc.want)
			}

			// End to end: a real Put lands on the same id and URI.
			store := newAuthorityTestStore(t, t.TempDir(), tc.authority, &recordingAuthorizer{}, nil)
			request := testPutRequest(values.RetentionRun, values.RedactionPrivate, "run-golden")
			request.Store = tc.authority
			request.ExpiresAt = tc.expires
			request.MaxBytes = 1 << 20
			request.CreatedAt, request.Access.At = created, created
			metadata, err := store.Put(t.Context(), request, strings.NewReader("golden"))
			if err != nil {
				t.Fatal(err)
			}
			wantURI := "artifact://" + tc.authority + "/run/" + goldenOwnerHash + "/" + tc.want
			if metadata.Ref.URI != wantURI {
				t.Fatalf("URI=%s want %s", metadata.Ref.URI, wantURI)
			}
		})
	}
}
