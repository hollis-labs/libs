package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	agentcontext "github.com/hollis-labs/libs/agent/core/context"
	"github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/libs/llm/core/llmtypes"
	"github.com/hollis-labs/libs/mesh/core/agentdef"
)

// Operative is an immutable prepared definition with one active Run at a time.
// It does not persist history or mint identities. Separate instances still need
// a host input lease/current-binding admission if sharing a conversation.
type Operative struct {
	definition agentdef.Definition
	digest     string
	ports      Ports
	tools      map[string]Tool
	required   []string
	busy       atomic.Bool
}

// New validates and snapshots the definition, supported features, tool bindings
// and configuration without host/model/tool I/O. Run resolves/verifies all pins
// and host authority before any model/tool call.
func New(def agentdef.Definition, ports Ports) (*Operative, error) {
	if err := def.Validate(); err != nil {
		return nil, err
	}
	if len(def.Behavior.Hooks) > 0 || len(def.Requirements.Skills) > 0 {
		return nil, fmt.Errorf("%w: executable hooks and packaged skills", ErrUnsupported)
	}
	if ports.Environment == nil || ports.Admission == nil || ports.Policy == nil || ports.Events == nil {
		return nil, fmt.Errorf("%w: environment, admission, policy and events ports required", ErrInvalid)
	}
	required := slices.Clone(def.Requirements.Requires)
	for _, c := range required {
		if !slices.Contains(ports.Capabilities, c) {
			return nil, fmt.Errorf("%w: required host capability %s", ErrUnsupported, c)
		}
	}
	var err error
	def, err = clone(def)
	if err != nil {
		return nil, err
	}
	tools := map[string]Tool{}
	for _, tool := range ports.Tools {
		name := tool.Definition.Name
		if strings.TrimSpace(name) == "" || tool.Execute == nil || len(tool.Versions) == 0 {
			return nil, fmt.Errorf("%w: tool requires name, implementation and version pins", ErrInvalid)
		}
		if _, ok := tools[name]; ok {
			return nil, fmt.Errorf("%w: duplicate tool %s", ErrInvalid, name)
		}
		for _, ref := range tool.Versions {
			if err := ref.Validate(); err != nil {
				return nil, err
			}
		}
		tool.Definition, err = clone(tool.Definition)
		if err != nil {
			return nil, err
		}
		tool.Versions = slices.Clone(tool.Versions)
		tools[name] = tool
	}
	selected := map[string]Tool{}
	for _, name := range def.Requirements.Tools {
		tool, ok := tools[name]
		if !ok {
			return nil, fmt.Errorf("%w: required tool %s", ErrUnsupported, name)
		}
		if _, ok := selected[name]; ok {
			return nil, fmt.Errorf("%w: duplicate required tool", ErrInvalid)
		}
		selected[name] = tool
	}
	if slices.Contains(required, CapabilityApproval) && (ports.Gate.Port == nil || ports.Authority == nil) {
		return nil, fmt.Errorf("%w: mandatory approval support requires gate and authority ports", ErrUnsupported)
	}
	if len(selected) > 0 {
		if err := ports.Gate.Wait.Validate(); err != nil {
			return nil, err
		}
		if !slices.Contains(required, "tool.execute") {
			required = append(required, "tool.execute")
		}
	}
	if _, err := ports.Router.selectRoute("", len(selected) > 0); err != nil {
		return nil, err
	}
	if len(definitionRefs(def)) > 0 && ports.Resources == nil {
		return nil, fmt.Errorf("%w: pinned resources require resolver", ErrInvalid)
	}
	l := &ports.Limits
	if l.MaxTurns < 0 || l.MaxOutputTokens < 0 || l.IdleTimeout < 0 || l.ApprovalTTL < 0 || l.MemoryItems < 0 || l.MemoryBytes < 0 || l.MaxToolResultBytes < 0 {
		return nil, fmt.Errorf("%w: negative limits", ErrInvalid)
	}
	if l.MaxTurns == 0 {
		l.MaxTurns = 8
	}
	if l.MaxOutputTokens == 0 {
		l.MaxOutputTokens = 1024
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = time.Minute
	}
	if l.ApprovalTTL == 0 {
		l.ApprovalTTL = time.Minute
	}
	if l.MemoryItems == 0 {
		l.MemoryItems = 4
	}
	if l.MemoryBytes == 0 {
		l.MemoryBytes = 8192
	}
	if l.MaxToolResultBytes == 0 {
		l.MaxToolResultBytes = 64 * 1024
	}
	if ports.Sequencer == nil {
		ports.Sequencer = runtimeevents.NewSequencer()
	}
	if ports.Now == nil {
		ports.Now = time.Now
	}
	ports.Capabilities = slices.Clone(ports.Capabilities)
	ports.Tools = nil
	digest, err := agentdef.Digest(&def)
	if err != nil {
		return nil, err
	}
	return &Operative{definition: def, digest: digest, ports: ports, tools: selected, required: required}, nil
}

func clone[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	err = dec.Decode(&out)
	return out, err
}

type pinned struct {
	ref  agentdef.Ref
	slot string
}

func definitionRefs(d agentdef.Definition) []pinned {
	var refs []pinned
	add := func(slot string, rs ...agentdef.Ref) {
		for _, r := range rs {
			refs = append(refs, pinned{r, slot})
		}
	}
	add(agentcontext.SlotAgent, d.Behavior.Instructions...)
	add(agentcontext.SlotRules, d.Behavior.SOPs...)
	add(agentcontext.SlotRules, d.HarnessProfile.Steering...)
	add(agentcontext.SlotContext, d.HarnessProfile.Context.Sources...)
	add("", d.Requirements.Resources...)
	add("", d.HarnessProfile.Approvals...)
	add("", d.HarnessProfile.Escalation...)
	for _, ref := range []*agentdef.Ref{d.HarnessProfile.Context.Policy, d.Continuity.MemoryPolicy, d.Continuity.RecoveryStrategy} {
		if ref != nil {
			add("", *ref)
		}
	}
	for _, c := range d.Capabilities {
		for _, ref := range []*agentdef.Ref{c.Input, c.Output} {
			if ref != nil {
				add("", *ref)
			}
		}
	}
	return refs
}

