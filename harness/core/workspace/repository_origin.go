package workspace

import (
	"reflect"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

func originHeader(o RepositoryOrigin) effects.Header {
	return effects.Header{Version: effects.SchemaVersion, OperationID: o.OperationID, InputDigest: o.InputDigest}
}

func originEvidenceBound(requests []repositories.Request, e effects.Evidence) bool {
	for _, request := range requests {
		if repositoryEvidenceBound(request, e) {
			return true
		}
	}
	return false
}

// Admission precedes flattening or inspection. Fresh members must be bound to
// this receipt's own pins. Inherited members must also occur in a preserved
// trusted origin envelope; a mutually consistent foreign inner pair is not one.
func admitRepositoryOrigins(r Receipt) ([]RepositoryOrigin, error) {
	fail := func() ([]RepositoryOrigin, error) {
		return nil, refuse("repository_origin_binding", "repositories", Conflict)
	}
	self := RepositoryOrigin{SchemaVersion: r.SchemaVersion, OperationID: r.OperationID, InputDigest: r.InputDigest, IdentityKey: r.IdentityKey}
	ownHeader := originHeader(self)
	origins := copyRecord(r.RepositoryOrigins)
	for _, o := range origins {
		if o.SchemaVersion != SchemaVersion || o.IdentityKey != r.IdentityKey || !originHeader(o).Valid() {
			return fail()
		}
		// An operation ID remains bound to a single digest even after reissue.
		if o.OperationID == r.OperationID && o.InputDigest != r.InputDigest {
			return fail()
		}
		for _, request := range o.Requests {
			if request.Header != originHeader(o) {
				return fail()
			}
		}
		for _, e := range o.Evidence {
			if e.Header != originHeader(o) || !originEvidenceBound(o.Requests, e) {
				return fail()
			}
		}
	}
	for _, request := range r.RepositoryRequests {
		if request.Header == ownHeader {
			self.Requests = append(self.Requests, request)
			continue
		}
		found := false
		for _, o := range origins {
			if slices.Contains(o.Requests, request) {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	for _, e := range r.EffectEvidence {
		if e.Kind != effects.RepositoryAttachment {
			continue
		}
		if e.Header == ownHeader {
			if !originEvidenceBound(self.Requests, e) {
				return fail()
			}
			self.Evidence = append(self.Evidence, e.Clone())
			continue
		}
		found := false
		for _, o := range origins {
			if slices.ContainsFunc(o.Evidence, func(v effects.Evidence) bool { return reflect.DeepEqual(v, e) }) {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	// Origin envelopes cannot substitute different members for the aggregate's
	// public request/evidence view, nor silently introduce hidden recovery state.
	for _, o := range origins {
		for _, request := range o.Requests {
			if !slices.Contains(r.RepositoryRequests, request) {
				return fail()
			}
		}
		for _, e := range o.Evidence {
			if !slices.ContainsFunc(r.EffectEvidence, func(v effects.Evidence) bool { return reflect.DeepEqual(v, e) }) {
				return fail()
			}
		}
	}
	if len(self.Requests) > 0 || len(self.Evidence) > 0 {
		origins = mergeRepositoryOrigin(origins, self)
	}
	return origins, nil
}

// Sequential snapshots of one trusted origin may contain additional terminal
// evidence. Preserve the observed ordering and all members; never replace an
// earlier origin with the current retry operation's pins.
func mergeRepositoryOrigin(origins []RepositoryOrigin, o RepositoryOrigin) []RepositoryOrigin {
	for i := range origins {
		prior := &origins[i]
		if prior.SchemaVersion != o.SchemaVersion || prior.OperationID != o.OperationID || prior.InputDigest != o.InputDigest || prior.IdentityKey != o.IdentityKey {
			continue
		}
		for _, request := range o.Requests {
			if !slices.Contains(prior.Requests, request) {
				prior.Requests = append(prior.Requests, request)
			}
		}
		for _, e := range o.Evidence {
			if !slices.ContainsFunc(prior.Evidence, func(v effects.Evidence) bool { return reflect.DeepEqual(v, e) }) {
				prior.Evidence = append(prior.Evidence, e.Clone())
			}
		}
		return origins
	}
	return append(origins, copyRecord(o))
}
