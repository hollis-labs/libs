package materialize

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/hollis-labs/substrate/harness/workspace/publication"
)

// PublishRequest is a closed operation: the concrete engine may only move the
// declared current to the declared absent aside, then candidate to current.
// Journal facts alone do not supply current host authority or native custody.
type PublishRequest struct{ Journal publication.Journal }

// PublicationHost supplies semantic validation and accounting through the root
// adapter over its existing ReceiptStore. It exposes no managed-file writer.
// Successful validation is not a metadata/volume/absence capability producer.
type PublicationHost interface {
	ValidatePublication(context.Context, PublishRequest) error
	RecordPublication(context.Context, publication.Journal) error
	PublicationControl() publication.Root
}

// PublicationHandle reports accounting, never Ready or launch admission.
// Mutated includes attempted mutation whose result could not be verified.
type PublicationHandle struct {
	Journal   publication.Journal
	Mutated   bool
	Committed bool
	Retained  []publication.Root
}

func publicationRequest(req PublishRequest) (PublishRequest, error) {
	j := req.Journal.Clone()
	if err := j.Validate(); err != nil {
		return PublishRequest{}, err
	}
	// A recovered intent or committed-looking record is inspection input, never
	// permission to replay an old mutation. Only a new, verified reservation
	// boundary may enter this closed execution path.
	if len(j.Events) != 3 || j.Events[2].Phase != publication.ReservationHeld {
		return PublishRequest{}, ErrUnsupportedOperation
	}
	return PublishRequest{Journal: j}, nil
}

func publicationHandle(req PublishRequest) PublicationHandle {
	l := req.Journal.Layout
	return PublicationHandle{Journal: req.Journal.Clone(), Retained: []publication.Root{l.Current, l.Candidate, l.Aside}}
}

// Publish does not extend the artifact-only Engine interface. Complete native
// metadata/volume capability is initially unavailable: semantic callbacks or a
// caller-written journal cannot mint it. Refuse before Record or any mutation.
func (e *DefaultEngine) Publish(ctx context.Context, req PublishRequest, host PublicationHost) (PublicationHandle, error) {
	if ctx == nil {
		return PublicationHandle{}, ErrUnsupportedOperation
	}
	if err := ctx.Err(); err != nil {
		return PublicationHandle{}, err
	}
	frozen, err := publicationRequest(req)
	if err != nil {
		return PublicationHandle{}, err
	}
	out := publicationHandle(frozen)
	if host == nil {
		return out, ErrUnsupportedOperation
	}
	capability, err := preflightPublicationCustody(ctx, frozen)
	if err != nil {
		return out, err
	}
	return e.publishVerified(ctx, frozen, host, capability)
}

// publicationCustody is private earned native admission. There is no public
// constructor, callback/bool mint or decode path. Initial platforms cannot
// produce complete metadata/volume coverage; downstream fixtures are synthetic.
type publicationCustody struct{ original publication.Journal }

func (e *DefaultEngine) publishVerified(ctx context.Context, req PublishRequest, host PublicationHost, capability *publicationCustody) (out PublicationHandle, err error) {
	if ctx == nil || host == nil {
		return out, ErrUnsupportedOperation
	}
	req, err = publicationRequest(req)
	if err != nil {
		return out, err
	}
	out = publicationHandle(req)
	if capability == nil || !samePublicationPins(req.Journal, capability.original) {
		return out, ErrUnsupportedOperation
	}
	native, err := openPublicationNative(req)
	if err != nil {
		return out, err
	}
	defer func() {
		err = errors.Join(err, native.close())
		if err != nil {
			out.Committed = false
		}
	}()
	j := req.Journal.Clone()
	// Every callback precedes actual native custody/context checks. This does
	// not manufacture coverage for a missing complete metadata producer.
	validate := func(stage publication.Phase) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := host.ValidatePublication(ctx, PublishRequest{Journal: j.Clone()}); err != nil {
			return err
		}
		if host.PublicationControl() != j.Control {
			return ErrUnsafeTarget
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return native.validate(ctx, j, stage)
	}
	record := func(phase publication.Phase, observed publication.FileIdentity, digest string) error {
		j.Events = append(j.Events, publication.Event{Sequence: uint64(len(j.Events) + 1), Phase: phase, ObservedIdentity: observed, ManifestDigest: digest})
		out.Journal = j.Clone()
		if err := j.Validate(); err != nil {
			return err
		}
		if err := host.RecordPublication(ctx, j.Clone()); err != nil {
			return err
		}
		return validate(phase)
	}
	if err = validate(publication.ReservationHeld); err != nil {
		return out, err
	}
	l := j.Layout
	if l.CurrentExists {
		if err = record(publication.OldAsideIntent, publication.FileIdentity{}, ""); err != nil {
			return out, err
		}
		out.Mutated = true
		if err = native.rename(filepath.Base(l.Current.Path), filepath.Base(l.Aside.Path)); err != nil {
			return out, err
		}
		if err = native.validate(ctx, j, publication.OldAsideObserved); err != nil {
			return out, err
		}
		if err = record(publication.OldAsideObserved, l.Current.Identity, l.ExpectedCurrentManifestDigest); err != nil {
			return out, err
		}
	}
	if err = record(publication.CandidateCurrentIntent, publication.FileIdentity{}, ""); err != nil {
		return out, err
	}
	out.Mutated = true
	if err = native.rename(filepath.Base(l.Candidate.Path), filepath.Base(l.Current.Path)); err != nil {
		return out, err
	}
	if err = native.validate(ctx, j, publication.CurrentObserved); err != nil {
		return out, err
	}
	if err = record(publication.CurrentObserved, l.Candidate.Identity, l.CandidateManifestDigest); err != nil {
		return out, err
	}
	if err = record(publication.PublicationCommitted, l.Candidate.Identity, l.CandidateManifestDigest); err != nil {
		return out, err
	}
	out.Committed = true
	return out, nil
}

func samePublicationPins(a, b publication.Journal) bool {
	return a.Version == b.Version && a.JournalID == b.JournalID && a.Control == b.Control && a.Origin == b.Origin && a.Layout == b.Layout && a.Use == b.Use
}
