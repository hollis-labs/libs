#!/bin/sh
set -eu
export GOWORK=off
go mod verify
go mod tidy -diff
if [ -n "$(gofmt -l .)" ]; then
  echo 'Go files require formatting'
  exit 1
fi
go vet ./...
golangci-lint run --allow-parallel-runners
go test -race -count=1 ./...
go test -race -count=20 -run 'Test(RemoteCompletedAncestorIsNotCycle|RemoteNotificationHalfOpenRemainsUnproven|RemoteCallbackDetachedDeadlineAndBindingFence|RemoteBatchTimeoutRetainsCapacity|RemoteBatchAsyncAndCommit|RemoteNotificationQueueAndNoBreakerSuccess|RemoteDeadlineRetainsCapacity|RemoteCancellationAndConnectionExcluded|RemotePanicCountsAndLatencyRefusalDoesNot|SharedConformance|ConcurrentOnce|HandlesAndScopedRemoval|ConcurrentDisposalAndRemoval|DisposeCancelsAndBoundsWait|RemovalBetweenSnapshotAndStart|RemoveInvalidatesActiveResult|CanceledStartDoesNotConsumeOnce|ParallelIsolationAndOutcomeOrder|ConcurrentDispatchOnce|TimeoutRetainsCapacity|HookParallelismRetainedAfterTimeout|AsyncDepthAndDetachedContext|QueueOverloadAndShutdown|QueuedUnloadPreventsStart|UnloadDiscardsLateFilterResult|AfterCommitRollbackAndOneShot|SuccessfulCompletionNotCanceled|RemovalCancelsCapacityWait|FutureResultsAreOwned|ConcurrentCatalogDiscoveryAndWarnings|ConcurrentBreakerHalfOpenProbe|ConcurrentBreakerResetAndLateCompletions|ConcurrentBreakerSuccessDoesNotResetOpen|BreakerTimeoutResetRetainsCapacity|BreakerCallerAndUnloadCancellationExcluded|TelemetryRecordsArePayloadFreeAndReentrant|NestedTraceAndDepthRejection|AsyncAndAfterCommitTracing)' .
go test -race -count=20 -run 'Test(Conformance|PluginHarness|RejectsBrokenAdapters|FaultProbeReference)$' ./hookstest
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
