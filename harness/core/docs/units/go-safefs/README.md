# go-safefs

Filesystem safety primitives: path confinement under a root and crash-safe atomic writes.

There is no root package. Import the sub-package you need: `pathsafe` for
confinement, `atomicfile` for atomic writes. They are independent and neither
imports the other. The module has no dependencies beyond the Go standard library.

## Status

**Pre-release.** This project is unreleased, not deployed, and has no outside consumers. It's being built in the open: the code, the docs, and this README describe what exists today, not a pitch for what's planned. Interfaces and behavior change without notice, and there are no compatibility guarantees yet.

## Install

```sh
go get github.com/hollis-labs/go-safefs/pathsafe
go get github.com/hollis-labs/go-safefs/atomicfile
```

## pathsafe

`pathsafe.ResolveUnder(root, userPath)` cleans `userPath`, joins it under
`root`, resolves symlinks, and returns an absolute path that is guaranteed to
live under `root`, or an `*pathsafe.EscapeError`. A leaf that does not exist yet
is handled by resolving its longest existing ancestor, so you can validate the
target of a file you are about to create.

```go
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/hollis-labs/go-safefs/pathsafe"
)

func main() {
	root, err := os.MkdirTemp("", "root-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	p, err := pathsafe.ResolveUnder(root, "notes/todo.txt")
	fmt.Println(p != "", err)

	_, err = pathsafe.ResolveUnder(root, "../../etc/passwd")
	var esc *pathsafe.EscapeError
	fmt.Println(errors.As(err, &esc))
}
```

## atomicfile

`atomicfile.WriteFile` writes to a sibling temp file, fsyncs it, sets the mode,
renames it over the destination, then fsyncs the parent directory. Readers never
see a partial file. `atomicfile.NewWriter` does the same for streaming writes and
returns a `*atomicfile.Writer` with `Write`, `Close` (publish) and `Abort`
(discard).

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hollis-labs/go-safefs/atomicfile"
)

func main() {
	dir, err := os.MkdirTemp("", "atomicfile-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "config.json")
	if err := atomicfile.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		panic(err)
	}

	w, err := atomicfile.NewWriter(filepath.Join(dir, "stream.txt"), 0o600)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(w, "streamed")
	if err := w.Close(); err != nil {
		panic(err)
	}

	got, _ := os.ReadFile(path)
	fmt.Println(string(got))
}
```

## Compatibility

This module is pre-1.0: minor releases may break the exported API. Pin an exact
version, and read [CHANGELOG.md](./CHANGELOG.md) before upgrading — every
breaking change is listed there.

## Out of scope

- OS-level sandboxing (process isolation, syscalls, namespaces): that is `go-sandbox`'s job. `pathsafe` only answers whether a path lives under a root.
- Content-addressed artifact storage, manifests and pinned-root identity checks: that is `go-workflow-host/artifactfs`'s job. `pathsafe/doc.go` describes its `os.SameFile` root-pinning technique as a pattern only.
- Panic recovery for goroutines: Nanite's `safego`, unrelated to the filesystem and not part of this module.
- Generic directory-tree helpers (copy, walk, move) and other grab-bag file utilities beyond confinement and atomic writes.

## Development

```sh
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```

CI (`.github/workflows/check.yml`) is the full gate.

## License

MIT — see [LICENSE](./LICENSE).
