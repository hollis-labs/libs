// Package hitlrouting is the routing profile for cards and human-in-the-loop requests:
// a small declarative rule file that says which sink should receive a request and how.
//
// It is a thin profile over core/util/router, which does the matching, ordering and
// decision recording. The profile adds only what is specific to cards and HITL:
//
//   - the field vocabulary a rule can match on: kind, source, tool and namespace;
//   - the target, which is a sink id (a name; see "Sinks" below);
//   - two closed parameters, completion (wait, async, notify) and delivery
//     (durable_message, await), validated when the file is parsed;
//   - a mandatory catch-all, so a configured profile always names a sink and one app can
//     be set up to receive every request.
//
// # Rule file
//
//	{
//	  "version": "2026-10-23.1",
//	  "rules": [
//	    {"id": "prod-shell", "priority": 10,
//	     "match": {"kind": "approval", "tool": "shell", "namespace": "prod/*"},
//	     "sink": "ops-console", "completion": "wait", "delivery": "await"},
//	    {"id": "forms", "match": {"kind": ["form", "survey"]},
//	     "sink": "tachyon", "completion": "async", "delivery": "durable_message"}
//	  ],
//	  "catch_all": {"sink": "tachyon", "completion": "async", "delivery": "durable_message"}
//	}
//
// A match field is one value (exact), a list (any of), or a value containing "*" or "?"
// (glob: "*" is any run of characters including "/", "?" is one). Every field a rule
// names must match; a field the request does not carry never matches. Rules are tried by
// priority, highest first, ties in file order, and the first match wins. The catch-all
// is tried last and cannot be disabled or reordered, and a request always has a kind, so
// a request that reaches Resolve always gets a sink.
//
// # Sinks
//
// The profile names a sink; it does not know what one is. The host application maps a
// sink id to its own delivery: a messaging address, a queue, an in-process handler. A
// host that sends over messaging composes with the messaging router (which routes by the
// address authority) and federation (the mutually authenticated hop); this package
// imports neither and adds no second router.
//
// # Completion and delivery
//
// Both values are labels the host interprets. completion says whether the asker waits
// (wait), takes a handle and is told later (async) or just displays (notify). delivery
// says whether the host should deliver durably as a message (durable_message) or hold
// the request for the asker to await (await). This package does not define how either is
// carried out, and neither changes the go-hitl lifecycle: whether and how a request is
// acknowledged remains the caller's.
package hitlrouting
