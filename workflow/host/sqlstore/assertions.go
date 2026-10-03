package sqlstore

import workflowruntime "github.com/hollis-labs/libs/workflow/runtime"

// Compile-time proof that Store implements every go-workflow runtime store
// interface. A new runtime.*Store method in a go-workflow bump must be
// implemented here (a minor bump of this module).
var (
	_ workflowruntime.StateStore             = (*Store)(nil)
	_ workflowruntime.CancellationStore      = (*Store)(nil)
	_ workflowruntime.ChildTerminalWaitStore = (*Store)(nil)
	_ workflowruntime.ControlFlowStore       = (*Store)(nil)
	_ workflowruntime.ExternalOperationStore = (*Store)(nil)
	_ workflowruntime.FanOutStore            = (*Store)(nil)
	_ workflowruntime.MemoStore              = (*Store)(nil)
	_ workflowruntime.PinStore               = (*Store)(nil)
	_ workflowruntime.OutputReuseStore       = (*Store)(nil)
	_ workflowruntime.ValueRecordStore       = (*Store)(nil)
	_ workflowruntime.ReactorStore           = (*Store)(nil)
	_ workflowruntime.RecoveryStore          = (*Store)(nil)
	_ workflowruntime.ReplayStore            = (*Store)(nil)
	_ workflowruntime.NodeInputStore         = (*Store)(nil)
	_ workflowruntime.RetryStore             = (*Store)(nil)
	_ workflowruntime.RunControlStore        = (*Store)(nil)
	_ workflowruntime.RunPolicyStore         = (*Store)(nil)
	_ workflowruntime.SchedulerResourceStore = (*Store)(nil)
	_ workflowruntime.ServiceStore           = (*Store)(nil)
	_ workflowruntime.WaitStore              = (*Store)(nil)
	_ workflowruntime.CompensationStore      = (*Store)(nil)
)
