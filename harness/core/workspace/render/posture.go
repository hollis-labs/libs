package render

import (
	claude "github.com/hollis-labs/substrate/harness/adapters/claude/nativefiles"
	layout "github.com/hollis-labs/substrate/harness/adapters/layout/plan"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
	"github.com/hollis-labs/substrate/harness/interception/permission"
	"github.com/hollis-labs/substrate/llm-core/contracts/runtimes"
)

// nativePolicy is selected once from the bound mode that also travels in the
// runtime reference. It encodes no launch flags; registry owns that projection.
type nativePolicy struct {
	claudeMode, approval, sandbox string
	openCode                      map[string]string
}

var nativePolicies = map[permission.Mode]nativePolicy{
	permission.ModeDefault:     {claudeMode: "default", approval: "on-request", sandbox: "read-only", openCode: map[string]string{"edit": "ask", "bash": "ask"}},
	permission.ModeAcceptEdits: {claudeMode: "acceptEdits", approval: "on-request", sandbox: "workspace-write", openCode: map[string]string{"edit": "allow", "bash": "ask"}},
	permission.ModePlan:        {claudeMode: "plan", approval: "never", sandbox: "read-only", openCode: map[string]string{"edit": "deny", "bash": "ask"}},
	permission.ModeYolo:        {claudeMode: "bypassPermissions", approval: "never", sandbox: "danger-full-access", openCode: map[string]string{"edit": "allow", "bash": "allow", "webfetch": "allow", "external_directory": "allow", "doom_loop": "allow"}},
}

func nativePosture(req *Request, out *Result) error {
	if out.Binding.Posture == nil || out.Binding.Posture.Posture == "" {
		return nil
	}
	mode := out.Binding.Posture.Posture
	for _, parts := range req.Native.OperatorKeyPaths {
		if len(parts) == 0 {
			continue
		}
		policyKey := req.Provider == runtimes.Claude && parts[0] == "permissions" || req.Provider == runtimes.Codex && (parts[0] == "approval_policy" || parts[0] == "sandbox_mode" || parts[0] == "sandbox_workspace_write") || req.Provider == runtimes.OpenCode && parts[0] == "permission"
		if policyKey {
			return refuse(*req, layout.Permissions, "host_policy_mixture", "bound native permission policy cannot be annotated as unowned")
		}
	}
	policy, ok := nativePolicies[mode]
	if !ok {
		return refuse(*req, layout.Permissions, "invalid_posture", "resolved permission mode has no native mapping")
	}
	mixture := false
	switch req.Provider {
	case runtimes.Claude:
		mixture = req.Native.Claude.Permission != nil
		for _, slot := range req.Native.Claude.Slots {
			if slot.Key == "permissions" {
				mixture = true
			}
		}
		if !mixture {
			req.Native.Claude.Permission = &claude.Permission{DefaultMode: policy.claudeMode}
		}
	case runtimes.Codex:
		mixture = req.Native.Codex.ApprovalPolicy != "" || req.Native.Codex.SandboxMode != "" || len(req.Native.Codex.WritableRoots) > 0
		for _, slot := range req.Native.Codex.Slots {
			if slot.Key == "approval_policy" || slot.Key == "sandbox_mode" || slot.Key == "sandbox_workspace_write" {
				mixture = true
			}
		}
		if !mixture {
			req.Native.Codex.ApprovalPolicy = policy.approval
			req.Native.Codex.SandboxMode = policy.sandbox
		}
	case runtimes.OpenCode:
		for _, slot := range req.Native.OpenCode.Slots {
			if slot.Key == "permission" || slot.Key == "agent" {
				mixture = true
			}
		}
		if !mixture {
			req.Native.OpenCode.Slots = append(append([]contract.Slot(nil), req.Native.OpenCode.Slots...), contract.Slot{Key: "permission", Value: policy.openCode})
		}
	case runtimes.Antigravity:
		out.Diagnostics = append(out.Diagnostics, Diagnostic{Code: "native_permission_omitted", Provider: req.Provider, Mode: req.Mode, Concern: layout.Permissions, Posture: mode, Reason: "no evidenced native permission file; runtime posture reference only"})
	}
	if mixture {
		return refuse(*req, layout.Permissions, "host_policy_mixture", "posture reference cannot also carry host-supplied native permission settings")
	}
	return nil
}
