package workspace

import (
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/repositories"
)

func TestRepositoryOriginAdmissionPreservesExactMembers(t *testing.T) {
	s, c, r, o := repositoryPlanInputs(t)
	p := fixturePlanned(t, s, c, r, o)
	req := p.EffectInputs().Repositories[0]
	e := repositoryEvidenceFixture(p)
	origin := RepositoryOrigin{SchemaVersion: SchemaVersion, OperationID: req.Header.OperationID, InputDigest: req.Header.InputDigest, IdentityKey: s.Identity.EncodedKey, Requests: []repositories.Request{req}, Evidence: []effects.Evidence{e}}
	control := Receipt{SchemaVersion: SchemaVersion, OperationID: "aggregate-operation", InputDigest: "aggregate-digest", IdentityKey: s.Identity.EncodedKey, RepositoryRequests: []repositories.Request{req}, EffectEvidence: []effects.Evidence{e}, RepositoryOrigins: []RepositoryOrigin{origin}}
	if _, err := admitRepositoryOrigins(control); err != nil {
		t.Fatal("older origin refused", err)
	}
	for name, change := range map[string]func(*Receipt){
		"request-only-foreign-header": func(r *Receipt) {
			r.RepositoryOrigins[0].Evidence = nil
			r.EffectEvidence = nil
			r.RepositoryOrigins[0].Requests[0].Header.OperationID = "foreign"
			r.RepositoryRequests[0].Header.OperationID = "foreign"
		},
		"coherent-unbound-evidence": func(r *Receipt) {
			r.RepositoryOrigins[0].Evidence[0].Attachments[0].AuthorizationVersion = "foreign"
			r.EffectEvidence[0].Attachments[0].AuthorizationVersion = "foreign"
		},
		"extra-aggregate-request": func(r *Receipt) {
			r.RepositoryOrigins[0].Evidence = nil
			r.EffectEvidence = nil
			extra := r.RepositoryRequests[0]
			extra.Branch = "work/extra"
			r.RepositoryRequests = append(r.RepositoryRequests, extra)
		},
		"schema":                     func(r *Receipt) { r.RepositoryOrigins[0].SchemaVersion = "foreign" },
		"identity":                   func(r *Receipt) { r.RepositoryOrigins[0].IdentityKey = "foreign" },
		"operation":                  func(r *Receipt) { r.RepositoryOrigins[0].OperationID = "foreign" },
		"digest":                     func(r *Receipt) { r.RepositoryOrigins[0].InputDigest = "foreign" },
		"origin-request-missing":     func(r *Receipt) { r.RepositoryOrigins[0].Requests = nil },
		"origin-evidence-missing":    func(r *Receipt) { r.RepositoryOrigins[0].Evidence = nil },
		"aggregate-request-missing":  func(r *Receipt) { r.RepositoryRequests = nil },
		"aggregate-evidence-missing": func(r *Receipt) { r.EffectEvidence = nil },
		"unbound-origin-evidence":    func(r *Receipt) { r.RepositoryOrigins[0].Evidence[0].Attachments[0].AuthorizationVersion = "foreign" },
		"conflicting-self-digest": func(r *Receipt) {
			r.RepositoryOrigins[0].OperationID = r.OperationID
			r.RepositoryOrigins[0].Requests[0].Header.OperationID = r.OperationID
			r.RepositoryOrigins[0].Evidence[0].Header.OperationID = r.OperationID
			r.RepositoryRequests[0].Header.OperationID = r.OperationID
			r.EffectEvidence[0].Header.OperationID = r.OperationID
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := copyRecord(control)
			change(&value)
			if _, err := admitRepositoryOrigins(value); err == nil {
				t.Fatal("origin or member mismatch accepted")
			}
		})
	}
	detached, err := admitRepositoryOrigins(control)
	if err != nil {
		t.Fatal(err)
	}
	detached[0].Requests[0].SourceIdentity = "edited"
	detached[0].Evidence[0].Attachments[0].AuthorizationVersion = "edited"
	if control.RepositoryOrigins[0].Requests[0].SourceIdentity == "edited" || control.RepositoryOrigins[0].Evidence[0].Attachments[0].AuthorizationVersion == "edited" {
		t.Fatal("mutable origin evidence")
	}
}
