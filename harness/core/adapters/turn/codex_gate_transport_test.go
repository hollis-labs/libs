//go:build !windows

package turn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/libs/harness/core/adapters/agentsessions"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

// Exercise the real async runtime -> gate adapter -> real HITL fixture -> native
// wire response over the existing fake Codex subprocess, without a live CLI.
func TestCodexGateStructuredTransport(t *testing.T) {
	for _, method := range []string{CodexCommandApprovalMethod, CodexElicitationMethod} {
		t.Run(method, func(t *testing.T) {
			r, _, facts, native, claims := codexGateFixture(t, method)
			dir := t.TempDir()
			logPath := filepath.Join(dir, "frames.log")
			runtime, err := agentsessions.NewFromAdapter(agentsessions.AdapterRuntimeConfig{
				ID: "fake-codex-gate", Kind: "cli", Adapter: fakeCodexAdapter{binary: os.Args[0]},
				Caps: agentsessions.Capabilities{JsonRpcStdio: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			observed := make(chan agentsessions.JsonRpcReplyStatus, 1)
			session, err := runtime.Start(context.Background(), agentsessions.StartOptions{
				Workdir: dir, LogPath: filepath.Join(dir, "session.log"),
				Env: append(os.Environ(), fakeCodexEnv+"=1", "FAKE_CODEX_LOG="+logPath,
					"FAKE_CODEX_APPROVAL_METHOD="+method, "FAKE_CODEX_APPROVAL_PARAMS="+string(native.Params)),
				JsonRpcAttemptBinding:   func(int) (runtimecontract.Binding, error) { return facts.Request.Binding, nil },
				JsonRpcRequestResponder: r.Respond,
				JsonRpcReplyObserver: func(_ agentsessions.JsonRpcServerRequest, status agentsessions.JsonRpcReplyStatus, err error) {
					if err != nil {
						t.Error(err)
					}
					observed <- status
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Stop(context.Background()); _, _ = session.Wait() })
			rpc := session.(agentsessions.JsonRpcCaller)
			if _, err = rpc.Call(fakeCtx(t), "turn/start", map[string]string{"threadId": "thread-1"}); err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-observed:
				if status != agentsessions.JsonRpcReplySent || *claims != 1 {
					t.Fatal("native reply lacked exact grant claim or transport write")
				}
			case <-fakeCtx(t).Done():
				t.Fatal("missing delivery observation")
			}
			frames := (fakeCodex{logPath: logPath}).frames(t)
			found := false
			for _, frame := range frames {
				if string(frame.ID) == `"srv-1"` && frame.Method == "" {
					found = true
					if len(frame.Error) > 0 || len(frame.Result) == 0 {
						t.Fatalf("native response refused: %+v", frame)
					}
				}
			}
			if !found {
				t.Fatal("fake provider never received structured approval response")
			}
		})
	}
}
