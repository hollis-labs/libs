// Package publication validates nonsecret publication journal evidence. Pure
// decoding/classification establishes no filesystem custody, host authority,
// absence, reservation or launch readiness. Managed renames belong to the
// concrete materialize engine; no journal value authorizes replay.
package publication

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/workspace/bootkey"
)

const Version = "workspace.publication.v1"
const MaxJournalBytes = 1 << 20
const MaxEvents = 16

var ErrJournal = errors.New("publication: unbound or unsupported journal")

type Phase string

const (
	Planned                Phase = "planned"
	CandidateVerified      Phase = "candidate_verified"
	ReservationHeld        Phase = "reservation_held"
	OldAsideIntent         Phase = "old_aside_intent"
	OldAsideObserved       Phase = "old_aside_observed"
	CandidateCurrentIntent Phase = "candidate_current_intent"
	CurrentObserved        Phase = "current_observed"
	PublicationCommitted   Phase = "publication_committed"
	HandoffIntent          Phase = "handoff_intent"
	ShimAdoptionObserved   Phase = "shim_adoption_observed"
	CallerPinReleaseIntent Phase = "caller_pin_release_intent"
	HandoffCommitted       Phase = "handoff_committed"
)

// Origin is the original operation binding, never a reissued retry digest.
type Origin struct{ OperationID, InputDigest, AgentURN, IdentityKey string }

// FileIdentity identifies an opened host-observed inode, not a pathname claim.
type FileIdentity struct {
	Volume        string
	Device, Inode uint64
}
type Root struct {
	ID, Path, Owner, Provenance string
	Identity                    FileIdentity
}
type Layout struct {
	Parent, Current, Candidate, Aside                        Root
	CandidateGeneration, CandidateManifestDigest             string
	CurrentExists                                            bool
	ExpectedCurrentGeneration, ExpectedCurrentManifestDigest string
}

// UseBinding records the existing mutation resource and supplemental stable pin.
// The concrete canonical mapping and all current descriptors MUST independently
// match at apply. This record never creates an alternative mutation domain.
type UseBinding struct {
	Namespace, CanonicalID, MutationPath, PinPath string
	MutationIdentity, PinIdentity                 FileIdentity
	ReservationID                                 string
}
type Event struct {
	Sequence         uint64
	Phase            Phase
	ObservedIdentity FileIdentity
	ManifestDigest   string
}
type Journal struct {
	Version, JournalID string
	Control            Root
	Origin             Origin
	Layout             Layout
	Use                UseBinding
	Events             []Event
}

