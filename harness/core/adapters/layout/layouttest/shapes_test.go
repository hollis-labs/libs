package layouttest_test

import (
	"github.com/hollis-labs/libs/harness/core/adapters/layout"
	"github.com/hollis-labs/libs/llm/core/contracts/runtimes"
)

// The launch shapes the tests exercise. Claude print, Codex exec, OpenCode run
// and Antigravity print are all one shape: subprocess-per-turn.
var (
	perTurn = layout.Shape{Mode: runtimes.ModeSubprocessPerTurn}
	bare    = layout.Shape{Mode: runtimes.ModeSubprocessPerTurn, Variant: layout.VariantBare}
	jsonRPC = layout.Shape{Mode: runtimes.ModeJSONRPCStdio}
)
