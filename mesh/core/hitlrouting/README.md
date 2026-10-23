# hitlrouting

Routing profile for cards and human-in-the-loop requests: which sink receives a request, and how.
A thin layer over [`core/util/router`](../../../core/util/router): it adds the field vocabulary
(`kind`, `source`, `tool`, `namespace`), a sink id as the target, two closed parameters
(`completion`: `wait`, `async`, `notify`; `delivery`: `durable_message`, `await`) and a mandatory
`catch_all`. See `doc.go` for the rule file and the matching rules, and `example_test.go` for a
runnable example.

It is not: a delivery mechanism (the host maps a sink id to its own delivery), a second router
(compose with the messaging router and federation for cross-host sends), a policy engine (pass a
`router.Policy` in the config if you need one), or part of the go-hitl lifecycle (it changes no
state and decides no acknowledgement behavior).

```sh
export GOWORK=off
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```
