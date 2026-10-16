package mw_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/hollis-labs/libs/core/util/mw"
)

type member = mw.Member[string, string]

// announce is a middleware that prints when a call enters and leaves it.
func announce(name string, phase mw.Phase, fm mw.FailureMode) member {
	return member{
		Desc: mw.Descriptor{Name: name, Phase: phase, Failure: fm},
		MW: func(next mw.Handler[string, string]) mw.Handler[string, string] {
			return func(ctx context.Context, in string) (string, error) {
				fmt.Println(name, "in")
				out, err := next(ctx, in)
				fmt.Println(name, "out")
				return out, err
			}
		},
	}
}

func ExampleAssemble() {
	stack, err := mw.Assemble(mw.Options{},
		announce("recover", mw.PhaseRecover, mw.Closed),
		announce("request-id", mw.PhaseTrace, mw.Open),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	h := stack.Wrap(func(context.Context, string) (string, error) {
		fmt.Println("handler")
		return "ok", nil
	})
	h(context.Background(), "request")
	// Output:
	// recover in
	// request-id in
	// handler
	// request-id out
	// recover out
}

func ExampleAssemble_rejectsABadOrder() {
	_, err := mw.Assemble(mw.Options{},
		announce("authn", mw.PhaseAuthn, mw.Closed),
		announce("request-id", mw.PhaseTrace, mw.Open), // tracing belongs outside authentication
	)
	fmt.Println(err)
	// Output:
	// request-id (#1): phase trace is outside phase authn; reorder
}

func ExampleStack_Explain() {
	stack, _ := mw.Assemble(mw.Options{},
		announce("recover", mw.PhaseRecover, mw.Closed),
		announce("authn", mw.PhaseAuthn, mw.Closed),
	)
	fmt.Print(stack.Explain())
	// Output:
	// #   name             phase       failure mode     notes
	// 0   recover          recover     closed  enforce
	// 1   authn            authn       closed  enforce
}

func ExampleMember_shadow() {
	deny := member{
		Desc: mw.Descriptor{Name: "authz", Phase: mw.PhaseAuthz, Failure: mw.Closed},
		Mode: mw.Shadow, // evaluate and record, apply nothing
		MW: func(next mw.Handler[string, string]) mw.Handler[string, string] {
			return func(context.Context, string) (string, error) { return "", errors.New("denied") }
		},
	}
	stack, _ := mw.Assemble(mw.Options{Observer: func(e mw.Event) {
		fmt.Println(e.Name, e.Mode, e.Outcome)
	}}, deny)
	out, err := stack.Wrap(func(context.Context, string) (string, error) { return "served", nil })(context.Background(), "x")
	fmt.Println(out, err)
	// Output:
	// authz shadow denied
	// served <nil>
}
