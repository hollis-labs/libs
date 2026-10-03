# Extraction history provenance

This repository was extracted from the `workflow/` subtree of
`github.com/hollis-labs/hadron` at source checkpoint
`c69676ae0cde4baafcc60b31e9269998003a55c7` on 2026-09-04.

The filtered module-root checkpoint is
`02381d0a75ceb1519694c2a60046f875cba073e3`. The extraction retains 59 useful
workflow commits. It selected the engine subtree, graph-native workflow
examples, the engine adoption guide, architecture decisions 0006, 0007, 0008,
0009, 0010, and 0013, plus the MIT license; then promoted `workflow/` to the
repository root while preserving `examples/workflow/` so persisted source
locators and their goldens do not drift.

History was rewritten from a disposable clone with `git-filter-repo` 2.47.0
(tool revision `a40bce548d2c`). The filter changes repository paths and commit
IDs, but not the engine's stable `HADR-*` diagnostics, schemas, wire values,
state/event semantics, or digest algorithms. Hadron release tags were removed
from this repository because they identify a different Go module.

The first standalone-module commit adds `go.mod`, rewrites Go import paths to
`github.com/hollis-labs/go-workflow`, and adds standalone release guards and
documentation. Source checkpoint and filtered checkpoint IDs above are the
review anchors for future forensic comparison.
