.PHONY: test race tangent-check

export GOWORK := off

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

# Opt-in cross-check against Tangent's schema (read only). Fails if the check
# was skipped, so a skip can never be mistaken for a pass:
#   make tangent-check HITL_TANGENT_SCHEMA=/abs/path/to/request.schema.json
tangent-check:
	@test -n "$(HITL_TANGENT_SCHEMA)" || { echo "set HITL_TANGENT_SCHEMA to Tangent's request.schema.json"; exit 2; }
	@out=$$(mktemp) && HITL_TANGENT_SCHEMA="$(HITL_TANGENT_SCHEMA)" go test -count=1 -v -run Tangent ./schema | tee $$out; \
	status=$$?; if grep -q -- '--- SKIP' $$out; then echo "tangent-check: SKIPPED, not passed"; rm -f $$out; exit 1; fi; rm -f $$out; exit $$status
