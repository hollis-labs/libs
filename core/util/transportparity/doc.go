// Package transportparity gives a test the ability to assert that two
// independently implemented transport doors (HTTP, MCP, ACP, ...) fronting the
// same underlying operation still agree on OUTCOME, without requiring them to
// agree on wire shape.
//
// It is a test-helper library, in the tradition of testify: its exported
// signatures take a testing value and it is meant for _test.go files, never for
// production code. It does not invoke either transport itself. Every app's own
// MCP or ACP call convention differs too much to generalize, so a caller runs
// each door with its own closure, turns each result into an [Outcome], and hands
// the pair to [AssertSameOutcome].
//
// What counts as agreement, on purpose:
//
//   - Both succeed, or both fail. This is the check that catches an
//     authorization step present on one door and missing on the other.
//   - On failure, the same error CATEGORY (a go-svcerr code such as not_found or
//     permission), never the same status number or message: an HTTP status and a
//     JSON-RPC code will never be equal, and wording differs by design.
//   - On success, the same value once both are reduced to canonical JSON
//     ([CanonicalJSON]), so map-key order, pointer versus value and 1 versus 1.0
//     do not cause false failures.
//
// [AcceptedFieldNames] and [AssertSameFieldNames] cover the other half a caller
// can get wrong: what a door ACCEPTS. Two request structs can return the same
// bytes for the same fixture while one calls a field "key" and the other
// "memory_key"; comparing the accepted JSON field paths catches that.
//
// Not here, deliberately: a catalog of which operations exist on which door,
// with waivers (that is roughly nine tenths per-app data), a generic MCP
// invocation helper, and any decision about an app's error envelope (that is
// go-svcerr's).
package transportparity
