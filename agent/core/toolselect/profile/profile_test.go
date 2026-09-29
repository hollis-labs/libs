package profile_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hollis-labs/go-toolselect/profile"
)

func ptr(b bool) *bool { return &b }

func cat() profile.Catalog {
	return profile.Catalog{Servers: []profile.Server{
		{ID: "torque", Tools: []profile.Tool{
			{Name: "torque_task_list", ReadOnly: ptr(true), Destructive: ptr(false)},
			{Name: "torque_task_create"},
			{Name: "torque_task_delete", ReadOnly: ptr(false), Destructive: ptr(true)},
		}},
		{ID: "files", Tools: []profile.Tool{
			{Name: "files_write", ReadOnly: ptr(false)},
			{Name: "files_read", Title: "Read", ReadOnly: ptr(true)},
		}},
	}}
}

func visNames(v []profile.VisibleTool) []string {
	out := make([]string, len(v))
	for i, t := range v {
		out[i] = t.Name
	}
	return out
}

func hiddenMap(h []profile.HiddenReason) map[string]profile.HiddenCause {
	m := map[string]profile.HiddenCause{}
	for _, r := range h {
		m[r.Name] = r.Reason
	}
	return m
}

// Default posture: the zero Profile shows everything and hides nothing.
func TestZeroProfileShowsEverything(t *testing.T) {
	vis, hid, err := profile.Evaluate(cat(), profile.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hid) != 0 {
		t.Fatalf("hidden: %v", hid)
	}
	want := []string{"torque_task_create", "torque_task_delete", "torque_task_list", "files_read", "files_write"}
	if !reflect.DeepEqual(visNames(vis), want) {
		t.Fatalf("got %v, want %v", visNames(vis), want)
	}
	if vis[3].Title != "Read" || vis[3].Server != "files" {
		t.Errorf("fields not carried: %+v", vis[3])
	}
}

func TestEmptyCatalogAndZeroProfile(t *testing.T) {
	vis, hid, err := profile.Evaluate(profile.Catalog{}, profile.Profile{})
	if err != nil || len(vis) != 0 || len(hid) != 0 {
		t.Fatalf("%v %v %v", vis, hid, err)
	}
}

func TestUnknownServerIsAnError(t *testing.T) {
	for _, servers := range []map[string]bool{
		{"ghost": true}, {"ghost": false}, {"torque": true, "nil": true, "tether": false},
	} {
		vis, hid, err := profile.Evaluate(cat(), profile.Profile{Servers: servers})
		if !errors.Is(err, profile.ErrUnknownServer) {
			t.Fatalf("%v: err = %v", servers, err)
		}
		if vis != nil || hid != nil {
			t.Errorf("outputs not nil on error")
		}
	}
	_, _, err := profile.Evaluate(cat(), profile.Profile{Servers: map[string]bool{"tether": true, "nil": true}})
	if err == nil || err.Error() != `toolselect/profile: unknown server id in profile: "nil", "tether"` {
		t.Errorf("message = %v", err)
	}
}

