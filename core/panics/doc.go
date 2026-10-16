// Package panics contains panics at the boundaries where they would otherwise
// take down a process or leave work half-done, and turns each one into a record
// that is safe to keep and a reference that is safe to hand to anyone.
//
// # The idea
//
// A recovered panic becomes an [Error]. Its text is "internal error (ref
// pnc_...)" and nothing else: the panic value can carry anything the failing
// code was holding (a secret, a user's input, a query), so it never crosses a
// trust boundary. The detail goes to the [Reporter] (a log line, a database row,
// both) where the operator looks it up by ref.
//
// The value and the stack are untrusted text. Before a [Record] is built they
// are passed through the [Config.Redact] function, stripped of control and
// format characters (a hostile value must not repaint a terminal) and bounded.
// The package itself has no dependencies and so cannot know what a secret looks
// like: with no Redact function configured the value is WITHHELD and only its
// type is kept. Wire a real redactor in the application's main package, or opt
// out explicitly with [Unredacted].
//
// # Where to use it
//
// Every place a panic would escape its owner:
//
//   - every goroutine: use [Go], never a bare "go func()" (an unrecovered panic in
//     any goroutine kills the whole process);
//   - every request, message or tool call a server handles ([Call], [Recover],
//     and the httpguard subpackage for net/http);
//   - code that decides for the caller (a parser, a policy) must fail closed:
//     wrap it with [Call] and treat a panic as "deny" or "needs a human";
//   - an executor that holds resources (a child process, a lock, a row marked
//     "running"): recover, release, and record the failure on the owning record;
//   - callbacks into code you do not own, especially ones a library invokes from
//     its own goroutine, where you cannot recover from outside;
//   - the outermost start-up path, so even a panic during wiring ends in a ref
//     and exit code [ExitSoftware] instead of a stack dump.
//
// # Example
//
//	panics.SetDefault(panics.Config{
//		Reporter: panics.StderrReporter(os.Stderr),
//		Redact:   myRedactor.Text, // e.g. the application's standard redactor
//	})
//
//	panics.Go("reaper", reapLoop, nil)
//
//	func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
//		var err error
//		defer panics.Recover("handle "+r.Pattern, &err)
//		...
//	}
package panics
