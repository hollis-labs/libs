package transportparity

import (
	"fmt"
	"strings"
)

// recorder is a T that records failures instead of failing the real test, so the
// package can assert on its own failure messages.
type recorder struct {
	errors []string
	fatal  bool
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
	r.fatal = true
}
func (r *recorder) failed() bool { return len(r.errors) > 0 }
func (r *recorder) text() string { return strings.Join(r.errors, "\n") }
