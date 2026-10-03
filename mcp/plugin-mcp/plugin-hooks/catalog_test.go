package pluginhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCatalogJSON(t *testing.T) {
	a, f := definition("event.ready", Action), definition("value.change", Filter)
	a.Deprecated = &Deprecation{Since: "0.2.0", Replacement: "event.updated", Reason: "payload changed", Removal: "0.4.0"}
	a.Budget = time.Second + time.Microsecond
	a.ValidateInput = func(json.RawMessage) error { t.Fatal("introspection executed validator"); return nil }
	c := Catalog{Version: "1", Definitions: []Definition{f, a}}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var doc CatalogDocument
	if decodeErr := json.Unmarshal(b, &doc); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if doc.CatalogVersion != "1" || len(doc.Definitions) != 2 || doc.Definitions[0].Name != a.Name {
		t.Fatalf("%s", b)
	}
	d := doc.Definitions[0]
	if d.BudgetMS != 1000.001 || d.HandlerTimeoutMS != 500 || d.RemoteOK == nil || *d.RemoteOK || *d.Deprecated != *a.Deprecated {
		t.Fatalf("%+v", d)
	}
	if string(d.InputSchema) != `{"type":"object"}` || len(d.MutablePaths) != 0 || doc.Definitions[1].MutablePaths[0] != "/visible" {
		t.Fatalf("%s", b)
	}
	if bytes.Contains(b, []byte("Validate")) || bytes.Contains(b, []byte("input_schema\":\"")) {
		t.Fatalf("functions or encoded schema: %s", b)
	}
	direct, err := json.Marshal(f)
	if err != nil || !bytes.Contains(direct, []byte(`"output_schema":{"type":"object"}`)) {
		t.Fatalf("%s: %v", direct, err)
	}
	// Document mutation cannot alter executable policy, including pointer fields.
	snapshot := c.Document()
	snapshot.Definitions[0].InputSchema[0] = 'x'
	*snapshot.Definitions[0].RemoteOK = true
	snapshot.Definitions[0].Deprecated.Reason = "changed"
	snapshot.Definitions[0].Views["limited"][0] = "/secret"
	if a.InputSchema[0] != '{' || *a.RemoteOK || a.Deprecated.Reason == "changed" || a.Views["limited"][0] != "/visible" {
		t.Fatal("document aliases catalog")
	}
	empty, err := json.Marshal(Catalog{Version: "1"})
	if err != nil || string(empty) != `{"catalog_version":"1","definitions":[]}` {
		t.Fatalf("%s %v", empty, err)
	}
}

