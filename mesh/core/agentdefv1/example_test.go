package agentdef_test

import (
	"errors"
	"fmt"
	"testing/fstest"

	agentdef "github.com/hollis-labs/go-agentdef"
)

const triage = `---
name: incident-triage
description: Investigates and triages production incidents from an alert payload.
requires: [mcp]
---
You triage incidents.
`

func ExampleParse() {
	d, err := agentdef.Parse([]byte(triage))
	if err != nil {
		fmt.Println("parse:", err)
		return
	}
	if err := d.Validate(); err != nil {
		fmt.Println("validate:", err)
		return
	}
	digest, _ := agentdef.Digest(d)
	fmt.Println(d.Name, digest[:7], len(digest))
	// Output: incident-triage sha256: 71
}

func ExampleParse_unknownField() {
	_, err := agentdef.Parse([]byte("---\nname: a\ndescription: d\nmodel: gpt-4\n---\n"))
	fmt.Println(err != nil)
	// Output: true
}

func ExampleDefinition_Validate() {
	d := &agentdef.Definition{Name: "Bad_Name", Description: "d", Uses: []string{"telepathy"}}
	known := func(n string) bool { return n == "mcp" }
	err := d.Validate(agentdef.WithCapabilities(known))
	var ve *agentdef.ValidationError
	if errors.As(err, &ve) {
		for _, fe := range ve.Errors {
			fmt.Println(fe.Field)
		}
	}
	// Output:
	// name
	// uses[0]
}

func ExampleLoadLayers() {
	def := func(body string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\nname: triage\ndescription: d\n---\n" + body)}
	}
	_, err := agentdef.LoadLayers([]agentdef.Layer{
		{FS: fstest.MapFS{"triage.md": def("team")}, Name: "team"},
		{FS: fstest.MapFS{"triage.md": def("user")}, Name: "user"},
	})
	var ce *agentdef.CollisionError
	if errors.As(err, &ce) {
		fmt.Println(ce.Name, ce.Layers)
	}
	// Output: triage [team user]
}

func ExampleCheckGenerated() {
	fsys := fstest.MapFS{"shared/rules.md": {Data: []byte("rules v2")}}
	d := &agentdef.Definition{Body: "<!-- agentdef:generated source=shared/rules.md hash=sha256:stale -->\nold text\n<!-- /agentdef:generated -->"}
	stale, err := agentdef.CheckGenerated(fsys, ".", d)
	fmt.Println(len(stale), err)
	// Output: 1 <nil>
}

func ExampleLint() {
	d := &agentdef.Definition{Name: "a", Description: "Code reviewer", Tools: []string{"read", "read"}, Body: "Review code."}
	for _, f := range agentdef.Lint(d) {
		fmt.Println(f.Rule)
	}
	// Output:
	// duplicate-entry
	// description-label
}
