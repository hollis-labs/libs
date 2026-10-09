package tesseract_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	tesseract "github.com/hollis-labs/libs/util/tesseractclient"
	"github.com/hollis-labs/libs/util/tesseractclient/tesseracttest"
)

// serverFilterKeys is the wire vocabulary of the recall route's nested
// `filters` object, hand-copied from Tesseract so this module need not import
// the tesseract app. It mirrors memory.RecallFilters:
//
//	apps/tesseract/internal/memory/recall.go:85-209   the struct (only
//	  WorkstreamID and StateFilters carry JSON tags; every other key is its Go
//	  field name)
//	apps/tesseract/internal/contextapi/memory_handler.go:225-241   the wire
//	  wrapper, which DisallowUnknownFields and refuses workstream_id
//
// Verified against Tesseract at a7af190 (2026-09-19). When the server's set
// changes, update this list from those two places; the tests below then say
// what to do about the client.
var serverFilterKeys = []string{
	"DerivedFrom", "Statuses", "Tags", "ConfidenceMin", "Since", "Until",
	"SimilarityMin", "Domains", "FacetKinds", "FacetSources", "RelatedTo",
	"RelatedRelations", "PointerHealth", "state_filters",
}

// omittedOnPurpose are server keys RecallFilters does not model because no
// caller populates them (SimilarityMin also has a flat top-level peer that
// wins over it). Adding one is fine; add it to RecallFilters and delete it
// here in the same change.
var omittedOnPurpose = []string{
	"DerivedFrom", "SimilarityMin", "Domains", "FacetKinds", "FacetSources",
	"RelatedTo", "RelatedRelations", "PointerHealth", "state_filters",
}

func filterKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	typ := reflect.TypeFor[tesseract.RecallFilters]()
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("RecallFilters.%s has no explicit JSON key; the wire key must be spelled out", f.Name)
		}
		keys = append(keys, name)
	}
	return keys
}

func TestFiltersEncodeWithTheServersGoFieldNames(t *testing.T) {
	req := tesseract.RecallRequest{
		Namespaces: []string{"a/b"},
		Filters: tesseract.RecallFilters{
			Statuses: []string{"canonical"}, Tags: []string{"x"}, ConfidenceMin: 0.5,
			Since: "2026-01-01T00:00:00Z", Until: "2026-02-01T00:00:00Z",
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var filters map[string]any
	if err := json.Unmarshal(top["filters"], &filters); err != nil {
		t.Fatalf("no filters object in %s: %v", raw, err)
	}
	got := slices.Sorted(maps.Keys(filters))
	want := []string{"ConfidenceMin", "Since", "Statuses", "Tags", "Until"}
	if !slices.Equal(got, want) {
		t.Fatalf("filters keys = %v, want %v (Go field names, not snake_case)\nbody: %s", got, want, raw)
	}
	// The other top-level keys are snake_case.
	if _, ok := top["namespaces"]; !ok {
		t.Errorf("top-level keys = %v", slices.Sorted(maps.Keys(top)))
	}
}

func TestEmptyFiltersAreOmitted(t *testing.T) {
	raw, err := json.Marshal(tesseract.RecallRequest{Namespaces: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "filters") {
		t.Errorf("body = %s, want no filters object when none are set", raw)
	}
}

func TestRecallFiltersFieldSetMirrorsTheServer(t *testing.T) {
	got := filterKeys(t)
	for _, k := range got {
		if !slices.Contains(serverFilterKeys, k) {
			t.Errorf("RecallFilters sends %q, which the server's filters object does not have; the route would 400 (DisallowUnknownFields)", k)
		}
	}
	for _, k := range serverFilterKeys {
		if !slices.Contains(got, k) && !slices.Contains(omittedOnPurpose, k) {
			t.Errorf("server filter key %q is neither modeled nor listed in omittedOnPurpose", k)
		}
	}
	for _, k := range omittedOnPurpose {
		if slices.Contains(got, k) {
			t.Errorf("%q is modeled but still listed in omittedOnPurpose", k)
		}
		if !slices.Contains(serverFilterKeys, k) {
			t.Errorf("omittedOnPurpose lists %q, which is not a server key", k)
		}
	}
}

func TestLegacyFilterFieldsCannotBeSent(t *testing.T) {
	// Tangent's client carried Origins, a field the server never had. The
	// typed API must make it (and workstream_id, which the wire wrapper
	// refuses) unsendable, and the server must refuse it if forced.
	typ := reflect.TypeFor[tesseract.RecallFilters]()
	for _, banned := range []string{"Origins", "WorkstreamID", "workstream_id", "confidence_min", "statuses"} {
		if _, ok := typ.FieldByName(banned); ok {
			t.Errorf("RecallFilters has a %s field", banned)
		}
		if slices.Contains(filterKeys(t), banned) {
			t.Errorf("RecallFilters sends key %q", banned)
		}
	}

	fake := tesseracttest.New(t)
	for _, body := range []string{
		`{"namespaces":["a"],"filters":{"Origins":["x"]}}`,
		`{"namespaces":["a"],"filters":{"confidence_min":0.5}}`,
		`{"namespaces":["a"],"filters":{"workstream_id":"w"}}`,
	} {
		resp, err := http.Post(fake.URL()+"/v1/memory/recall", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: fake status = %d, want 400 like the real route", body, resp.StatusCode)
		}
	}
}

func TestFiltersTravelToTheServer(t *testing.T) {
	fake := tesseracttest.New(t, questions(1)...)
	_, err := tesseract.New(fake.URL(), "").Recall(ctx(t), tesseract.RecallRequest{
		Namespaces: []string{root + "/*"}, SearchMode: "lexical", Query: "Q",
		Filters: tesseract.RecallFilters{Statuses: []string{"canonical"}, ConfidenceMin: 0.1, Since: "2026-01-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("the fake (strict like the server) refused what the client sent: %v", err)
	}
	body := string(fake.Requests(tesseracttest.RouteRecall)[0].Body)
	if !strings.Contains(body, `"search_mode":"lexical"`) || !strings.Contains(body, `"filters":{"Statuses"`) {
		t.Errorf("body = %s", body)
	}
}
