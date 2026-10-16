package redact

import (
	"errors"
	"fmt"
)

// Renderer is an error that knows which part of its text the application wrote
// itself. RenderRedacted returns that prose as it is and passes anything else in its
// text (a wrapped cause: a vendor's message, a child process's stderr) through r.
type Renderer interface {
	RenderRedacted(r *Redactor) string
}

// A pattern redactor cannot tell a recovery instruction ("reload the plugin") from
// a leaked value, and when it runs over its own application's messages it eats
// them. Guidance is the application saying which text is its own: it is kept, and
// only a cause is redacted. The arguments of Guidance must be names (an operation,
// a field, an id), never values from a provider or a user: the rules never run over them.

type guidance struct {
	text  string
	cause error
}

// Guidance returns an error whose text the application composed. Its format
// arguments must be names, never secrets: the text is not redacted.
func Guidance(format string, args ...any) error {
	return &guidance{text: fmt.Sprintf(format, args...)}
}

// GuidanceWrap is Guidance with a cause. errors.Is and errors.As reach the cause;
// when rendered through a Redactor the prose is kept and the cause is redacted.
func GuidanceWrap(cause error, format string, args ...any) error {
	return &guidance{text: fmt.Sprintf(format, args...), cause: cause}
}

func (g *guidance) Error() string {
	if g.cause != nil {
		return g.text + ": " + g.cause.Error()
	}
	return g.text
}
func (g *guidance) Unwrap() error { return g.cause }
func (g *guidance) RenderRedacted(r *Redactor) string {
	if g.cause != nil {
		return g.text + ": " + r.ErrorText(g.cause)
	}
	return g.text
}

// ErrorText renders err for an operator: the prose of a [Renderer] as written,
// everything else redacted.
func (r *Redactor) ErrorText(err error) string {
	if err == nil {
		return ""
	}
	var rd Renderer
	if errors.As(err, &rd) && err == error(rd.(error)) { //nolint:errorlint // only an error that is itself a Renderer: a wrapper's own text is not trusted prose
		return rd.RenderRedacted(r)
	}
	return r.Text(err.Error())
}

type redactedError struct {
	text string
	err  error
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }

// Error returns err with its text redacted; errors.Is and errors.As still reach
// the original. An error with nothing to redact is returned as it was.
func (r *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	got := r.ErrorText(err)
	if got == err.Error() {
		return err
	}
	return &redactedError{text: got, err: err}
}