func TestServersEnableDisable(t *testing.T) {
	vis, hid, err := profile.Evaluate(cat(), profile.Profile{Servers: map[string]bool{"files": false, "torque": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vis) != 3 || len(hid) != 2 {
		t.Fatalf("%v %v", visNames(vis), hid)
	}
	for _, h := range hid {
		if h.Server != "files" || h.Reason != profile.CauseServerDisabled {
			t.Errorf("%+v", h)
		}
	}
}

// Precedence: server_disabled first, then deny > read_only > allow.
func TestPrecedenceDenyReadOnlyAllow(t *testing.T) {
	// files_write: ReadOnly=false. torque_task_create: hint nil.
	tests := []struct {
		name string
		p    profile.Profile
		tool string
		want profile.HiddenCause // "" = visible
	}{
		{"deny+readonly+allow: deny wins", profile.Profile{ToolsDeny: []string{"files_write"}, ReadOnly: true, ToolsAllow: []string{"files_*"}}, "files_write", profile.CauseDenied},
		{"deny+readonly: deny wins", profile.Profile{ToolsDeny: []string{"files_write"}, ReadOnly: true}, "files_write", profile.CauseDenied},
		{"deny+allow: deny wins", profile.Profile{ToolsDeny: []string{"files_*"}, ToolsAllow: []string{"files_read"}}, "files_read", profile.CauseDenied},
		{"readonly+allow: read_only wins", profile.Profile{ReadOnly: true, ToolsAllow: []string{"files_write"}}, "files_write", profile.CauseReadOnly},
		{"readonly alone", profile.Profile{ReadOnly: true}, "files_write", profile.CauseReadOnly},
		{"allow alone excludes", profile.Profile{ToolsAllow: []string{"files_read"}}, "files_write", profile.CauseNotAllowed},
		{"allow alone includes", profile.Profile{ToolsAllow: []string{"files_*"}}, "files_write", ""},
		{"readonly passes proven read-only, allow matches", profile.Profile{ReadOnly: true, ToolsAllow: []string{"files_read"}}, "files_read", ""},
		{"readonly hides nil hint", profile.Profile{ReadOnly: true}, "torque_task_create", profile.CauseReadOnly},
		{"deny does not touch others", profile.Profile{ToolsDeny: []string{"files_write"}}, "files_read", ""},
		{"disabled server beats deny", profile.Profile{Servers: map[string]bool{"files": false}, ToolsDeny: []string{"files_*"}}, "files_read", profile.CauseServerDisabled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vis, hid, err := profile.Evaluate(cat(), tc.p)
			if err != nil {
				t.Fatal(err)
			}
			got := hiddenMap(hid)[tc.tool]
			if got != tc.want {
				t.Errorf("hidden cause = %q, want %q", got, tc.want)
			}
			inVisible := false
			for _, v := range vis {
				inVisible = inVisible || v.Name == tc.tool
			}
			if inVisible != (tc.want == "") {
				t.Errorf("visible = %v, want %v", inVisible, tc.want == "")
			}
			if len(vis)+len(hid) != 5 {
				t.Errorf("tools lost: %d visible + %d hidden", len(vis), len(hid))
			}
		})
	}
}

func TestReadOnlyHidesNilHint(t *testing.T) {
	vis, hid, err := profile.Evaluate(cat(), profile.Profile{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"torque_task_list", "files_read"}; !reflect.DeepEqual(visNames(vis), want) {
		t.Fatalf("visible %v", visNames(vis))
	}
	if hiddenMap(hid)["torque_task_create"] != profile.CauseReadOnly {
		t.Errorf("nil hint not hidden as read_only: %v", hid)
	}
}

func TestOutputOrderPinnedThenServerThenName(t *testing.T) {
	p := profile.Profile{Order: []string{"files_write", "ghost", "torque_task_list"}}
	vis, _, err := profile.Evaluate(cat(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"files_write", "torque_task_list", "torque_task_create", "torque_task_delete", "files_read"}
	if !reflect.DeepEqual(visNames(vis), want) {
		t.Fatalf("got %v, want %v", visNames(vis), want)
	}
	// Server order follows the catalog, not the alphabet.
	rev := profile.Catalog{Servers: []profile.Server{cat().Servers[1], cat().Servers[0]}}
	vis, _, _ = profile.Evaluate(rev, profile.Profile{})
	if got := visNames(vis); got[0] != "files_read" || got[len(got)-1] != "torque_task_list" {
		t.Errorf("server order not honored: %v", got)
	}
	// A pinned name that is hidden is not resurrected.
	vis, hid, _ := profile.Evaluate(cat(), profile.Profile{Order: []string{"files_write"}, ToolsDeny: []string{"files_write"}})
	for _, v := range vis {
		if v.Name == "files_write" {
			t.Error("denied tool visible via Order")
		}
	}
	if hiddenMap(hid)["files_write"] != profile.CauseDenied {
		t.Error("not reported hidden")
	}
}

func TestEvaluateIsDeterministic(t *testing.T) {
	p := profile.Profile{Order: []string{"files_read"}, ToolsDeny: []string{"*_delete"}, AlwaysLoad: []string{"torque_task_list"}}
	wantV, wantH, err := profile.Evaluate(cat(), p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		v, h, _ := profile.Evaluate(cat(), p)
		if !reflect.DeepEqual(v, wantV) || !reflect.DeepEqual(h, wantH) {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestAlwaysLoadIsInformationalOnly(t *testing.T) {
	base, _, _ := profile.Evaluate(cat(), profile.Profile{})
	vis, _, err := profile.Evaluate(cat(), profile.Profile{AlwaysLoad: []string{"files_write", "ghost"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(visNames(vis), visNames(base)) {
		t.Errorf("AlwaysLoad moved tools: %v", visNames(vis))
	}
	for _, v := range vis {
		if v.AlwaysLoad != (v.Name == "files_write") {
			t.Errorf("%s AlwaysLoad = %v", v.Name, v.AlwaysLoad)
		}
	}
}

// Regression pin against mcp-host's boolHint, which defaults an undeclared
// hint to false: a nil hint must stay nil on every code path.
func TestAnnotationPassthroughNilStaysNil(t *testing.T) {
	c := profile.Catalog{Servers: []profile.Server{{ID: "s", Tools: []profile.Tool{
		{Name: "s_undeclared"},
		{Name: "s_true", ReadOnly: ptr(true), Destructive: ptr(true)},
		{Name: "s_false", ReadOnly: ptr(false), Destructive: ptr(false)},
	}}}}
	profiles := map[string]profile.Profile{
		"zero":       {},
		"allow":      {ToolsAllow: []string{"s_*"}},
		"deny-other": {ToolsDeny: []string{"nope"}},
		"order":      {Order: []string{"s_undeclared"}},
		"alwaysload": {AlwaysLoad: []string{"s_undeclared"}},
		"server-on":  {Servers: map[string]bool{"s": true}},
	}
	for name, p := range profiles {
		vis, _, err := profile.Evaluate(c, p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(vis) != 3 {
			t.Fatalf("%s: %v", name, visNames(vis))
		}
		for _, v := range vis {
			switch v.Name {
			case "s_undeclared":
				if v.ReadOnly != nil || v.Destructive != nil {
					t.Errorf("%s: nil hint became non-nil: %+v", name, v)
				}
			case "s_true":
				if v.ReadOnly == nil || !*v.ReadOnly || v.Destructive == nil || !*v.Destructive {
					t.Errorf("%s: %+v", name, v)
				}
			case "s_false":
				if v.ReadOnly == nil || *v.ReadOnly || v.Destructive == nil || *v.Destructive {
					t.Errorf("%s: %+v", name, v)
				}
			}
		}
	}
	// Hidden-then-visible paths: ReadOnly filter keeps declared hints intact too.
	vis, _, _ := profile.Evaluate(c, profile.Profile{ReadOnly: true})
	if len(vis) != 1 || vis[0].Name != "s_true" || vis[0].Destructive == nil || !*vis[0].Destructive {
		t.Errorf("read-only path: %+v", vis)
	}
}

func TestGlobsMatchNamesAcrossServersAndNoMatchIsNoOp(t *testing.T) {
	vis, hid, err := profile.Evaluate(cat(), profile.Profile{ToolsAllow: []string{"ghost_*"}})
	if err != nil {
		t.Fatalf("zero-match glob must not error: %v", err)
	}
	if len(vis) != 0 || len(hid) != 5 {
		t.Fatalf("%v %v", vis, hid)
	}
	for _, h := range hid {
		if h.Reason != profile.CauseNotAllowed {
			t.Errorf("%+v", h)
		}
	}
	vis, _, _ = profile.Evaluate(cat(), profile.Profile{ToolsDeny: []string{"ghost_*"}})
	if len(vis) != 5 {
		t.Errorf("zero-match deny hid tools: %v", visNames(vis))
	}
}

func TestMalformedPatternIsAnError(t *testing.T) {
	for _, p := range []profile.Profile{
		{ToolsAllow: []string{"["}}, {ToolsDeny: []string{"a[b"}}, {ToolsDeny: []string{"\\"}},
	} {
		if _, _, err := profile.Evaluate(cat(), p); !errors.Is(err, profile.ErrBadPattern) {
			t.Errorf("%+v: err = %v", p, err)
		}
	}
}

func TestInstructionsAndIDArePassthrough(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	if _, _, err := profile.Evaluate(cat(), profile.Profile{ID: "p", Instructions: string(long)}); err != nil {
		t.Fatalf("Instructions must not be validated: %v", err)
	}
}

func BenchmarkEvaluate(b *testing.B) {
	var s profile.Server
	s.ID = "s"
	for i := 0; i < 500; i++ {
		s.Tools = append(s.Tools, profile.Tool{Name: fmt.Sprintf("s_tool_%03d", i)})
	}
	c := profile.Catalog{Servers: []profile.Server{s}}
	p := profile.Profile{ToolsAllow: []string{"s_tool_*"}, ToolsDeny: []string{"*_9*"}}
	for b.Loop() {
		_, _, _ = profile.Evaluate(c, p)
	}
}
