# Credential resource links

This package prepares authorized logical resource links in an inactive private
candidate. It never discovers a provider home, reads credential contents,
copies credentials, or grants launch readiness. Installed targets refuse links.

The host captures the real provider home **before** redirecting native config
environment variables. `ResolveRealHome` validates that explicit capture and
canonical observations; `Group` records the capture, concrete bindings and
individual authorization revisions. Source read access is explicit. Source
write access needs a separate authorization and remains the host's sandbox
responsibility; these handlers never write to sources.

The workspace owner holds the complete mutation lock set in a protected
namespace, validates authority/fences, and preflights every effect before root
mutation. `Preflight` checks every required source and destination without
creating directories. Future candidates and missing future parents are
observational only. After the regular artifact engine creates and verifies the
candidate and its parents, the owner supplies its root ID, generation and
receipt sink to `Apply`. All actual sources and destinations are rechecked
before the first link. A destination is absent or the exact authorized existing
link; files and conflicting links are never replaced.

`effects.ReceiptSink` is a narrow adapter over the owner's single operation
receipt store, not another persistence implementation. Group and per-link
intent precede mutation; link identities and progress follow it. A receipt
failure is an operation failure. The operation ID and frozen input digest must
cover the capture, roots, grants and entire group; callers must not reuse them
with changed inputs. Evidence is trusted host input: schema, operation, digest
and resource binding checks are **not cryptographic authentication**.

On error, only links created by this call are compensation targets. Reverse
compensation checks authority, candidate custody, pinned parent identity, link
identity and logical target. Existing correct links and all source resources
remain untouched. Every post-mutation or uncertain failure is `partial`, even
after successful compensation; obligations identify retention and recovery.
Receipts contain references and identities, never credential bytes or content
digests. Logical targets retain visibility of atomic reauthentication.

`Inspect` observes complete and interrupted evidence without replaying links.
It classifies before, intended-after, missing and divergent paths. Unrecorded
links are divergent even when their target looks right. Interrupted groups
remain partial until the owner's recovery protocol reconciles them. These
helpers inspect inactive candidates; publication and runtime pin ownership are
separate responsibilities.

`credentials/localfs` uses `os.Root` on Linux and macOS. Unsupported platforms
refuse before mutation. Directory traversal rejects symlink parents; exclusive
creation does not replace destinations. Private custody and cooperating locks
are required. This does **not** protect against a same-user process renaming
parents or swapping entries, and compare-then-remove is not an atomic
compare-and-unlink guarantee. Unproven custody retains links. No filesystem
durability or all-or-nothing transaction across other effect kinds is claimed.
