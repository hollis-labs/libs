// Package hubtest is the acceptance suite for go-streamhub backends and for
// the Hub that runs on them.
//
// A backend author calls [Conformance] with a [Factory] to check a
// streamhub.Log against the Log contract (cursor assignment, After ranges,
// gap and cursor-ahead errors, paging, Trim, reopen). [HubSuite] runs the
// hub-level behavior (replay/live boundary, slow policies, terminal
// handling, lifecycle) on top of the same Log, so a durable backend is
// exercised through the real Hub too.
//
// The package also exports small helpers for tests of code built on the
// hub: [GatedLog], a Log wrapper that can hold After or Append open, and
// [Next] and [Drain], which read a Subscription and fail the test on error.
//
// The suites use only the testing package and the streamhub API. They use
// real time only as a failure timeout, so they are deterministic when they
// pass.
package hubtest
