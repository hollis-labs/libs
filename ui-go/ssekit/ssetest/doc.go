// Package ssetest is the test harness for code that speaks Server-Sent Events
// through github.com/hollis-labs/libs/ui-go/ssekit.
//
// Script builds an http.Handler that plays a scripted, deliberately hostile
// server: it drops connections, overlaps and skips event ids, answers with
// error statuses, stalls and bursts. Record and Replay capture a stream as
// frames with inter-chunk timing and play it back, deterministically under
// testing/synctest.
package ssetest
