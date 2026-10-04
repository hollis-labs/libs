//go:build !unix

package goldens

// Non-Unix platforms have no process umask.
func pinFixtureUmask() func() { return func() {} }
