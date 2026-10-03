.PHONY: check generate race test test-external tidy-check vet

check: generate tidy-check test test-external race vet

generate:
	go generate ./...
	git diff --exit-code -- graph/schema/workflow.schema.json compile/schema/execution-plan.schema.json

tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

test:
	go test ./...

test-external:
	cd testdata/external-consumer && go mod tidy
	git diff --exit-code -- testdata/external-consumer/go.mod testdata/external-consumer/go.sum
	cd testdata/external-consumer && go test ./...

race:
	go test -race ./...

vet:
	go vet ./...
