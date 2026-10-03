package federation_test

import (
	"fmt"

	"github.com/hollis-labs/substrate/mesh/federation"
	"github.com/hollis-labs/substrate/mesh/messaging/memstore"
)

// The default operation set is Torque's; Inbox and Subscribe stay off until an
// adopter widens it.
func ExampleDefaultOpSet() {
	fmt.Println(federation.DefaultOpSet().Ops())
	// Output: [send get thread consume cancel]
}

func ExampleOpSet_Widen() {
	wide, err := federation.DefaultOpSet().Widen(federation.OpInbox)
	fmt.Println(wide.Allows(federation.OpInbox), federation.DefaultOpSet().Allows(federation.OpInbox), err)
	// Output: true false <nil>
}

// A peer is pinned by the SHA-256 fingerprint of its certificate, in either the
// bare or the openssl colon form.
func ExampleNewPeerRegistry() {
	reg, err := federation.NewPeerRegistry([]federation.PeerConfig{{
		Label:        "hollis-b",
		Fingerprints: []string{"AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB"},
		Authorities:  []string{"b.example"},
	}})
	if err != nil {
		panic(err)
	}
	peer, ok := reg.Lookup("abababababababababababababababababababababababababababababababab")
	fmt.Println(ok, peer.Label, peer.IsAuthoritative("b.example"), peer.IsAuthoritative("a.example"))
	// Output: true hollis-b true false
}

// With no config file federation is off: Enable returns a nil *Federation and
// nothing runs.
func ExampleEnable() {
	f, err := federation.Enable("no-such-federation.json", memstore.New())
	fmt.Println(f == nil, err)
	// Output: true <nil>
}
