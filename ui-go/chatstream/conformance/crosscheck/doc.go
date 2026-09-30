// Package crosscheck is the integration test of the whole module: every
// dialect's recorded streams, decoded by its adapter, encoded by every sink. It
// is where a decoder and an encoder that were each correct by their own tests
// would disagree about a convention (the Meta keys a tool call is rendered from,
// which part a tool result answers). It has no non-test code.
package crosscheck
