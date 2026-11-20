package steer

import "time"

// Option configures [New] or [NewReflexes].
type Option func(*options)

type options struct {
	now       func() time.Time
	cooldowns CooldownStore
	policy    Policy
	policySet bool
	agentID   string
	fill      StateFill
}

func collect(opts []Option) options {
	o := options{now: time.Now, policy: DefaultPolicy()}
	for _, apply := range opts {
		apply(&o)
	}
	if o.cooldowns == nil {
		o.cooldowns = NewMemoryCooldowns()
	}
	return o
}

// WithClock sets the clock cooldowns and mode times use; nil keeps time.Now.
func WithClock(now func() time.Time) Option {
	return func(o *options) {
		if now != nil {
			o.now = now
		}
	}
}

// WithCooldowns sets where reflex cooldowns are kept. A host that persists it keeps them
// across restarts. The default is a fresh [MemoryCooldowns].
func WithCooldowns(c CooldownStore) Option { return func(o *options) { o.cooldowns = c } }

// WithPolicy sets the classifier policy; [New] validates it. The default is
// [DefaultPolicy].
func WithPolicy(p Policy) Option {
	return func(o *options) { o.policy, o.policySet = p.clone(), true }
}

// WithAgentID names the agent in the reflex library's state (State.AgentID).
func WithAgentID(id string) Option { return func(o *options) { o.agentID = id } }

// WithStateFill adds host facts to the reflex library's state before predicates run.
func WithStateFill(f StateFill) Option { return func(o *options) { o.fill = f } }
