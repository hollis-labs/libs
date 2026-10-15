package main

import (
	"fmt"

	"github.com/hollis-labs/substrate/mesh/federation"
	"github.com/hollis-labs/substrate/mesh/messaging/memstore"
)

func main() {
	// With no federation.json, federation is off and nothing runs: no listener,
	// no foreign routes, no extra attack surface.
	f, err := federation.Enable("federation.json", memstore.New())
	fmt.Println(f == nil, err)

	// The shipped operation set is Torque's surface. Inbox and Subscribe are off
	// until an adopter widens the set on purpose.
	fmt.Println(federation.DefaultOpSet().Ops())
	wide, _ := federation.DefaultOpSet().Widen(federation.OpInbox)
	fmt.Println(wide.Allows(federation.OpInbox), federation.DefaultOpSet().Allows(federation.OpInbox))
}