func (o *Operative) prepare(ctx context.Context, in Input, window *agentcontext.ContextWindow) error {
	if err := o.verify(ctx, in); err != nil {
		return err
	}
	def, err := clone(o.definition)
	if err != nil {
		return err
	}
	if err = o.ports.Policy.CheckDefinition(ctx, def, in.Binding, in.Environment); err != nil {
		return err
	}
	content := map[string]string{agentcontext.SlotAgent: o.definition.Body}
	for _, pin := range definitionRefs(o.definition) {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := o.ports.Resources.Resolve(ctx, in.Binding, pin.ref)
		if err != nil {
			return err
		}
		if len(raw) > 1<<20 {
			return fmt.Errorf("%w: resource exceeds 1 MiB", ErrLimit)
		}
		if agentdef.ArtifactDigest(raw) != pin.ref.Digest {
			return fmt.Errorf("%w: resource digest mismatch", ErrInvalid)
		}
		if pin.slot != "" {
			if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
				return fmt.Errorf("%w: instruction resource must be text", ErrInvalid)
			}
			content[pin.slot] += "\n\n" + string(raw)
		}
	}
	for slot, text := range content {
		// Do not silently truncate mandatory instructions under the context library's
		// generic slot ceilings. This facade rejects oversized static content instead.
		s := window.Slot(slot)
		if slot != agentcontext.SlotAgent && s.MaxTokens > 0 && (agentcontext.DefaultEstimator{}).Estimate(text) > s.MaxTokens {
			return ErrContextBudget
		}
		window.SetContent(slot, text)
	}
	if o.ports.Memory != nil {
		var items []MemoryItem
		err := o.admit(ctx, Admission{Kind: "memory", Binding: in.Binding, Environment: in.Environment, DefinitionDigest: o.digest}, func(c context.Context) error {
			var e error
			items, e = o.ports.Memory.Read(c, MemoryQuery{Binding: in.Binding, Query: in.Text, MaxItems: o.ports.Limits.MemoryItems, MaxBytes: o.ports.Limits.MemoryBytes})
			return e
		})
		if err != nil {
			return err
		}
		encoded, encodeErr := json.Marshal(items)
		if encodeErr != nil {
			return encodeErr
		}
		if len(encoded) > o.ports.Limits.MemoryBytes {
			return fmt.Errorf("%w: memory serialized response bound", ErrLimit)
		}
		if len(items) > o.ports.Limits.MemoryItems {
			return fmt.Errorf("%w: memory item bound", ErrLimit)
		}
		var text strings.Builder
		for _, item := range items {
			if item.Binding != in.Binding || item.Reference.OwnerID != in.Binding.OwnerID {
				return fmt.Errorf("%w: memory scope mismatch", ErrAdmission)
			}
			if err := item.Reference.Validate(); err != nil {
				return err
			}
			text.WriteString(item.Content)
			text.WriteByte('\n')
			if text.Len() > o.ports.Limits.MemoryBytes {
				return fmt.Errorf("%w: memory byte bound", ErrLimit)
			}
		}
		s := window.Slot(agentcontext.SlotMemory)
		if (agentcontext.DefaultEstimator{}).Estimate(text.String()) > s.MaxTokens {
			return ErrContextBudget
		}
		window.SetContent(agentcontext.SlotMemory, text.String())
	}
	return nil
}

func (o *Operative) verify(ctx context.Context, in Input) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := in.Environment.ValidateAt(o.ports.Now(), o.required); err != nil {
		return err
	}
	if in.Environment.Receipt.OwnerID != in.Binding.OwnerID {
		return ErrAdmission
	}
	env, _ := clone(in.Environment)
	return o.ports.Environment.Verify(ctx, in.Binding, env, slices.Clone(o.required))
}

func (o *Operative) admit(ctx context.Context, a Admission, start func(context.Context) error) error {
	if err := o.verify(ctx, Input{Binding: a.Binding, Environment: a.Environment}); err != nil {
		return err
	}
	snapshot, err := clone(a)
	if err != nil {
		return err
	}
	var calls atomic.Int32
	var closed atomic.Bool
	var callbackErr error
	err = o.ports.Admission.Admit(ctx, snapshot, func(c context.Context) error {
		if closed.Load() || calls.Add(1) != 1 {
			return ErrAdmission
		}
		if ctx.Err() != nil {
			callbackErr = context.Cause(ctx)
			return callbackErr
		}
		if c.Err() != nil {
			callbackErr = context.Cause(c)
			return callbackErr
		}
		admitted, cancel := context.WithCancelCause(c)
		stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
		defer stop()
		defer cancel(nil)
		callbackErr = invoke(admitted, start)
		return callbackErr
	})
	closed.Store(true)
	if err != nil {
		return err
	}
	if calls.Load() != 1 {
		return ErrAdmission
	}
	return callbackErr
}

func toolDefinitions(tools map[string]Tool, names []string) []llmtypes.ToolDefinition {
	out := make([]llmtypes.ToolDefinition, 0, len(names))
	for _, name := range names {
		out = append(out, tools[name].Definition)
	}
	return out
}

// A panic after effect admission has unknown external outcome. Do not include
// recovered values in routine events or errors: they may contain tool inputs.
func invoke(ctx context.Context, start func(context.Context) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrEffectPanic
		}
	}()
	return start(ctx)
}
