// Package install prepares conservative changes for the closed Claude and
// Codex installed layouts. Prepare is pure: it consumes resolved render output,
// explicit path/key grants and physical snapshots, and freezes a detached
// request for the concrete materialize engine. It reads no environment or file,
// resolves no home, records nothing and starts no process.
//
// First installation creates only absent granted files or keys. Refresh needs
// trusted previous committed evidence for every present owned leaf, and that
// leaf must still match its recorded state. A filename, marker or grant alone
// never adopts existing content. Whole-file owned drift and native structural
// conflicts refuse. Unknown operator JSON members and TOML semantic values
// remain; TOML refresh uses the existing encoder and does not preserve comments
// or layout. A fully granted absent document preserves its rendered bytes.
//
// Existing unreadable documents refuse, including physically present zero-byte
// JSON, invalid UTF-8 and ambiguous duplicate keys. This apply boundary does not
// use keymerge's historical unreadable-document overwrite outcome. The pure
// keymerge merge APIs and archived renderer evidence keep their existing scope.
//
// Snapshots carry bytes only as preparation input. Evidence carries paths,
// identities, phases and file/key digests, with no document values. It belongs
// in the host's trusted external workspace ReceiptStore, not a manifest in a
// user-managed root. Evidence is a host trust contract, not self-authenticating
// authority. workspace.Plan and workspace.Materialize independently bind its
// original operation/digest, full root custody and current semantic grants.
//
// Existing user/provider directories are traversal observations. Their modes
// are preserved, and they are never adopted or removed. Only necessary absent
// parents may be created, with operation-created metadata. Unicode NFC and
// observed filesystem case behavior are comparison keys for rejecting aliases;
// user paths are never rewritten. Unknown case coverage refuses.
//
// Preparation and installed artifact completion confer no launch readiness.
// Publication, pin handoff, retirement and runtime activation are separate
// contracts. Unresolved installed intent is retained and refused rather than
// replayed or compensated. OpenCode and Antigravity installed apply remain
// unsupported; their pure render behavior is unchanged.
package install
