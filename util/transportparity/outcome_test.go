package transportparity

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	svcerr "github.com/hollis-labs/libs/util/svcerr"
)

func TestAssertSameOutcome(t *testing.T) {
	notFound := FromError(svcerr.New(svcerr.CodeNotFound, "no such gate"), svcerr.CodeInternal, 500)
	permission := FromError(svcerr.New(svcerr.CodePermission, "scope missing"), svcerr.CodeInternal, 500)

	tests := []struct {
		name    string
		a, b    Outcome
		wantErr string // substring of the failure; "" means it must pass
	}{
		{"both ok, equal values", FromValue(map[string]any{"id": 1}), FromValue(struct {
			ID int `json:"id"`
		}{1}), ""},
		{"both ok, different values", FromValue(map[string]any{"id": 1}), FromValue(map[string]any{"id": 2}), "different values"},
		{"both fail, same category", notFound, FromError(errors.New("row not found"), svcerr.CodeNotFound, 404), ""},
		{"both fail, different category", notFound, permission, "disagree on the failure category"},
		{"ok versus failure", FromValue("x"), notFound, "disagree on success"},
		{"failure versus ok", notFound, FromValue("x"), "disagree on success"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			AssertSameOutcome(rec, tc.name, tc.a, tc.b)
			switch {
			case tc.wantErr == "" && rec.failed():
				t.Errorf("unexpected failure: %s", rec.text())
			case tc.wantErr != "" && !strings.Contains(rec.text(), tc.wantErr):
				t.Errorf("failure %q does not contain %q", rec.text(), tc.wantErr)
			}
		})
	}
}

// Status and wording differ between doors by design and must never fail a
// comparison.
func TestStatusAndDetailAreNeverCompared(t *testing.T) {
	a := Outcome{Category: svcerr.CodeNotFound, Status: 404, Detail: "HTTP 404 no such gate"}
	b := Outcome{Category: svcerr.CodeNotFound, Status: -32001, Detail: "gate not found (rpc)"}
	rec := &recorder{}
	AssertSameOutcome(rec, "status and detail", a, b)
	if rec.failed() {
		t.Errorf("status/detail were compared: %s", rec.text())
	}
}

// Two failures that both have no category agree on nothing; passing them would
// hide drift behind an unclassified error.
func TestUncategorizedFailuresAreRejected(t *testing.T) {
	a := FromError(errors.New("boom"), "", 0)
	b := FromError(errors.New("bang"), "", 0)
	rec := &recorder{}
	AssertSameOutcome(rec, "uncategorized", a, b)
	if !strings.Contains(rec.text(), "no category") {
		t.Errorf("got %q, want a complaint about the missing category", rec.text())
	}
}

func TestFromErrorDerivesThroughWrapping(t *testing.T) {
	err := fmt.Errorf("handler: %w", svcerr.New(svcerr.CodeConflict, "version clash"))
	o := FromError(err, svcerr.CodeInternal, 500)
	if o.OK || o.Category != svcerr.CodeConflict || o.Status != 409 {
		t.Errorf("got %+v, want conflict/409 derived from the wrapped *svcerr.Error", o)
	}
	if !strings.Contains(o.Detail, "version clash") {
		t.Errorf("Detail = %q", o.Detail)
	}
}

func TestFromErrorFallsBackForAPlainError(t *testing.T) {
	// The text says "not found" and must not be read as a category.
	o := FromError(errors.New("task not found"), svcerr.CodeInternal, 502)
	if o.Category != svcerr.CodeInternal || o.Status != 502 {
		t.Errorf("got %+v, want the fallbacks", o)
	}
}

func TestFromErrorNilIsAFailureNotAPanic(t *testing.T) {
	o := FromError(nil, svcerr.CodeInternal, 500)
	if o.OK || o.Detail != "<nil error>" {
		t.Errorf("got %+v", o)
	}
}

func TestOutcomeString(t *testing.T) {
	if got := FromValue(1).String(); got != "ok" {
		t.Errorf("got %q", got)
	}
	got := FromError(svcerr.New(svcerr.CodeNotFound, "m"), "", 0).String()
	if !strings.Contains(got, `category="not_found"`) || !strings.Contains(got, "status=404") {
		t.Errorf("got %q", got)
	}
}