func TestCustomCatalogDeclarations(t *testing.T) {
	name := "plugin.assigned_owner.document.updated"
	d := definition(name, Action)
	d.OwnerNamespace = "assigned_owner"
	r, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{d}})
	if err != nil {
		t.Fatal(err)
	}
	// The namespace alone grants nothing. A trusted host may explicitly permit
	// another owner's subscription; the declaring namespace is not its identity.
	s, err := r.NewScope(ScopeConfig{Owner: "subscriber", Generation: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, checkErr := s.ValidateRegistration(name, "observe", Action, Options{}); !errors.Is(checkErr, ErrUnauthorized) {
		t.Fatal(checkErr)
	}
	allowed, err := r.NewScope(ScopeConfig{Owner: "subscriber", Generation: "2", Hooks: []string{name}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allowed.AddAction(name, "observe", Options{}, func(context.Context, Invocation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Definition){
		func(d *Definition) { d.OwnerNamespace = "" },
		func(d *Definition) { d.OwnerNamespace = "other" },
		func(d *Definition) { d.Name = "plugin.assigned_owner.event" },
		func(d *Definition) { d.Name = "plugin.assigned_owner.document.updated.extra" },
		func(d *Definition) { d.InputSchema = nil },
		func(d *Definition) { d.ValidateInput = nil },
		func(d *Definition) { d.MaxHandlers = 0 },
		func(d *Definition) { d.Name = "core.updated" },
	} {
		bad := copyDefinition(d)
		mutate(&bad)
		if _, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{bad}}); !errors.Is(err, ErrInvalidDefinition) {
			t.Fatal(bad, err)
		}
	}
	if _, err := r.NewScope(ScopeConfig{Owner: "unknown", Generation: "1", Hooks: []string{"plugin.assigned_owner.missing.event"}}); !errors.Is(err, ErrUnknownHook) {
		t.Fatal(err)
	}
}

func TestDeprecationRegistrationAndRemoval(t *testing.T) {
	old := definition("event.ready", Action)
	old.Deprecated = &Deprecation{Since: "0.2.0", Replacement: "event.updated", Reason: "new payload", Removal: "0.4.0"}
	replacement := definition("event.updated", Action)
	r, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{old, replacement}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.NewScope(ScopeConfig{Owner: "observer", Generation: "1", Hooks: []string{old.Name, replacement.Name}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ValidateRegistration(old.Name, "legacy", Action, Options{})
	if err != nil || len(v.Warnings) != 1 || v.Warnings[0].Hook != old.Name || v.Warnings[0].Deprecation != *old.Deprecated {
		t.Fatalf("%+v %v", v, err)
	}
	v.Warnings[0].Deprecation.Reason = "caller edit"
	if _, addErr := s.AddAction(old.Name, "legacy", Options{}, func(context.Context, Invocation) error { return nil }); addErr != nil {
		t.Fatal(addErr)
	}
	regs := s.Registrations()
	if len(regs) != 1 || regs[0].Hook != old.Name || regs[0].Warnings[0].Deprecation.Reason != old.Deprecated.Reason || len(r.snapshot(replacement.Name)) != 0 {
		t.Fatal("redirected or warning lost")
	}
	regs[0].Warnings[0].Deprecation.Reason = "snapshot edit"
	if s.Registrations()[0].Warnings[0].Deprecation.Reason != old.Deprecated.Reason {
		t.Fatal("warning aliases registry")
	}
	v, err = s.ValidateRegistration(replacement.Name, "fresh", Action, Options{})
	if err != nil || len(v.Warnings) != 0 {
		t.Fatalf("%+v %v", v, err)
	}
	// At the removal release the publisher supplies only supported declarations.
	removed, err := NewRegistry(Catalog{Version: "2", Definitions: []Definition{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := removed.NewScope(ScopeConfig{Owner: "observer", Generation: "1", Hooks: []string{replacement.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.AddAction(old.Name, "old", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnknownHook) || !strings.Contains(err.Error(), old.Name) {
		t.Fatal(err)
	}
	if _, err := removed.NewScope(ScopeConfig{Owner: "observer", Generation: "2", Hooks: []string{old.Name}}); !errors.Is(err, ErrUnknownHook) || !strings.Contains(err.Error(), old.Name) {
		t.Fatal(err)
	}
	for _, p := range []*Deprecation{{Reason: "missing"}, {Since: "1", Reason: "x", Removal: "2", Replacement: old.Name}, {Since: "1", Reason: "x", Removal: "2", Replacement: "Stop"}} {
		bad := copyDefinition(old)
		bad.Deprecated = p
		if _, err := NewRegistry(Catalog{Version: "1", Definitions: []Definition{bad}}); !errors.Is(err, ErrInvalidDefinition) {
			t.Fatal(err)
		}
	}
}

func TestManifestPreflightPolicy(t *testing.T) {
	_, s := fixture(t)
	zero := time.Duration(0)
	for _, test := range []struct {
		hook, name string
		kind       Kind
		o          Options
		err        error
	}{
		{"event.ready", "valid", Action, Options{}, nil},
		{"value.change", "valid", Filter, Options{}, nil},
		{"event.ready", "", Action, Options{}, ErrInvalidOptions},
		{"event.ready", "wrong", Filter, Options{}, ErrInvalidOptions},
		{"absent.event", "unknown", Action, Options{}, ErrUnknownHook},
		{"event.ready", "options", Action, Options{Timeout: &zero}, ErrInvalidOptions},
		{"event.ready", "options", Action, Options{SchemaDigest: "wrong"}, ErrInvalidOptions},
		{"event.ready", "options", Action, Options{View: "absent"}, ErrInvalidOptions},
	} {
		v, err := s.ValidateRegistration(test.hook, test.name, test.kind, test.o)
		if !errors.Is(err, test.err) {
			t.Fatalf("%+v: %v", test, err)
		}
		if err == nil && v.Options.Timeout <= 0 {
			t.Fatal("unresolved policy")
		}
	}
	if len(s.Registrations()) != 0 {
		t.Fatal("preflight installed registration")
	}
	if err := s.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateRegistration("event.ready", "later", Action, Options{}); !errors.Is(err, ErrDisposed) {
		t.Fatal(err)
	}
}

func TestLifecycleAdapterTable(t *testing.T) {
	expected := map[string]string{
		"SessionStart": "session.start", "SessionEnd": "session.end", "UserPromptSubmit": "message.sending",
		"PreToolUse": "tool.executing", "PostToolUse": "tool.complete", "PermissionRequest": "permission.requesting",
		"PreCompact": "context.pre_compact", "PostCompact": "context.compacted", "SubagentStart": "subagent.start",
		"SubagentStop": "subagent.stopping", "Stop": "turn.stopping",
	}
	got := map[string]string{}
	_, s := fixture(t)
	for _, m := range AgentLifecycleMappings() {
		if _, exists := got[m.NativeEvent]; exists || m.Contract == "" {
			t.Fatal("duplicate or undocumented mapping", m)
		}
		got[m.NativeEvent] = m.Hook
		if _, err := s.AddAction(m.NativeEvent, "vendor", Options{}, func(context.Context, Invocation) error { return nil }); !errors.Is(err, ErrUnknownHook) {
			t.Fatal("registry accepted vendor alias", m, err)
		}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("lifecycle drift: %+v", got)
	}
	table := AgentLifecycleMappings()
	table[0].Hook = "changed"
	if AgentLifecycleMappings()[0].Hook == "changed" {
		t.Fatal("shared adapter table")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestCatalogMarkdown(t *testing.T) {
	c := Catalog{Version: "1", Definitions: []Definition{definition("value.change", Filter)}}
	var b bytes.Buffer
	if err := c.WriteMarkdown(&b); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"# Hook catalog 1", "## value.change", "input_schema", "output_schema", "mutable_paths", "schema_digest", "budget_ms"} {
		if !strings.Contains(b.String(), text) {
			t.Fatal("missing", text)
		}
	}
	if err := c.WriteMarkdown(brokenWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	c.Definitions[0].InputSchema = json.RawMessage(`{invalid`)
	if err := c.WriteMarkdown(&b); err == nil {
		t.Fatal("bad schema serialized")
	}
}
