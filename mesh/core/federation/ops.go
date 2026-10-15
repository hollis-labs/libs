package federation

import (
	"errors"
	"fmt"
	"sort"
)

// Op is one go-messaging.Store operation that can cross the hop between two
// installs.
type Op string

// The operations. Only the first five are on by default.
const (
	OpSend    Op = "send"
	OpGet     Op = "get"
	OpThread  Op = "thread"
	OpConsume Op = "consume"
	OpCancel  Op = "cancel"
	// OpInbox and OpSubscribe drain and stream a whole mailbox. They are the
	// bigger trust concession: Torque judged bulk mailbox access a larger step
	// than fetching one envelope by id, and never mounts them. They are off in
	// DefaultOpSet and an adopter has to turn them on deliberately.
	OpInbox     Op = "inbox"
	OpSubscribe Op = "subscribe"
)

// allOps is every operation, in a fixed order.
var allOps = []Op{OpSend, OpGet, OpThread, OpConsume, OpCancel, OpInbox, OpSubscribe}

// Known reports whether op is one of the operations above.
func (o Op) Known() bool {
	for _, k := range allOps {
		if o == k {
			return true
		}
	}
	return false
}

// OpSet says which operations a Server exposes or a Dial client will call. An
// operation that is absent or false is not exposed: a server does not mount its
// route, and a client fails the call without a request.
type OpSet map[Op]bool

// DefaultOpSet is Torque's reference surface, ratified 2026-09-30
// (federation.security_model.opset_and_resolver): Send, Get, Thread, Consume and
// Cancel on; Inbox and Subscribe off. A future adopter that wants Inbox or
// Subscribe across hosts must build a widened OpSet itself; it does not get them
// by default. Each call returns a fresh map.
func DefaultOpSet() OpSet {
	return OpSet{OpSend: true, OpGet: true, OpThread: true, OpConsume: true, OpCancel: true}
}

// NewOpSet returns an OpSet with exactly ops on. It returns an error for an
// operation it does not know, so a typo cannot silently leave one off.
func NewOpSet(ops ...Op) (OpSet, error) {
	s := make(OpSet, len(ops))
	for _, o := range ops {
		if !o.Known() {
			return nil, fmt.Errorf("federation: unknown operation %q", o)
		}
		s[o] = true
	}
	return s, nil
}

// Allows reports whether op is on. A nil OpSet allows nothing; NewServer and
// Dial treat a nil OpSet as DefaultOpSet before they get here.
func (s OpSet) Allows(op Op) bool { return s[op] }

// Ops returns the operations that are on, in the fixed order of the constants.
func (s OpSet) Ops() []Op {
	var out []Op
	for _, o := range allOps {
		if s[o] {
			out = append(out, o)
		}
	}
	return out
}

// Clone returns a copy the caller may modify.
func (s OpSet) Clone() OpSet {
	c := make(OpSet, len(s))
	for k, v := range s {
		c[k] = v
	}
	return c
}

// Widen returns a copy of s with ops turned on. It is the explicit way to move
// beyond DefaultOpSet.
func (s OpSet) Widen(ops ...Op) (OpSet, error) {
	c := s.Clone()
	for _, o := range ops {
		if !o.Known() {
			return nil, fmt.Errorf("federation: unknown operation %q", o)
		}
		c[o] = true
	}
	return c, nil
}

// ErrNoOps is returned when an OpSet turns every operation off: a server that
// exposes nothing is a misconfiguration, not a locked-down mode.
var ErrNoOps = errors.New("federation: the operation set enables no operation")

// validate resolves a nil set to the default and rejects unknown operations and
// an empty set. The result is a private copy.
func (s OpSet) validate() (OpSet, error) {
	if s == nil {
		return DefaultOpSet(), nil
	}
	for k := range s {
		if !k.Known() {
			return nil, fmt.Errorf("federation: unknown operation %q", k)
		}
	}
	c := s.Clone()
	if len(c.Ops()) == 0 {
		return nil, ErrNoOps
	}
	return c, nil
}

// String lists the enabled operations.
func (s OpSet) String() string {
	names := make([]string, 0, len(s))
	for _, o := range s.Ops() {
		names = append(names, string(o))
	}
	sort.Strings(names)
	return fmt.Sprint(names)
}
