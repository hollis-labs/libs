package conformance

import (
	"context"
	"github.com/hollis-labs/substrate/mesh"
	"testing"
)

type hiddenSupport struct{ responseError error }

func (p hiddenSupport) Describe(context.Context) (mesh.Descriptor, error) {
	return mesh.Descriptor{}, nil
}
func (p hiddenSupport) Invoke(_ context.Context, r mesh.Request) (mesh.Response, error) {
	if r.Verb == mesh.Handoff {
		return mesh.Response{}, p.responseError
	}
	return mesh.Response{}, mesh.NewError(mesh.ErrorUnsupported, "unclaimed")
}
func TestReverseClaimsRejectHiddenSupportAndWrongRefusal(t *testing.T) {
	for _, err := range []error{nil, mesh.NewError(mesh.ErrorNotFound, "target"), mesh.NewError(mesh.ErrorDenied, "actor")} {
		if checkUnclaimed(context.Background(), hiddenSupport{err}, mesh.Descriptor{}) == nil {
			t.Fatalf("accepted unclaimed operation result %v", err)
		}
	}
	if err := checkUnclaimed(context.Background(), hiddenSupport{mesh.NewError(mesh.ErrorUnsupported, "handoff")}, mesh.Descriptor{}); err != nil {
		t.Fatal(err)
	}
}
