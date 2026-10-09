package main

import (
	"fmt"
	"log"

	agentdef "github.com/hollis-labs/go-agentdef"
)

const file = `---
name: incident-triage
description: Investigates and triages production incidents from an alert payload.
requires: [mcp]
---
You triage incidents. Start from the alert payload.
`

func main() {
	d, err := agentdef.Parse([]byte(file))
	if err != nil {
		log.Fatal(err)
	}
	if err = d.Validate(); err != nil {
		log.Fatal(err)
	}
	digest, err := agentdef.Digest(d)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(d.Name, digest)
}
