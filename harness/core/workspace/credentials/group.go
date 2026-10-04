package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/substrate/harness/workspace/effects"
	"github.com/hollis-labs/substrate/harness/workspace/render"
)

func freeze(g Group) Group {
	g.Bindings = append([]Binding(nil), g.Bindings...)
	g.Home.PlantedRoots = append([]string(nil), g.Home.PlantedRoots...)
	sort.Slice(g.Bindings, func(i, j int) bool { return g.Bindings[i].Destination < g.Bindings[j].Destination })
	return g
}
func digest(g Group) string {
	b, _ := json.Marshal(g)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func result(g Group, out effects.Outcome, code string) effects.Result {
	return effects.Result{Outcome: out, Code: code, Evidence: effects.Evidence{Header: g.Header, Kind: effects.CredentialLinks, RootID: g.Candidate.ID, Phase: "preflight", Outcome: out}}
}
func binding(g Group, c effects.PreflightContext) string {
	r := g.Candidate
	h := g.Home
	if !g.Header.Valid() || g.Header != c.Header {
		return "invalid_binding"
	}
	if g.Layer != "boot" {
		return "installed_links_refused"
	}
	if r.ID == "" || r.Owner == "" || r.Provenance == "" || !r.Inactive || !r.PrivateCustody || !absolute(r.Path) || !absolute(r.AllowedBase) || !under(r.AllowedBase, r.Path) || r.Path == r.AllowedBase || !absolute(r.MutationIdentity) || !under(r.MutationIdentity, r.Path) || r.Path == r.MutationIdentity {
		return "candidate_refused"
	}
	locked := false
	for _, l := range c.HeldLocks {
		if l.Namespace != "" && absolute(l.Namespace) && l.CanonicalID == r.MutationIdentity && !under(r.MutationIdentity, l.Namespace) {
			locked = true
		}
	}
	if !locked {
		return "missing_lock"
	}
	if c.Validate == nil {
		return "missing_authority"
	}
	if !h.BeforeRedirect || h.Provider == "" || h.CaptureID == "" || h.Revision == "" || h.Provenance == "" || !absolute(h.LogicalPath) || !absolute(h.CanonicalPath) || !absolute(h.CanonicalBase) || !under(h.CanonicalBase, h.CanonicalPath) || under(r.MutationIdentity, h.CanonicalPath) || under(r.MutationIdentity, h.LogicalPath) || len(h.PlantedRoots) == 0 {
		return "home_refused"
	}
	for _, p := range h.PlantedRoots {
		if !absolute(p) || under(p, h.CanonicalPath) || under(p, h.LogicalPath) {
			return "home_refused"
		}
	}
	if len(g.Bindings) == 0 || len(g.Bindings) > 4096 {
		return "invalid_group"
	}
	for n, b := range g.Bindings {
		if render.ValidateRelPath(b.Source) != nil || render.ValidateRelPath(b.Destination) != nil || b.AuthorizationID == "" || b.AuthorizationVersion == "" || !b.SourceRead || (b.SourceWrite && (b.SourceWriteAuthorizationID == "" || b.SourceWriteAuthorizationVersion == "")) {
			return "binding_refused"
		}
		for _, prior := range g.Bindings[:n] {
			if strings.EqualFold(prior.Destination, b.Destination) || strings.HasPrefix(strings.ToLower(prior.Destination), strings.ToLower(b.Destination)+"/") || strings.HasPrefix(strings.ToLower(b.Destination), strings.ToLower(prior.Destination)+"/") {
				return "destination_alias"
			}
		}
	}
	return ""
}

func inspectSource(ctx context.Context, g Group, b Binding, p LinkPort) (SourceObservation, bool, string) {
	s, err := p.Source(ctx, g.Home, b.Source)
	if err != nil || !s.Accessible {
		if !b.Required {
			return SourceObservation{}, true, ""
		}
		return s, false, "source_unavailable"
	}
	want := filepath.Join(g.Home.LogicalPath, filepath.FromSlash(b.Source))
	if s.LogicalPath != want || !absolute(s.CanonicalPath) || !under(g.Home.CanonicalPath, s.CanonicalPath) || under(g.Candidate.MutationIdentity, s.CanonicalPath) {
		return s, false, "source_escape"
	}
	for _, r := range g.Home.PlantedRoots {
		if under(r, s.CanonicalPath) {
			return s, false, "source_escape"
		}
	}
	if s.LogicalPath == filepath.Join(g.Candidate.Path, filepath.FromSlash(b.Destination)) {
		return s, false, "self_link"
	}
	return s, false, ""
}

// Preflight checks the entire group without creating candidate directories.
func Preflight(ctx context.Context, g Group, c effects.PreflightContext, p LinkPort) (PreparedGroup, effects.Result) {
	g = freeze(g)
	if code := binding(g, c); code != "" {
		return PreparedGroup{}, result(g, effects.Refused, code)
	}
	if p == nil || !p.Supported() {
		return PreparedGroup{}, result(g, effects.Unsupported, "platform_unsupported")
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil {
		return PreparedGroup{}, result(g, effects.Refused, "authority_refused")
	}
	for _, b := range g.Bindings {
		s, omit, code := inspectSource(ctx, g, b, p)
		if code != "" {
			return PreparedGroup{}, result(g, effects.Refused, code)
		}
		// Even optional omitted sources cannot hide conflicting destinations.
		d, err := p.Destination(ctx, g.Candidate, b.Destination)
		if err != nil {
			return PreparedGroup{}, result(g, effects.Conflict, "destination_unavailable")
		}
		if d.Exists && (!d.IsLink || omit || d.Target != s.LogicalPath || d.Identity == "" || d.ParentIdentity == "") {
			return PreparedGroup{}, result(g, effects.Conflict, "destination_conflict")
		}
	}
	return PreparedGroup{group: g, digest: digest(g)}, result(g, effects.AlreadyPresent, "preflight_complete")
}

// Apply rechecks ALL sources and actual destinations before the first link.
// Every post-mutation failure is Partial, including successful compensation.
func Apply(ctx context.Context, prepared PreparedGroup, c effects.ApplyContext, p LinkPort) (out effects.Result) {
	g := freeze(prepared.group)
	if prepared.digest == "" || prepared.digest != digest(g) {
		return result(g, effects.Refused, "invalid_prepared_group")
	}
	if _, pre := Preflight(ctx, g, c.PreflightContext, p); pre.Code != "preflight_complete" {
		return pre
	}
	if c.Receipts == nil || c.ArtifactRootID != g.Candidate.ID || c.ArtifactGeneration == "" {
		return result(g, effects.Refused, "missing_apply_evidence")
	}
	session, err := p.OpenCandidate(ctx, g.Candidate)
	if err != nil {
		return result(g, effects.Conflict, "candidate_unavailable")
	}
	if session == nil {
		return result(g, effects.Unsupported, "candidate_unsupported")
	}
	defer func() {
		if session.Close() != nil {
			out.Outcome = effects.Partial
			out.Code = "candidate_close_failed"
			out.Evidence.Outcome = effects.Partial
			out.Evidence.Phase = "interrupted"
			if c.Receipts.Record(context.WithoutCancel(ctx), out.Evidence.Clone()) != nil {
				out.Obligations = append(out.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "receipt_pending"})
			}
			out.Obligations = append(out.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "recovery_required"})
		}
	}()
	out = result(g, effects.AlreadyPresent, "")
	var sources []SourceObservation
	var observed []LinkObservation
	var omitted []bool
	for _, b := range g.Bindings {
		s, omit, code := inspectSource(ctx, g, b, p)
		if code != "" {
			return result(g, effects.Refused, code)
		}
		d, e := session.Inspect(ctx, b.Destination)
		if e != nil {
			return result(g, effects.Conflict, "destination_unavailable")
		}
		if d.Exists && (!d.IsLink || omit || d.Target != s.LogicalPath || d.Identity == "" || d.ParentIdentity == "") {
			return result(g, effects.Conflict, "destination_conflict")
		}
		sources = append(sources, s)
		observed = append(observed, d)
		omitted = append(omitted, omit)
	}
	out.Evidence.Phase = "intent"
	for n, b := range g.Bindings {
		state := effects.AlreadyPresent
		if !observed[n].Exists {
			state = effects.Pending
		}
		if omitted[n] {
			state = effects.Omitted
		}
		out.Evidence.Links = append(out.Evidence.Links, effects.LinkEvidence{Source: sources[n].LogicalPath, Destination: b.Destination, Target: sources[n].LogicalPath, AuthorizationID: b.AuthorizationID, AuthorizationVersion: b.AuthorizationVersion, ParentIdentity: observed[n].ParentIdentity, LinkIdentity: observed[n].Identity, Outcome: state})
	}
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return result(g, effects.Refused, "receipt_failed_before_mutation")
	}
	mutated := false
	fail := func(code string) effects.Result {
		if !mutated {
			out.Outcome = effects.Conflict
			out.Code = code
			out.Evidence.Outcome = out.Outcome
			return out
		}
		out.Outcome = effects.Partial
		out.Code = code
		out.Evidence.Outcome = effects.Partial
		out.Evidence.Phase = "interrupted"
		cleanupCtx := context.WithoutCancel(ctx)
		for n := len(out.Evidence.Links) - 1; n >= 0; n-- {
			e := &out.Evidence.Links[n]
			if !e.Created {
				continue
			}
			want := LinkObservation{Exists: true, IsLink: true, Target: e.Target, Identity: e.LinkIdentity, ParentIdentity: e.ParentIdentity}
			if c.Validate(cleanupCtx) != nil || session.Validate(cleanupCtx) != nil || session.RemoveIfMatches(cleanupCtx, e.Destination, want) != nil {
				e.Outcome = effects.Partial
				out.Obligations = append(out.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "link_retained"})
			} else {
				e.Outcome = effects.Omitted
			}
		}
		out.Obligations = append(out.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "recovery_required"})
		if c.Receipts.Record(cleanupCtx, out.Evidence.Clone()) != nil {
			out.Obligations = append(out.Obligations, effects.Obligation{RootID: g.Candidate.ID, Code: "receipt_pending"})
		}
		return out
	}
	for n, b := range g.Bindings {
		if omitted[n] || observed[n].Exists {
			continue
		}
		if ctx.Err() != nil || c.Validate(ctx) != nil || session.Validate(ctx) != nil {
			return fail("authority_or_candidate_changed")
		}
		// Recheck source availability at each mutation without reading bytes.
		if _, omit, code := inspectSource(ctx, g, b, p); code != "" || omit {
			return fail("source_changed")
		}
		out.Evidence.Phase = "link_intent"
		if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
			return fail("receipt_failed")
		}
		made, e := session.CreateExclusive(ctx, sources[n].LogicalPath, b.Destination)
		if made.Exists {
			mutated = true
			entry := &out.Evidence.Links[n]
			entry.Created = true
			entry.LinkIdentity = made.Identity
			entry.ParentIdentity = made.ParentIdentity
			entry.Outcome = effects.Applied
		}
		if e != nil || !made.Exists || !made.IsLink || made.Target != sources[n].LogicalPath || made.Identity == "" || made.ParentIdentity == "" {
			return fail("link_creation_failed")
		}
		out.Evidence.Phase = "link_created"
		if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
			return fail("receipt_failed")
		}
	}
	if ctx.Err() != nil || c.Validate(ctx) != nil || session.Validate(ctx) != nil {
		return fail("authority_or_candidate_changed")
	}
	for n, b := range g.Bindings {
		if omitted[n] {
			continue
		}
		if _, omit, code := inspectSource(ctx, g, b, p); code != "" || omit {
			return fail("source_changed")
		}
		d, e := session.Inspect(ctx, b.Destination)
		wanted := out.Evidence.Links[n]
		if e != nil || !d.Exists || !d.IsLink || d.Target != wanted.Target || d.Identity != wanted.LinkIdentity || d.ParentIdentity != wanted.ParentIdentity {
			return fail("verification_failed")
		}
	}
	if mutated {
		out.Outcome = effects.Applied
	} else {
		out.Outcome = effects.AlreadyPresent
	}
	out.Evidence.Outcome = out.Outcome
	out.Evidence.Phase = "complete"
	if c.Receipts.Record(ctx, out.Evidence.Clone()) != nil {
		return fail("receipt_failed")
	}
	out.Code = "group_complete"
	return out
}
