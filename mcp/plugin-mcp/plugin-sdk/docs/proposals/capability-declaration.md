# Proposal: capability declaration

**Status:** accepted and implemented, 2026-09-18.
**Raised:** 2026-09-18, from a cross-host audit of Cerberus and Nanite.

Implemented as `subprocess.CapabilityRequest`, `InitParams.Granted`,
`InitParams.HasCapability` and `subprocesstest.WithGranted`. The resolution is
recorded at the end of this document; the argument below is kept as written so
the decision can be re-read against what raised it.

## The gap

Neither host built against this SDK has any way for a plugin to declare what
ambient access it needs, and neither has a way to grant less than everything.

The concrete case that raised it. One host passes a curated environment
allow-list to plugin subprocesses, with a comment stating it "carries no
credentials by design". The list includes `SSH_AUTH_SOCK`, `DOCKER_HOST` and
`DOCKER_CONFIG`. An SSH agent socket is a live credential handle: **every loaded
plugin can authenticate as the operator to every host that trusts their key**,
whether or not it declared any credential at all. Docker socket access is
root-equivalent on most hosts.

Those variables are there for a real reason — one connector needs remote Docker
over SSH. The problem is not that they are granted, it is that they are granted
*ambiently, to every plugin, undeclared and invisible*.

That is exactly the pattern the credential channel in this SDK exists to avoid.
A plugin declares the secrets it needs, the host resolves only those, and they
arrive over init config rather than the environment — because the environment is
ambient and reaches everything. **The same argument applies to non-secret
capabilities, and today nothing carries it.**

Checked against both hosts: no `Permission`, `Capability`, `Scope` or `Grant`
field exists in either manifest schema. This is a shared gap, not one host's
oversight.

## Why this might belong here

The SDK's `AGENTS.md` is explicit that a host's trust or isolation model does
not live in this repo, and this proposal does not argue with that. Enforcement,
sandboxing and the meaning of any particular capability are host concerns and
should stay host concerns.

What is proposed is narrower: **a declaration mechanism with an open
vocabulary**, so that "this plugin asks for X" is expressible in the shared
contract while "what X means and whether you get it" stays entirely with the
host.

There is precedent in this repository for exactly that split. The registry
contract already treats a contribution kind as "an open string and its metadata
is opaque JSON, because one host's envelopes, widgets and slots are another
host's something else." Capabilities have the same shape: `ssh_agent` means
something to an infrastructure control plane and nothing to an agent framework,
and neither host should have to learn the other's vocabulary to use the
mechanism.

If that argument does not hold — if a declaration with no enforcement is judged
to be host vocabulary wearing a shared coat — then the right outcome is that
each host adds its own field and this proposal is closed. That is a legitimate
answer. What should not happen is that neither host has one because it was
never decided.

## Sketch

Deliberately thin. The SDK would carry the request and surface it at `Init`;
everything else is the host's.

```go
// A capability a plugin requests from its host. Name is an open string whose
// meaning belongs to the host; Metadata is opaque JSON the host may interpret.
// The SDK neither defines names nor enforces grants.
type CapabilityRequest struct {
    Name     string          `json:"name"`
    Reason   string          `json:"reason,omitempty"`
    Optional bool            `json:"optional,omitempty"`
    Metadata json.RawMessage `json:"metadata,omitempty"`
}
```

And the granted set travelling back on the existing handshake, so a plugin can
tell what it actually got:

```go
type InitParams struct {
    // ... existing fields ...

    // Granted lists the capability names the host allowed. A plugin that
    // requested a capability absent here must degrade rather than assume.
    Granted []string `json:"granted,omitempty"`
}
```

Three properties worth preserving whatever the final shape:

- **Declaration lives in the host's manifest**, not in code, so it is reviewable
  before the plugin runs. The SDK carries the type; the host's schema embeds it.
- **`Granted` is honest.** A host that grants nothing says so, and a plugin that
  asked for something optional can adapt. A silent partial grant is the current
  situation with extra steps.
- **Additive and backward compatible.** An older plugin sends no requests; an
  older host ignores the field and sends no `Granted`. Both keep working, which
  the existing forward-compatibility tests would need to cover.

## What this does not solve

- **Enforcement.** A host that declares capabilities and then passes the same
  environment to everyone has gained documentation, not security. The mechanism
  is only worth adding if at least one host intends to enforce it.
- **Sandboxing.** Unchanged and still host-owned.
- **A hostile plugin.** A plugin that lies in its manifest is bounded by what
  the host grants, not by what it claimed — which is the point, but it means the
  declaration is an input to a decision rather than a guarantee by itself.

## Resolution

**Accepted, as a declaration mechanism only.** The host-neutrality test the
proposal set for itself is met, and it is met the same way the registry
contract meets it: `Name` is an open string, `Metadata` is opaque JSON, and the
SDK defines no capability names. Nothing in the module reads a
`CapabilityRequest`. `AGENTS.md` records the boundary alongside the registry's.

Two things were decided in the course of implementing it that the sketch did
not settle.

**An absent capability is not a refusal, and the SDK does not pretend
otherwise.** A host that predates the mechanism omits `granted` entirely; a host
that grants nothing sends an empty list. With `omitempty` — which the sketch
specified and which keeps a non-participating host's payload byte-identical to
what it sent before — those two are indistinguishable on the wire. Rather than
add machinery to tell them apart, `HasCapability` documents the ambiguity and
`docs/best-practices.md` gives the rule that follows from it: degrade on
absence, and fail the *operation* that genuinely needs the capability, never the
load. The alternative — a newer plugin refusing to start against an older host —
would break working installs to enforce a control the SDK does not implement,
which is the failure mode this proposal warns about, pointed the other way.

**The degraded path is the default in tests.** `subprocesstest.WithGranted`
seeds the granted list and the harness grants nothing without it, so a plugin
that assumes a capability it never received fails its own suite rather than an
operator's install.

What did not change: the SDK still grants, resolves and enforces nothing, and
`ProtocolVersion` is still 1. The "What this does not solve" section above
stands in full, and the first item in it remains the load-bearing one. **A host
that declares capabilities and then passes the same environment to every plugin
has gained documentation, not security.** The mechanism is now available to be
enforced; whether it is, is each host's to answer.

## Next step — for the hosts, not this repo

The SDK half is done. What remains is host work, and the audit that raised this
is the tracking record for it:

- Embed `CapabilityRequest` in the manifest schema, so a plugin's request is
  reviewable before it runs.
- Decide what to allow, and report it honestly in `InitParams.Granted` — a host
  that grants everything and says so is at least auditable; one that grants
  everything and says nothing is where we started.
- Narrow the ambient environment to match. Until that happens the declaration
  is documentation, which is worth having and is not a control.

Cross-reference: `docs/plans/plugin-capability-audit.md` in
`hollis-labs/cerberus` records the audit that raised this, including the full
comparison of what each host dispatches and declares.
