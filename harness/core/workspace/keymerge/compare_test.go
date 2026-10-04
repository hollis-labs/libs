package keymerge_test

import (
	"errors"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/keymerge"
)

// TestCompareJSONForgivesLayoutAndMovedKeysAndNothingElse: a document matches
// when it is what a merge would write once both are laid out the same way, so a
// document laid out differently, or with a declared key moved, is the same
// document; every changed value, removed key, respelled number, duplicate key
// and ordinary unreadable document is a mismatch. The outer-white-space
// exception is pinned separately below.
func TestCompareJSONForgivesLayoutAndMovedKeysAndNothingElse(t *testing.T) {
	owned := []keymerge.KeyPath{p("fixture-model"), p("fixture-perms", "mode")}
	const desired = `{"fixture-model":"opus","fixture-perms":{"mode":"auto"}}`
	for _, tc := range []struct {
		name     string
		existing string
		match    bool
		outcome  keymerge.Outcome
		reason   keymerge.Reason
	}{
		{"the same document at rest", "{\n  \"fixture-model\": \"opus\",\n  \"fixture-perms\": {\n    \"mode\": \"auto\"\n  }\n}\n", true, keymerge.OutcomeMerged, ""},
		{"the same document compact, with no newline", desired, true, keymerge.OutcomeMerged, ""},
		{"the same document laid out some other way", "{\n\t\"fixture-model\": \"opus\",\n\t\"fixture-perms\": {\"mode\": \"auto\"}\n}", true, keymerge.OutcomeMerged, ""},
		{"a declared key moved", `{"fixture-perms":{"mode":"auto"},"fixture-model":"opus"}`, true, keymerge.OutcomeMerged, ""},
		{"a key the desired document never declared", `{"fixture-model":"opus","fixture-tui":"x","fixture-perms":{"mode":"auto"}}`, true, keymerge.OutcomeMerged, ""},
		{"a key inside a declared object that was never declared", `{"fixture-model":"opus","fixture-perms":{"allow":["x"],"mode":"auto"}}`, true, keymerge.OutcomeMerged, ""},
		{"a changed value", `{"fixture-model":"haiku","fixture-perms":{"mode":"auto"}}`, false, keymerge.OutcomeMerged, ""},
		{"a changed nested value", `{"fixture-model":"opus","fixture-perms":{"mode":"plan"}}`, false, keymerge.OutcomeMerged, ""},
		{"a removed key", `{"fixture-perms":{"mode":"auto"}}`, false, keymerge.OutcomeMerged, ""},
		{"a removed nested key", `{"fixture-model":"opus","fixture-perms":{}}`, false, keymerge.OutcomeMerged, ""},
		{"a respelled string", "{\"fixture-model\":\"" + "\\u006fpus" + "\",\"fixture-perms\":{\"mode\":\"auto\"}}", false, keymerge.OutcomeMerged, ""},
		{"nothing on disk", ``, false, keymerge.OutcomeExistingUnreadable, keymerge.ReasonEmpty},
		{"not JSON", "not a settings document\n", false, keymerge.OutcomeExistingUnreadable, keymerge.ReasonNotJSON},
		{"not an object", `["fixture-model"]`, false, keymerge.OutcomeExistingUnreadable, keymerge.ReasonNotObject},
		{"content after the object", "{\"fixture-model\":\"opus\"}\nand then some\n", false, keymerge.OutcomeExistingUnreadable, keymerge.ReasonTrailingContent},
		{"a key declared twice", `{"fixture-model":"opus","fixture-model":"haiku"}`, false, keymerge.OutcomeExistingUnreadable, keymerge.ReasonDuplicateKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keymerge.CompareJSON([]byte(desired), []byte(tc.existing), owned)
			if err != nil {
				t.Fatal(err)
			}
			if got.Match != tc.match || got.Outcome != tc.outcome || got.Reason != tc.reason {
				t.Errorf("CompareJSON = %+v, want match %v, outcome %q, reason %q", got, tc.match, tc.outcome, tc.reason)
			}
			if got.ExistingLen != len(tc.existing) {
				t.Errorf("ExistingLen = %d, want %d", got.ExistingLen, len(tc.existing))
			}
			merged := mergeJSON(t, desired, tc.existing, owned...)
			if got.WriteLen != len(merged.Document) {
				t.Errorf("WriteLen = %d, want the %d bytes a merge writes", got.WriteLen, len(merged.Document))
			}
		})
	}
}

// TestCompareJSONReportsOuterUnicodeSpaceAsUnreadableEvenWhenLayoutMatches
// pins the inherited comparison rule: layout trims some outer Unicode space
// that the JSON grammar rejects. The unreadable verdict remains observable even
// when the laid-out bytes match; a byte-order mark is not trimmed and does not
// get the same exception.
func TestCompareJSONReportsOuterUnicodeSpaceAsUnreadableEvenWhenLayoutMatches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing string
		match    bool
	}{
		{"non-breaking space", "\u00a0{}\u00a0", true},
		{"em space", "\u2003{}\u2003", true},
		{"byte-order mark", "\ufeff{}\ufeff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keymerge.CompareJSON([]byte(`{}`), []byte(tc.existing), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Match != tc.match || got.Outcome != keymerge.OutcomeExistingUnreadable || got.Reason != keymerge.ReasonNotJSON {
				t.Errorf("CompareJSON = %+v, want match %v with existing_unreadable/not_json", got, tc.match)
			}
		})
	}
}

