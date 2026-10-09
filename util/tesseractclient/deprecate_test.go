package tesseract_test

import (
	"errors"
	"strings"
	"testing"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
	"github.com/hollis-labs/libs/util/tesseractclient/tesseracttest"
)

func TestDeprecateRetiresARevisionAndIsIdempotent(t *testing.T) {
	rev := tesseracttest.NewRevision("project/example/memory/decisions", "D-1")
	fake := tesseracttest.New(t, rev)
	c := tesseract.New(fake.URL(), "")

	for i := range 2 {
		if err := c.Deprecate(ctx(t), rev.RevisionID); err != nil {
			t.Fatalf("deprecate #%d: %v", i+1, err)
		}
	}
	reqs := fake.Requests(tesseracttest.RouteDeprecate)
	if len(reqs) != 2 || string(reqs[0].Body) != `{"revision_id":"`+rev.RevisionID+`"}` {
		t.Fatalf("requests = %+v, want two POSTs carrying only revision_id", reqs)
	}
	// Recall no longer returns it, but GetRevision still hydrates it.
	page, err := c.Recall(ctx(t), tesseract.RecallRequest{Namespaces: []string{rev.Namespace}})
	if err != nil || len(page.Results) != 0 {
		t.Fatalf("recall after deprecate: %v, %d results; want none", err, len(page.Results))
	}
	got, err := c.GetRevision(ctx(t), rev.RevisionID)
	if err != nil || got.Status != "deprecated" {
		t.Fatalf("GetRevision = %+v, %v; want the deprecated revision", got, err)
	}
}

func TestDeprecateUnknownRevisionIsNotFound(t *testing.T) {
	fake := tesseracttest.New(t)
	err := tesseract.New(fake.URL(), "").Deprecate(ctx(t), "nope")
	if !errors.Is(err, tesseract.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeprecateNeedsARevisionID(t *testing.T) {
	fake := tesseracttest.New(t)
	if err := tesseract.New(fake.URL(), "").Deprecate(ctx(t), ""); err == nil {
		t.Fatal("want an error for an empty revision id")
	}
	if fake.Calls(tesseracttest.RouteDeprecate) != 0 {
		t.Error("an empty id must be refused without a request")
	}
}

func TestGetRevisionHydratesByID(t *testing.T) {
	rev := tesseracttest.NewRevision(root+"/questions", "Q-1", tesseracttest.Tags("a", "b"))
	fake := tesseracttest.New(t, rev)
	got, err := tesseract.New(fake.URL(), "").GetRevision(ctx(t), rev.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RevisionID != rev.RevisionID || got.Payload.Body != rev.Payload.Body || len(got.Tags) != 2 {
		t.Errorf("got %+v, want %+v", got, rev)
	}
	if p := fake.Requests(tesseracttest.RouteRevision)[0].Path; p != "/v1/memory/revisions/"+rev.RevisionID {
		t.Errorf("path = %q", p)
	}
}

func TestGetRevisionEscapesTheID(t *testing.T) {
	fake := tesseracttest.New(t)
	_, err := tesseract.New(fake.URL(), "").GetRevision(ctx(t), "a b?c")
	if !errors.Is(err, tesseract.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound from the fake", err)
	}
	if q := fake.Requests(tesseracttest.RouteRevision)[0]; q.RawQuery != "" || !strings.HasSuffix(q.Path, "/a b?c") {
		t.Errorf("request = %+v, want the id kept in the path", q)
	}
}

func TestGetRevisionUnknownAndEmpty(t *testing.T) {
	fake := tesseracttest.New(t)
	c := tesseract.New(fake.URL(), "")
	if _, err := c.GetRevision(ctx(t), "missing"); !errors.Is(err, tesseract.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := c.GetRevision(ctx(t), ""); err == nil {
		t.Error("want an error for an empty revision id")
	}
}
