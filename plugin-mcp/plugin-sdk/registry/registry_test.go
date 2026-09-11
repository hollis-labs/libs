package registry

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// TestProtocolLockedAt1 pins the wire version.
//
// The registry contract has two views in two languages and is read by hosts
// and loaders that ship as separately versioned artifacts. The protocol
// number is how a mismatch announces itself instead of surfacing as a field
// that silently stopped being read, so changing it is a coordinated act and
// this test is the thing that makes it deliberate. The TypeScript side
// carries the matching pin.
func TestProtocolLockedAt1(t *testing.T) {
	if Protocol != 1 {
		t.Fatalf("Protocol = %d, want 1 — bump the TypeScript pin in the same change", Protocol)
	}
	if got := NewResponse().Protocol; got != Protocol {
		t.Errorf("NewResponse().Protocol = %d, want %d", got, Protocol)
	}
}

func TestNewResponseHasNonNilMaps(t *testing.T) {
	r := NewResponse()
	if r.Plugins == nil || r.Contributions == nil {
		t.Fatal("NewResponse must initialize both maps so a loader always sees both keys")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"protocol", "plugins", "contributions"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("empty response omits %q; the loader treats it as a stable dictionary", key)
		}
	}
}

// TestResponseRoundTrip is the per-type roundtrip this repo already applies to
// every wire message, for the same reason: the host and the browser are
// separately built, so a field that stops surviving a marshal/unmarshal cycle
// is a compatibility break rather than a local bug.
func TestResponseRoundTrip(t *testing.T) {
	meta, err := Meta(map[string]any{"slot": "nav-rail", "priority": 10})
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	want := NewResponse()
	want.Plugins["acme.widgets"] = Plugin{
		BundleURL:     "/api/plugins/acme.widgets/ui/index.js",
		StylesheetURL: "/api/plugins/acme.widgets/ui/index.css",
		BundleVersion: "1757600000000",
		Runtime:       &Runtime{Name: "react", Version: "^19.0.0"},
	}
	want.Set("envelope", "acme.report", Contribution{PluginID: "acme.widgets", Export: "ReportView"})
	want.Set("slot", "acme.nav", Contribution{PluginID: "acme.widgets", Export: "AcmeNavPage", Meta: meta})

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("roundtrip changed the response:\n want %+v\n  got %+v", want, got)
	}
}

// TestMetaSurvivesOpaquely is the structural guarantee that keeps a host's
// taxonomy out of this package: whatever a host puts in Meta comes back
// unread and unreshaped, including shapes this package has no types for.
func TestMetaSurvivesOpaquely(t *testing.T) {
	meta := json.RawMessage(`{"nested":{"deep":[1,2,{"x":null}]},"unicode":"café"}`)
	r := NewResponse()
	r.Plugins["p"] = Plugin{BundleURL: "/p.js"}
	r.Set("whatever", "k", Contribution{PluginID: "p", Export: "E", Meta: meta})

	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Response
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var wantVal, gotVal any
	if err := json.Unmarshal(meta, &wantVal); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(got.Contributions["whatever"]["k"].Meta, &gotVal); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if !reflect.DeepEqual(wantVal, gotVal) {
		t.Errorf("meta changed across the wire:\n want %v\n  got %v", wantVal, gotVal)
	}
}

func TestPluginOmitsEmptyOptionalFields(t *testing.T) {
	raw, err := json.Marshal(Plugin{BundleURL: "/p.js"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(raw), `{"bundle_url":"/p.js"}`; got != want {
		t.Errorf("Plugin marshalled as %s, want %s", got, want)
	}
}

func TestValidateAcceptsAWellFormedResponse(t *testing.T) {
	r := NewResponse()
	r.Plugins["p"] = Plugin{BundleURL: "/p.js"}
	r.Set("envelope", "k", Contribution{PluginID: "p", Export: "E"})
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A plugin describing no browser half is valid. Its contributions are
// attributable and unresolved, which is a state the loader reports.
func TestValidateAcceptsAPluginWithNoBundle(t *testing.T) {
	r := NewResponse()
	r.Plugins["p"] = Plugin{}
	r.Set("envelope", "k", Contribution{PluginID: "p", Export: "E"})
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate rejected a bundle-less plugin: %v", err)
	}
}

func TestValidateRejections(t *testing.T) {
	newValid := func() Response {
		r := NewResponse()
		r.Plugins["p"] = Plugin{BundleURL: "/p.js"}
		r.Set("envelope", "k", Contribution{PluginID: "p", Export: "E"})
		return r
	}

	cases := []struct {
		name    string
		mutate  func(Response) Response
		wantErr error
	}{
		{
			name:    "protocol from a build we do not speak",
			mutate:  func(r Response) Response { r.Protocol = Protocol + 1; return r },
			wantErr: ErrProtocol,
		},
		{
			name:    "contribution with no plugin_id",
			mutate:  func(r Response) Response { r.Set("envelope", "k", Contribution{Export: "E"}); return r },
			wantErr: ErrInvalidContribution,
		},
		{
			// The defect this replaces: an export name inferred from an
			// identifier rather than declared. An empty export is a host that
			// dropped the field, and the loader must not guess at one.
			name:    "contribution with no export",
			mutate:  func(r Response) Response { r.Set("envelope", "k", Contribution{PluginID: "p"}); return r },
			wantErr: ErrInvalidContribution,
		},
		{
			name:    "empty contribution key",
			mutate:  func(r Response) Response { r.Set("envelope", "", Contribution{PluginID: "p", Export: "E"}); return r },
			wantErr: ErrInvalidContribution,
		},
		{
			name: "empty contribution kind",
			mutate: func(r Response) Response {
				r.Set("", "k", Contribution{PluginID: "p", Export: "E"})
				return r
			},
			wantErr: ErrInvalidContribution,
		},
		{
			name: "malformed meta",
			mutate: func(r Response) Response {
				r.Set("envelope", "k", Contribution{PluginID: "p", Export: "E", Meta: json.RawMessage(`{nope`)})
				return r
			},
			wantErr: ErrInvalidContribution,
		},
		{
			// Unresolvable by construction. The loader's only honest response
			// is to drop it, so the host finds out at serve time instead.
			name: "contribution naming a plugin the response does not describe",
			mutate: func(r Response) Response {
				r.Set("envelope", "k", Contribution{PluginID: "ghost", Export: "E"})
				return r
			},
			wantErr: ErrUnknownPlugin,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mutate(newValid()).Validate()
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() error = %v, want one wrapping %v", err, tc.wantErr)
			}
		})
	}
}

func TestSetCreatesTheKindMap(t *testing.T) {
	r := NewResponse()
	r.Set("newkind", "k", Contribution{PluginID: "p", Export: "E"})
	if got := r.Contributions["newkind"]["k"].Export; got != "E" {
		t.Errorf("Set did not record the contribution, got export %q", got)
	}
}

func TestMetaRejectsUnmarshalableValues(t *testing.T) {
	if _, err := Meta(make(chan int)); err == nil {
		t.Error("Meta accepted a value json cannot marshal")
	}
}
