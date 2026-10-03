// cairn-shim hosts one authorized stdio child and serves its private journal.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hollis-labs/substrate/harness/shim"
)

func main() {
	descriptor := flag.String("launch", "", "private launch descriptor")
	flag.Parse()
	if *descriptor == "" {
		fmt.Fprintln(os.Stderr, "--launch is required")
		os.Exit(2)
	}
	launch, err := shim.ReadLaunch(*descriptor)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid launch descriptor")
		os.Exit(1)
	}
	host, err := shim.Start(launch)
	if err != nil {
		code := "internal_error"
		if typed, ok := err.(*shim.Error); ok {
			code = typed.Code
		}
		fmt.Fprintln(os.Stderr, "shim launch failed: "+code)
		os.Exit(1)
	}
	defer host.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)
	// Keep the control socket available after child exit for durable inspection.
	// Host shutdown is explicit; a control connection never owns host lifetime.
	<-signals
}
