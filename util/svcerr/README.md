# go-svcerr

Small transport-agnostic typed error carrier: status, machine code, safe message, with errors.Is/As support.

## Install

```sh
go get github.com/hollis-labs/go-svcerr
```

## Usage

```go
package main

import (
	"fmt"

	svcerr "github.com/hollis-labs/go-svcerr"
)

func main() { fmt.Println(svcerr.Hello()) }
```

The same program lives in [`examples/hello`](./examples/hello/main.go).

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there.

## Out of scope

- TODO(author): what this library deliberately does not do (the cheapest defence against scope-creep reports).

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
