module example.com/go-workflow-external-consumer

go 1.26.6

require github.com/hollis-labs/go-workflow v0.0.0

require (
	github.com/expr-lang/expr v1.17.8 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/text v0.14.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// Pre-release proof only. Release qualification must remove this replacement
// and resolve the exact immutable module tag through the Go module proxy.
replace github.com/hollis-labs/go-workflow => ../..
