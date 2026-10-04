// Package workspace defines explicit, resolved workspace preparation inputs.
// Planning consumes observations supplied by a host; it does not discover a
// home, enroll an identity, resolve content, read credentials or grant authority.
// Paths are resource references with ownership and provenance, not shell input.
// Physical canonicalization and fresh ownership/fence checks belong at apply.
//
// Managed regular files have one apply engine: workspace/materialize. Credential
// destinations are excluded from managed trees and manifests, including removal
// selections. Credentials are authorized link effects, never planted bytes.
// Journals, receipts and stable locks are control files with a separate, narrow
// atomicfile carve-out; this is not a second managed-artifact writer.
//
// Artifact completion and launch readiness are separate guarantees. An earned
// artifact-complete result has Partial status and a launch_reservation_pending
// obligation. Legacy callers may proceed on ArtifactsComplete, preserving their
// existing artifact-only guarantee. New launch integrations must require Ready,
// which also requires effects, publication and an acknowledged use reservation.
// No current operation in this package issues Ready.
//
// Identity homes consume enrolled exact identity bytes and a persisted bootkey;
// display labels never determine identity paths. Durable homes reattach across
// sessions. Fresh homes require a separately enrolled ephemeral identity. The
// artifact-only legacy entry makes no identity or continuity claim. Neither
// planning nor apply may silently select a global temporary root.
// Every semantic root must match the host's declared roster. Observations bind
// its declared path and preserve its suffix relative to an owned allowed base;
// aliases of that base share one canonical base. The observed lock namespace
// uses the same binding, so canonical action paths and lock keys agree.
//
// DigestVersion pins SHA-256 over its NUL-terminated domain followed by JSON of
// the frozen input record. Set-valued grants, capabilities and provider homes
// are sorted and deduplicated; ordered launch arguments retain their order.
// Changing the record encoding requires a new version and golden. Identity-root
// basenames use the persisted bootkey; a bootkey.Version change requires an
// explicit identity migration, and old keys otherwise fail closed.
//
// CurrentManifest in a planned engine request is advisory snapshot evidence.
// Apply must reload the committed manifest under locks before reconciliation.
// Purity is a property of Plan's calls, rather than the imported package graph.
// Windows trailing-dot and trailing-space aliases are not supported by this
// lexical contract; hosts must refuse them during physical validation.
package workspace
