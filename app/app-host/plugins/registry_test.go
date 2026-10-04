package plugins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/registry"
)

func fixtureRegistry(generation string, revision uint64, body []byte) (registry.Response, map[string]Bundle) {
	r := registry.NewResponse("test-host", revision)
	r.Kinds["widget"] = registry.KindDescriptor{SchemaVersion: 1, MetadataSchema: json.RawMessage(`{}`), Representations: []registry.Representation{registry.Component}, Regions: []string{"dashboard"}, RequiredCapabilities: []string{}}
	r.Regions["dashboard"] = registry.RegionDescriptor{Kinds: []string{"widget"}, Representations: []registry.Representation{registry.Component}, ContextSchema: json.RawMessage(`{}`), Ordering: "manifest"}
	r.Plugins["ops"] = registry.Plugin{OwnerGeneration: generation, BundleURL: "/plugins/ops/" + generation + "/bundle.js", BundleVersion: registry.BundleDigest(body)}
	_ = r.Set(registry.Contribution{Status: registry.StatusAccepted, OwnerID: "ops", OwnerGeneration: generation, LocalKey: "summary", Kind: "widget", SchemaVersion: 1, Representation: registry.Component, Metadata: json.RawMessage(`{}`), Component: &registry.ComponentRef{Export: "Summary", Region: "dashboard"}})
	return r, map[string]Bundle{"ops": {JavaScript: body}}
}
func TestReviewedBundleReplacementAndRevocation(t *testing.T) {
	r, b := fixtureRegistry("g1", 1, []byte("export const Summary=()=>null"))
	delivery, err := NewDelivery(r, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) int {
		w := httptest.NewRecorder()
		delivery.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code
	}
	if get("/plugins/registry") != 200 || get("/plugins/ops/g1/bundle.js") != 200 {
		t.Fatal("missing real bundle")
	}
	malformed, bad := fixtureRegistry("g2", 2, []byte("new"))
	bad["ops"] = Bundle{JavaScript: []byte("tampered")}
	if err := delivery.Replace(malformed, bad, nil); err == nil {
		t.Fatal("accepted tampered bytes")
	}
	if get("/plugins/ops/g1/bundle.js") != 200 {
		t.Fatal("failed replacement lost current authority")
	}
	mutated, changed := fixtureRegistry("g1", 2, []byte("changed"))
	if err := delivery.Replace(mutated, changed, nil); err == nil {
		t.Fatal("mutated generation")
	}
	replacement, bytes := fixtureRegistry("g2", 2, []byte("replacement"))
	if err := delivery.Replace(replacement, bytes, nil); err != nil {
		t.Fatal(err)
	}
	if get("/plugins/ops/g1/bundle.js") != 404 || get("/plugins/ops/g2/bundle.js") != 200 {
		t.Fatal("stale bundle authority")
	}
	empty := registry.NewResponse("test-host", 3)
	if err := delivery.Replace(empty, nil, nil); err != nil {
		t.Fatal(err)
	}
	if get("/plugins/ops/g2/bundle.js") != 404 {
		t.Fatal("unload retained bundle")
	}
	if err := delivery.Replace(r, b, nil); err == nil {
		t.Fatal("accepted rollback")
	}
}
func TestRequiredUnknownKindRefusesAdmission(t *testing.T) {
	r, b := fixtureRegistry("g1", 1, []byte("fixture"))
	c := r.Contributions["widget"]["ops/summary"]
	c.Kind = "unknown"
	c.Required = true
	r.Contributions = map[string]map[string]registry.Contribution{"unknown": {"ops/summary": c}}
	if _, err := NewDelivery(r, b, nil); err == nil {
		t.Fatal("required unknown contribution accepted")
	}
}

type blockingWriter struct{ entered, release chan struct{} }

func (w blockingWriter) Header() http.Header { return http.Header{} }
func (w blockingWriter) WriteHeader(int)     {}
func (w blockingWriter) Write(b []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(b), nil
}
func TestSlowDeliveryCannotBlockRevoke(t *testing.T) {
	r, b := fixtureRegistry("g1", 1, []byte("fixture"))
	d, err := NewDelivery(r, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := blockingWriter{make(chan struct{}), make(chan struct{})}
	finished := make(chan struct{})
	go func() { d.ServeHTTP(w, httptest.NewRequest("GET", "/plugins/ops/g1/bundle.js", nil)); close(finished) }()
	<-w.entered
	defer close(w.release)
	replaced := make(chan error, 1)
	go func() { replaced <- d.Replace(registry.NewResponse("test-host", 2), nil, nil) }()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow writer blocks revocation")
	}
}

func TestGenerationIntegritySeparatesScriptAndStylesheetBytes(t *testing.T) {
	r, b := fixtureRegistry("g1", 1, []byte("ab"))
	p := r.Plugins["ops"]
	p.StylesheetURL = "/plugins/ops/g1/style.css"
	r.Plugins["ops"] = p
	b["ops"] = Bundle{JavaScript: []byte("ab"), Stylesheet: []byte("c")}
	d, err := NewDelivery(r, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Revision = 2
	p.BundleVersion = registry.BundleDigest([]byte("a"))
	r.Plugins["ops"] = p
	if err := d.Replace(r, map[string]Bundle{"ops": {JavaScript: []byte("a"), Stylesheet: []byte("bc")}}, nil); err == nil {
		t.Fatal("accepted changed artifacts with identical concatenated bytes")
	}
}

func TestDotSegmentGenerationCannotBeAdmitted(t *testing.T) {
	for _, generation := range []string{".", ".."} {
		r, b := fixtureRegistry(generation, 1, []byte("fixture"))
		if _, err := NewDelivery(r, b, nil); err == nil {
			t.Fatalf("accepted dot segment generation %q", generation)
		}
	}
	r, b := fixtureRegistry("g1", 1, []byte("fixture"))
	p := r.Plugins["ops"]
	p.BundleURL = "/plugins/../g1/bundle.js"
	r.Plugins = map[string]registry.Plugin{"..": p}
	c := r.Contributions["widget"]["ops/summary"]
	c.OwnerID = ".."
	r.Contributions = map[string]map[string]registry.Contribution{"widget": {"../summary": c}}
	if _, err := NewDelivery(r, map[string]Bundle{"..": b["ops"]}, nil); err == nil {
		t.Fatal("accepted dot segment owner")
	}
}
