package snapshot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hollis-labs/substrate/harness/workspace/snapshot"
)

func TestConsumerRestoreCannotUseUnissuedProviderOrReceipt(t *testing.T) {
	config := consumerConfig(t)
	intent := snapshot.CaptureIntent{SetID: "set", OperationID: "restore", RunID: "run", InputDigest: strings.Repeat("1", 64), TargetMapDigest: config.Targets.Digest(), PolicyRevision: "consumer-v1", InstanceID: "instance", BindingFence: "fence", ControllerEpoch: 1, BootGeneration: "boot", RuntimeGeneration: "runtime"}
	request := snapshot.RestoreRequest{Intent: intent, Selections: []snapshot.RestoreSelection{{TargetID: "source", Path: "source.txt", Expected: snapshot.ExpectedFile{Present: true, SHA256: strings.Repeat("2", 64)}}}}
	for _, provider := range []*snapshot.GuardedProvider{nil, {}} {
		for _, retained := range []*snapshot.RetainedSet{nil, {}} {
			result, err := provider.RestoreSelective(context.Background(), retained, request)
			if !errors.Is(err, snapshot.ErrAdmissionUnavailable) || result.Outcome != "refused" || result.OperationID != "restore" || result.ChangedCount != 0 || result.Partial || len(result.Obligations) != 0 {
				t.Fatalf("unissued restore reached effects or invented issued pins: %+v %v", result, err)
			}
			assertConsumerUnchanged(t, config)
		}
	}
	// These refusals establish only public admission behavior. They do not
	// exercise hash conflict, writer exclusion or post-effect accounting.
}
