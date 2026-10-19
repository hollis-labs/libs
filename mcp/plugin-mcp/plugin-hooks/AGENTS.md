# plugin-hooks

Read doc.go, catalog.go and registry.go before changing behavior.

The core is standard-library only. No replace directives or go.work. plugin-sdk
may import this module; this module must never import plugin-sdk or go-hooks.
Hosts own identity, authorization, compiled schema validators and transport.
No application-specific exported APIs, compatibility aliases or implicit policies.

Checks: GOWORK=off go vet ./..., golangci-lint run --allow-parallel-runners,
and GOWORK=off go test -race -count=1 ./... at final review.
Run ./scripts/check.sh for the full gate; it is slow and computationally heavy.
Repeat concurrency-heavy lifecycle tests with -race -count=20 at final review.
Fast package tests without -race while working.

Publishing, repository creation and tags require owner approval. Do not commit
secrets, private hostnames or local filesystem paths. Every release needs a
CHANGELOG heading before tagging.
