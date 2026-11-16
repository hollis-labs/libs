package agentdir

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/agent/sdk"
)

// SDKFactory adapts the directory host to Tradecraft's real Operative. The
// embedding host supplies ports and a fully enrolled input; this layer never
// mints a binding, environment receipt, grant or run ID.
type SDKFactory struct {
	Ports func(context.Context, Snapshot, string) (sdk.Ports, error)
	Input func(context.Context, Snapshot, string, string) (sdk.Input, error)
}

func (f SDKFactory) New(_ context.Context, s Snapshot, conversationID string) (Runner, error) {
	if f.Ports == nil || f.Input == nil || conversationID == "" {
		return nil, fmt.Errorf("agentdir: SDK ports, input builder and conversation required")
	}
	return sdkRunner{factory: f, snapshot: s, conversationID: conversationID}, nil
}

type sdkRunner struct {
	factory        SDKFactory
	snapshot       Snapshot
	conversationID string
}

func (r sdkRunner) Run(ctx context.Context, text string) (sdk.Result, error) {
	ports, err := r.factory.Ports(ctx, r.snapshot, r.conversationID)
	if err != nil {
		return sdk.Result{}, err
	}
	in, err := r.factory.Input(ctx, r.snapshot, r.conversationID, text)
	if err != nil {
		return sdk.Result{}, err
	}
	if string(in.Binding.ConversationID) != r.conversationID || in.Text != text {
		return sdk.Result{}, fmt.Errorf("agentdir: input binding or text differs from admitted conversation")
	}
	op, err := sdk.New(r.snapshot.Definition, ports)
	if err != nil {
		return sdk.Result{}, err
	}
	return op.Run(ctx, in)
}