// TestCompareJSONHonorsOwnership: a declared key the caller does not own is not
// the caller's to report, whether it differs or is missing.
func TestCompareJSONHonorsOwnership(t *testing.T) {
	const desired = `{"fixture-slot":"declared","fixture-mode":"auto"}`
	owned := []keymerge.KeyPath{p("fixture-mode")}
	for _, existing := range []string{
		`{"fixture-slot":"operator","fixture-mode":"auto"}`,
		`{"fixture-mode":"auto"}`,
	} {
		got, err := keymerge.CompareJSON([]byte(desired), []byte(existing), owned)
		if err != nil || !got.Match {
			t.Errorf("existing %s: CompareJSON = %+v, %v, want a match", existing, got, err)
		}
	}
	got, _ := keymerge.CompareJSON([]byte(desired), []byte(`{"fixture-slot":"operator","fixture-mode":"plan"}`), owned)
	if got.Match {
		t.Error("an owned key with a different value matched")
	}
}

// TestCompareJSONDesiredUnreadable: a desired document that cannot be read never
// matches what is on disk by being the same unreadable bytes' merge; it is
// reported as that.
func TestCompareJSONDesiredUnreadable(t *testing.T) {
	got, err := keymerge.CompareJSON([]byte(`[`), []byte(`{}`), nil)
	if err != nil || got.Match || got.Outcome != keymerge.OutcomeDesiredUnreadable || got.Reason != keymerge.ReasonNotJSON {
		t.Errorf("CompareJSON = %+v, %v, want a mismatch with desired_unreadable/not_json", got, err)
	}
}

// TestCompareRefusesUnusableOwnedPaths: the owned-path errors are not verdicts.
func TestCompareRefusesUnusableOwnedPaths(t *testing.T) {
	want := &keymerge.Error{Code: keymerge.CodeInvalidOwnedPaths}
	if _, err := keymerge.CompareJSON([]byte(`{"a":{"b":1}}`), []byte(`{}`), []keymerge.KeyPath{p("a")}); !errors.Is(err, want) {
		t.Errorf("CompareJSON error = %v, want %v", err, want)
	}
	if _, err := keymerge.CompareTOML([]byte("[a]\nb = 1\n"), nil, []keymerge.KeyPath{p("a")}); !errors.Is(err, want) {
		t.Errorf("CompareTOML error = %v, want %v", err, want)
	}
	if _, err := keymerge.CompareTOML([]byte("a = 1\n"), nil, []keymerge.KeyPath{{}}); !errors.Is(err, want) {
		t.Errorf("CompareTOML error = %v, want %v", err, want)
	}
}

// TestCompareTOML: both sides are normalized the way the encoder writes them,
// so what the encoder moves is forgiven and nothing else is.
func TestCompareTOML(t *testing.T) {
	owned := []keymerge.KeyPath{p("approval"), p("srv", "command"), p("srv", "args")}
	const desired = "approval = 'on-request'\n\n[srv]\nargs = ['one', 'two']\ncommand = 'run'\n"
	for _, tc := range []struct {
		name     string
		existing string
		match    bool
		outcome  keymerge.Outcome
	}{
		{"the same document", desired, true, keymerge.OutcomeMerged},
		{"comments and quoting differ", "# note\napproval = \"on-request\"\n[srv]\ncommand = \"run\"\nargs = [\"one\", \"two\"]\n", true, keymerge.OutcomeMerged},
		{"an inline table and a table are the same table", "approval = 'on-request'\nsrv = {command = 'run', args = ['one', 'two']}\n", true, keymerge.OutcomeMerged},
		{"a key the desired document never declared", "approval = 'on-request'\n[srv]\nargs = ['one', 'two']\ncommand = 'run'\nfixture_operator = true\n", true, keymerge.OutcomeMerged},
		{"a changed value", "approval = 'never'\n[srv]\nargs = ['one', 'two']\ncommand = 'run'\n", false, keymerge.OutcomeMerged},
		{"a changed array", "approval = 'on-request'\n[srv]\nargs = ['one']\ncommand = 'run'\n", false, keymerge.OutcomeMerged},
		{"a removed key", "approval = 'on-request'\n[srv]\ncommand = 'run'\n", false, keymerge.OutcomeMerged},
		{"a document that does not parse", "approval = [\n", false, keymerge.OutcomeExistingUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := keymerge.CompareTOML([]byte(desired), []byte(tc.existing), owned)
			if err != nil {
				t.Fatal(err)
			}
			if got.Match != tc.match || got.Outcome != tc.outcome {
				t.Errorf("CompareTOML = %+v, want match %v, outcome %q", got, tc.match, tc.outcome)
			}
			if tc.outcome == keymerge.OutcomeExistingUnreadable && got.Reason != keymerge.ReasonNotTOML {
				t.Errorf("reason = %q, want not_toml", got.Reason)
			}
			if got.ExistingLen != len(tc.existing) {
				t.Errorf("ExistingLen = %d, want %d", got.ExistingLen, len(tc.existing))
			}
			if tc.outcome == keymerge.OutcomeMerged {
				merged := mergeTOML(t, desired, tc.existing, owned...)
				if got.WriteLen != len(merged.Document) {
					t.Errorf("WriteLen = %d, want the %d bytes a merge writes", got.WriteLen, len(merged.Document))
				}
			}
		})
	}
	got, err := keymerge.CompareTOML([]byte("a = [\n"), []byte("a = 1\n"), nil)
	if err != nil || got.Match || got.Outcome != keymerge.OutcomeDesiredUnreadable || got.Reason != keymerge.ReasonNotTOML {
		t.Errorf("CompareTOML of an unreadable desired document = %+v, %v, want a mismatch with desired_unreadable/not_toml", got, err)
	}
}
