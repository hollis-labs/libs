package tether_test

import (
	"context"
	"log"
	"os"

	tether "github.com/hollis-labs/go-tether-client"
)

// ExampleResolveSessionBootstrap shows the launch/host-boundary shape:
// resolve (and best-effort register) a session's canonical identity, then
// inject it into a child process's environment regardless of whether
// Tether happened to be reachable.
func ExampleResolveSessionBootstrap() {
	ctx := context.Background()
	client := tether.MustNew("tcp:127.0.0.1:7180")

	res, err := tether.ResolveSessionBootstrap(ctx, client, tether.BootstrapOptions{
		LogicalAgentID: "agt_worker",
	})
	if err != nil {
		log.Fatal(err) // only a local mistake reaches here, never daemon unreachability
	}

	_ = os.Setenv("SESSION", res.SessionID)
	if !res.Registered {
		log.Printf("tether unreachable, continuing offline: %v", res.RegisterErr)
	}
}
