package workspace

import (
	"context"
	"slices"

	"github.com/hollis-labs/substrate/harness/workspace/credentials"
	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/trust"
)

type preparedEffects struct {
	credentials []credentials.PreparedGroup
	trust       []trust.Prepared
}

func effectContext(ctx context.Context, p PlannedWorkspace, ports Ports) effects.PreflightContext {
	c := effects.PreflightContext{Header: effectHeader(p), Validate: func(c context.Context) error {
		if err := c.Err(); err != nil {
			return err
		}
		return ports.Host.Validate(c, copyRecord(p.spec), copyRecord(p.resources))
	}}
	for _, key := range p.locks {
		c.HeldLocks = append(c.HeldLocks, effects.LockIdentity{Namespace: key.Namespace, CanonicalID: key.CanonicalID})
	}
	return c
}

func effectSuccess(o effects.Outcome) bool {
	return o == effects.Prepared || o == effects.Applied || o == effects.AlreadyPresent || o == effects.Omitted
}

func effectStatus(o effects.Outcome) Status {
	if o == effects.Unsupported {
		return Unsupported
	}
	if o == effects.Refused || o == effects.Conflict {
		return Conflict
	}
	return Partial
}

func preflightEffects(ctx context.Context, p PlannedWorkspace, ports Ports) (preparedEffects, error) {
	var prepared preparedEffects
	c := effectContext(ctx, p, ports)
	for _, g := range p.effectInputs.Credentials {
		value, out := credentials.Preflight(ctx, g, c, ports.Credentials)
		if !effectSuccess(out.Outcome) {
			return prepared, refuse(out.Code, "credentials", effectStatus(out.Outcome))
		}
		prepared.credentials = append(prepared.credentials, value)
	}
	// Repository requests remain explicitly deferred until the reviewed leaf is
	// integrated. They cannot be bypassed by an optional port or empty group.
	for _, r := range p.effectInputs.Trust {
		value, out := trust.Preflight(ctx, r, c, ports.Trust)
		if !effectSuccess(out.Outcome) {
			return prepared, refuse(out.Code, "trust", effectStatus(out.Outcome))
		}
		prepared.trust = append(prepared.trust, value)
	}
	return prepared, nil
}

// effectReceiptSink is the only receipt view given to leaves. They cannot open
// storage; successful updates go through the owner's durable ReceiptStore.
type effectReceiptSink struct {
	p      PlannedWorkspace
	store  ReceiptStore
	result *ApplyResult
}

func (s effectReceiptSink) Record(ctx context.Context, e effects.Evidence) error {
	if e.Header != effectHeader(s.p) || !s.known(e) {
		return refuse("effect_evidence_binding", "receipt", Conflict)
	}
	next := copyRecord(s.result.Receipt)
	next.EffectEvidence = append(next.EffectEvidence, e.Clone())
	next.Obligations = slices.Clone(s.result.Obligations)
	next.Phase = Interrupted
	if err := validateFrozenValues(next); err != nil {
		return err
	}
	if err := s.store.Record(ctx, next); err != nil {
		return err
	}
	s.result.Receipt = next
	return nil
}
func (s effectReceiptSink) known(e effects.Evidence) bool {
	if e.Kind == effects.CredentialLinks {
		for _, g := range s.p.effectInputs.Credentials {
			if e.RootID == g.Candidate.ID && len(e.Trust) == 0 {
				return true
			}
		}
	}
	if e.Kind == effects.Trust {
		for _, r := range s.p.effectInputs.Trust {
			if e.RootID == r.Config.ID && len(e.Links) == 0 {
				return true
			}
		}
	}
	return false
}

func appendObligation(result *ApplyResult, o Obligation) {
	if !slices.Contains(result.Obligations, o) {
		result.Obligations = append(result.Obligations, o)
	}
}

func carryRecovery(result *ApplyResult, p PlannedWorkspace, live Observations) error {
	prior := copyRecord(p.resources.RecoveryReceipts)
	for _, r := range live.Receipts {
		if r.OperationID == p.spec.OperationID {
			prior = append(prior, r)
		}
	}
	for _, r := range prior {
		if r.SchemaVersion != SchemaVersion || r.InputDigest == "" || r.OperationID == "" || r.IdentityKey != p.spec.Identity.EncodedKey || r.OperationID == p.spec.OperationID && r.InputDigest != p.digest {
			return refuse("recovery_evidence_binding", "receipt", Conflict)
		}
		for _, root := range r.Roots {
			declared, ok := findRoot(p.roots, root.Root.ID)
			if !ok || declared != root.Root {
				return refuse("recovery_evidence_binding", "roots", Conflict)
			}
			if r.Phase == Interrupted || len(r.Obligations) > 0 {
				result.Retained = appendRoot(result.Retained, declared)
			}
		}
		for _, o := range r.Obligations {
			if o.RootID != "" {
				if _, ok := findRoot(p.roots, o.RootID); !ok {
					return refuse("recovery_evidence_binding", "roots", Conflict)
				}
			}
			appendObligation(result, o)
		}
		result.Receipt.EffectEvidence = append(result.Receipt.EffectEvidence, copyRecord(r.EffectEvidence)...)
		result.Receipt.Effects = append(result.Receipt.Effects, copyRecord(r.Effects)...)
	}
	result.Receipt.Obligations = slices.Clone(result.Obligations)
	return nil
}

func applyEffects(ctx context.Context, p PlannedWorkspace, ports Ports, prepared preparedEffects, result *ApplyResult) error {
	if len(prepared.credentials)+len(prepared.trust) == 0 {
		return nil
	}
	generation := ""
	for _, root := range result.Receipt.Roots {
		if root.Root.ID == p.spec.Boot.Candidate.ID && root.Complete {
			generation = root.Generation
		}
	}
	if generation == "" {
		return refuse("missing_effect_artifacts", "effects", Conflict)
	}
	c := effects.ApplyContext{PreflightContext: effectContext(ctx, p, ports), ArtifactRootID: p.spec.Boot.Candidate.ID, ArtifactGeneration: generation, Receipts: effectReceiptSink{p, ports.ReceiptStore, result}}
	absorb := func(out effects.Result) error {
		for _, o := range out.Obligations {
			appendObligation(result, Obligation{Kind: RecoveryInspectionRequired, RootID: o.RootID, Code: o.Code})
		}
		kind := CredentialLinkEffect
		if out.Evidence.Kind == effects.Trust {
			kind = TrustEffect
		}
		result.Receipt.Effects = append(result.Receipt.Effects, EffectReceipt{Kind: kind, RootID: out.Evidence.RootID, Status: effectStatus(out.Outcome)})
		if out.Outcome == effects.Applied || out.Outcome == effects.Partial || len(out.Obligations) > 0 {
			if root, ok := findRoot(p.roots, out.Evidence.RootID); ok {
				result.Retained = appendRoot(result.Retained, root)
			}
		}
		result.Receipt.Obligations = slices.Clone(result.Obligations)
		if !effectSuccess(out.Outcome) {
			return refuse(out.Code, "effects", effectStatus(out.Outcome))
		}
		return nil
	}
	for _, value := range prepared.credentials {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := absorb(credentials.Apply(ctx, value, c, ports.Credentials)); err != nil {
			return err
		}
	}
	// Shared trust is the last, least reversible effect.
	for _, value := range prepared.trust {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := absorb(trust.Apply(ctx, value, c, ports.Trust)); err != nil {
			return err
		}
	}
	return nil
}
