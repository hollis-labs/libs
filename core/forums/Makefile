.PHONY: check vet lint test-race vuln

check: vet lint test-race vuln

vet:
	go vet ./...

lint:
	golangci-lint run

test-race:
	go test -race ./...

vuln:
	govulncheck ./...
