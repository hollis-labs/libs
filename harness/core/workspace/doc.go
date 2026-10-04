// Package workspace defines explicit, resolved workspace preparation inputs.
// Planning consumes observations supplied by a host; it does not discover a
// home, enroll an identity, resolve content, read credentials or grant authority.
// Paths are resource references with ownership and provenance, not shell input.
// Apply revalidates physical roots and ownership/fences under canonical ordered
// locks. Explicit local ports require protected, pre-existing control storage
// and caller-supplied live authority; no ambient defaults select it.
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
// No current operation in this package issues Ready. Apply records planned and
// interrupted control receipts before mutation, then verifies engine manifests
// and managed file digests/modes before recording artifact completion. Failures
// retain affected roots and inspection obligations; no rollback is claimed.
// Installed merges and host effects refuse before mutation. Locks coordinate
// cooperating local writers only; there is no launch pin, crash-safe publication
// or protection promise against a noncooperating ancestor replacement.
// Pre-existing directories are never chmodded or removed by directory ensure.
// This package never cleans retained roots; independent use proof and retirement
// authority are required by any later cleanup operation.
//
// Identity homes consume enrolled exact identity bytes and a persisted bootkey;
// display labels never determine identity paths. Durable homes reattach across
// sessions. Fresh homes require a separately enrolled ephemeral identity. The
// artifact-only legacy entry makes no identity or continuity claim. Neither
// planning nor apply may silently select a global temporary root.
package workspace
