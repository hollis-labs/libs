package publication

import (
	"encoding/json"
	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
	"path/filepath"
	"strings"
	"testing"
)

func journalFixture(t *testing.T) Journal {
	t.Helper()
	key, _ := bootkey.Encode("urn:fixture:publication")
	parent := filepath.Join(t.TempDir(), key)
	identity := FileIdentity{Volume: "fixture-volume", Device: 1, Inode: 1}
	root := func(id, name string) Root {
		return Root{ID: id, Path: filepath.Join(parent, name), Owner: "fixture", Provenance: "fixture"}
	}
	p := Root{ID: "parent", Path: parent, Owner: "fixture", Provenance: "fixture", Identity: identity}
	current, candidate, aside := root("current", "current"), root("candidate", "candidate"), root("aside", "previous-operation")
	candidate.Identity = FileIdentity{Volume: identity.Volume, Device: 1, Inode: 2}
	ns := filepath.Join(filepath.Dir(parent), "control")
	return Journal{Version: Version, JournalID: "original-journal", Control: Root{ID: "control", Path: ns, Owner: "fixture", Provenance: "fixture", Identity: FileIdentity{Volume: "control-volume", Device: 2, Inode: 5}}, Origin: Origin{OperationID: "original-operation", InputDigest: strings.Repeat("a", 64), AgentURN: "urn:fixture:publication", IdentityKey: key}, Layout: Layout{Parent: p, Current: current, Candidate: candidate, Aside: aside, CandidateGeneration: "candidate-gen", CandidateManifestDigest: strings.Repeat("b", 64)}, Use: UseBinding{Namespace: ns, CanonicalID: parent, MutationPath: filepath.Join(ns, "existing-mutation"), PinPath: filepath.Join(ns, "supplemental-pin"), MutationIdentity: FileIdentity{Volume: "control-volume", Device: 2, Inode: 3}, PinIdentity: FileIdentity{Volume: "control-volume", Device: 2, Inode: 4}, ReservationID: "original-reservation"}, Events: []Event{{Sequence: 1, Phase: Planned}}}
}
func TestJournalFrozenOriginalAndClosedPhases(t *testing.T) {
	j := journalFixture(t)
	j.Events = append(j.Events, Event{Sequence: 2, Phase: CandidateVerified, ObservedIdentity: j.Layout.Candidate.Identity, ManifestDigest: j.Layout.CandidateManifestDigest}, Event{Sequence: 3, Phase: ReservationHeld})
	b, _ := json.Marshal(j)
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	got.Events[0].Phase = HandoffCommitted
	if j.Events[0].Phase != Planned {
		t.Fatal("journal clone aliased")
	}
	for _, kind := range []string{"skip-intent", "foreign-digest", "future-phase", "observed-inode", "cross-volume", "aside-exists", "namespace-inside"} {
		t.Run(kind, func(t *testing.T) {
			v := j.Clone()
			switch kind {
			case "skip-intent":
				v.Events = append(v.Events, Event{Sequence: 4, Phase: CurrentObserved})
			case "foreign-digest":
				v.Origin.InputDigest = "unbound"
			case "future-phase":
				v.Events[0].Phase = "future"
			case "observed-inode":
				v.Events[1].ObservedIdentity.Inode++
			case "cross-volume":
				v.Layout.Candidate.Identity.Volume = "foreign"
			case "aside-exists":
				v.Layout.Aside.Identity = v.Layout.Candidate.Identity
			case "namespace-inside":
				v.Use.Namespace = v.Layout.Parent.Path
			}
			if v.Validate() == nil {
				t.Fatal("unbound or unsafe journal accepted")
			}
		})
	}
}
func TestJournalDecodeRefusesTornDuplicateUnknownAndOversize(t *testing.T) {
	j := journalFixture(t)
	raw, _ := json.Marshal(j)
	cases := [][]byte{raw[:len(raw)-1], append(append([]byte(nil), raw...), []byte("{}")...), []byte(`{"Version":"workspace.publication.v1","Version":"other"}`), []byte(strings.Repeat(" ", MaxJournalBytes+1))}
	unknown := append([]byte(`{"Foreign":true,`), raw[1:]...)
	cases = append(cases, unknown)
	nested := strings.Replace(string(raw), `"Inode":1`, `"Inode":1,"Inode":1`, 1)
	cases = append(cases, []byte(nested))
	for _, b := range cases {
		if _, err := Decode(b); err == nil {
			t.Fatal("unsafe encoding accepted")
		}
	}
}
func TestExistingCurrentRequiresAuditedGapBeforePublication(t *testing.T) {
	j := journalFixture(t)
	j.Layout.CurrentExists = true
	j.Layout.Current.Identity = FileIdentity{Volume: j.Layout.Parent.Identity.Volume, Device: 1, Inode: 9}
	j.Layout.ExpectedCurrentGeneration = "old-generation"
	j.Layout.ExpectedCurrentManifestDigest = strings.Repeat("c", 64)
	j.Events = append(j.Events, Event{Sequence: 2, Phase: CandidateVerified, ObservedIdentity: j.Layout.Candidate.Identity, ManifestDigest: j.Layout.CandidateManifestDigest}, Event{Sequence: 3, Phase: ReservationHeld}, Event{Sequence: 4, Phase: OldAsideIntent}, Event{Sequence: 5, Phase: OldAsideObserved, ObservedIdentity: j.Layout.Current.Identity, ManifestDigest: j.Layout.ExpectedCurrentManifestDigest}, Event{Sequence: 6, Phase: CandidateCurrentIntent})
	if err := j.Validate(); err != nil {
		t.Fatal(err)
	}
	j.Events = append(j.Events[:3], j.Events[5])
	j.Events[3].Sequence = 4
	if j.Validate() == nil {
		t.Fatal("old current gap phases skipped")
	}
}

func TestJournalDecodeRejectsEquivalentJSONFieldNames(t *testing.T) {
	j := journalFixture(t)
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"outer":        {`"Version":"` + Version + `"`, `"Version":"` + Version + `","version":"` + Version + `"`},
		"nested":       {`"Inode":1`, `"Inode":1,"inode":1`},
		"unicode-fold": {`"ReservationID":"original-reservation"`, `"ReservationID":"original-reservation","ReſervationID":"original-reservation"`},
	} {
		t.Run(name, func(t *testing.T) {
			aliased := strings.Replace(string(raw), pair[0], pair[1], 1)
			if aliased == string(raw) {
				t.Fatal("fixture did not exercise duplicate field")
			}
			if _, err := Decode([]byte(aliased)); err == nil {
				t.Fatal("equivalent JSON names admitted two assignments to one field")
			}
		})
	}
	if _, err := Decode(raw); err != nil {
		t.Fatalf("single field control: %v", err)
	}
}