func (j Journal) Clone() Journal { j.Events = append([]Event(nil), j.Events...); return j }
func text(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func absolute(s string) bool       { return text(s) && filepath.IsAbs(s) && filepath.Clean(s) == s }
func (f FileIdentity) Valid() bool { return text(f.Volume) && f.Inode != 0 }
func (r Root) valid() bool {
	return text(r.ID) && absolute(r.Path) && text(r.Owner) && text(r.Provenance)
}
func (j Journal) Validate() error {
	if !text(j.JournalID) || !j.Control.valid() || !j.Control.Identity.Valid() || j.Version != Version || !text(j.Origin.OperationID) || !digest(j.Origin.InputDigest) || !text(j.Origin.AgentURN) {
		return ErrJournal
	}
	key, err := bootkey.Encode(j.Origin.AgentURN)
	if err != nil || key != j.Origin.IdentityKey {
		return ErrJournal
	}
	l := j.Layout
	roots := []Root{l.Parent, l.Current, l.Candidate, l.Aside}
	for i, r := range roots {
		if !r.valid() || r.Owner != l.Parent.Owner {
			return ErrJournal
		}
		for _, prior := range roots[:i] {
			if prior.ID == r.ID || prior.Path == r.Path {
				return ErrJournal
			}
		}
	}
	if filepath.Base(l.Parent.Path) != key || filepath.Base(l.Current.Path) != "current" || !l.Parent.Identity.Valid() || !l.Candidate.Identity.Valid() || !text(l.CandidateGeneration) || !digest(l.CandidateManifestDigest) || l.Aside.Identity != (FileIdentity{}) {
		return ErrJournal
	}
	for _, r := range roots[1:] {
		if filepath.Dir(r.Path) != l.Parent.Path {
			return ErrJournal
		}
	}
	if l.CurrentExists {
		if !l.Current.Identity.Valid() || !text(l.ExpectedCurrentGeneration) || !digest(l.ExpectedCurrentManifestDigest) {
			return ErrJournal
		}
	} else if l.Current.Identity != (FileIdentity{}) || l.ExpectedCurrentGeneration != "" || l.ExpectedCurrentManifestDigest != "" {
		return ErrJournal
	}
	if l.Candidate.Identity.Volume != l.Parent.Identity.Volume || l.CurrentExists && l.Current.Identity.Volume != l.Parent.Identity.Volume {
		return ErrJournal
	}
	u := j.Use
	if !absolute(u.Namespace) || u.CanonicalID != l.Parent.Path || !absolute(u.MutationPath) || !absolute(u.PinPath) || u.MutationPath == u.PinPath || filepath.Dir(u.MutationPath) != u.Namespace || filepath.Dir(u.PinPath) != u.Namespace || !u.MutationIdentity.Valid() || !u.PinIdentity.Valid() || u.MutationIdentity == u.PinIdentity || !text(u.ReservationID) {
		return ErrJournal
	}
	// Namespaces must be disjoint from the replaceable identity parent. Geometry
	// checks do not prove canonical paths or local-volume custody.
	if contained(l.Parent.Path, u.Namespace) || contained(u.Namespace, l.Parent.Path) || contained(l.Parent.Path, j.Control.Path) || contained(j.Control.Path, l.Parent.Path) {
		return ErrJournal
	}
	order := []Phase{Planned, CandidateVerified, ReservationHeld}
	if l.CurrentExists {
		order = append(order, OldAsideIntent, OldAsideObserved)
	}
	order = append(order, CandidateCurrentIntent, CurrentObserved, PublicationCommitted, HandoffIntent, ShimAdoptionObserved, CallerPinReleaseIntent, HandoffCommitted)
	if len(j.Events) == 0 || len(j.Events) > MaxEvents || len(j.Events) > len(order) {
		return ErrJournal
	}
	for i, e := range j.Events {
		if e.Sequence != uint64(i+1) || e.Phase != order[i] {
			return ErrJournal
		}
		switch e.Phase {
		case CandidateVerified:
			if e.ObservedIdentity != l.Candidate.Identity || e.ManifestDigest != l.CandidateManifestDigest {
				return ErrJournal
			}
		case OldAsideObserved:
			if e.ObservedIdentity != l.Current.Identity || e.ManifestDigest != l.ExpectedCurrentManifestDigest {
				return ErrJournal
			}
		case CurrentObserved, PublicationCommitted:
			if e.ObservedIdentity != l.Candidate.Identity || e.ManifestDigest != l.CandidateManifestDigest {
				return ErrJournal
			}
		default:
			if e.ObservedIdentity != (FileIdentity{}) || e.ManifestDigest != "" {
				return ErrJournal
			}
		}
	}
	return nil
}
func contained(base, target string) bool {
	r, e := filepath.Rel(base, target)
	return e == nil && (r == "." || filepath.IsLocal(r))
}

// Decode rejects oversized, duplicate-key, trailing and unknown-field input.
// A valid decoded record remains only trusted-origin data awaiting observation.
func Decode(raw []byte) (Journal, error) {
	if len(raw) == 0 || len(raw) > MaxJournalBytes {
		return Journal{}, ErrJournal
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if uniqueValue(d, 0) != nil {
		return Journal{}, ErrJournal
	}
	if _, e := d.Token(); e != io.EOF {
		return Journal{}, ErrJournal
	}
	var j Journal
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&j) != nil || j.Validate() != nil {
		return Journal{}, ErrJournal
	}
	return j.Clone(), nil
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrJournal
	}
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			folded := foldedFieldName(s)
			if !ok || seen[folded] {
				return ErrJournal
			}
			seen[folded] = true
			if e = uniqueValue(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueValue(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrJournal
	}
	close, e := d.Token()
	if e != nil {
		return e
	}
	if delim == '{' && close != json.Delim('}') || delim == '[' && close != json.Delim(']') {
		return ErrJournal
	}
	return nil
}

// encoding/json matches struct field names using Unicode simple folding.
// Reject a second assignment to that same field before decoding. Only object
// member names are compared this way; evidence values retain their exact bytes.
func foldedFieldName(name string) string {
	return strings.Map(func(r rune) rune {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		return minimum
	}, name)
}
