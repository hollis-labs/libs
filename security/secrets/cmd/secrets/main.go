// Command secrets stores and checks secret references.
//
//	secrets set <ref>      read the value from stdin and store it
//	secrets check <ref>    resolve <ref>; print the source, never the value
//	secrets delete <ref>   remove the entry
//
// The value is read from stdin only. It is never an argument (argv is visible
// to every process of the user) and is never printed.
//
//	printf %s "$KEY" | secrets set file://openai/work     # headless Linux
//	secrets set keychain://openai/work < keyfile          # desktop
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	secrets "github.com/hollis-labs/libs/security/secrets"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, secrets.New()))
}

const usage = `usage:
  secrets set <ref>      store the value read from stdin
  secrets check <ref>    resolve <ref> and print its source (never the value)
  secrets delete <ref>   remove the entry
`

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, r *secrets.Resolver) int {
	if len(args) != 2 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, ref := args[0], args[1]
	var err error
	switch cmd {
	case "set":
		if isTerminal(stdin) {
			fmt.Fprintln(stderr, "secrets set: stdin is a terminal; pipe or redirect the value (it is never taken from arguments)")
			return 2
		}
		if err = r.Set(ctx, ref, stdin); err == nil {
			fmt.Fprintf(stdout, "stored %s\n", ref)
		}
	case "check":
		var res secrets.Resolved
		if res, err = r.Resolve(ctx, ref); err == nil {
			fmt.Fprintf(stdout, "ok %s source=%s\n", res.Ref, res.Source)
		}
	case "delete":
		if err = r.Delete(ctx, ref); err == nil {
			fmt.Fprintf(stdout, "deleted %s\n", ref)
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "secrets %s: %v\n", cmd, err)
		var e *secrets.Error
		if errors.As(err, &e) {
			if g := e.Guidance(); g != "" {
				fmt.Fprintln(stderr, g)
			}
		}
		return 1
	}
	return 0
}

// isTerminal reports whether in is an interactive terminal. A value typed at a
// prompt would be echoed, so the command refuses it.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
