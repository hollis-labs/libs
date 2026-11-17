// Package chathost is a durable, host-owned conversation and attachment store.
// It implements the SDK session admission ports without owning runtime processes.
package chathost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hollis-labs/libs/agent/sdk/session"
	"github.com/hollis-labs/libs/mesh/core/runtimecontract"
)

type Permission string

const (
	Read            Permission = "read"
	Control         Permission = "control"
	Admin           Permission = "admin"
	Runtime         Permission = "runtime"
	TakeoverControl Permission = "takeover"
)

// Authorize must authenticate the caller independently of the IDs in a request.
// The transport supplies this callback; shape validation here never grants access.
type Authorize func(context.Context, runtimecontract.OwnerID, Permission) error

type AgentKind string

const (
	Stable   AgentKind = "stable"
	Task     AgentKind = "task"
	Embedded AgentKind = "embedded"
)

type Supervisor string

const (
	App      Supervisor = "app"
	Nanite   Supervisor = "nanite"
	Cerberus Supervisor = "cerberus"
	Tether   Supervisor = "tether"
)

// Agent is the durable identity and its sole lifecycle owner. Seat and runtime
// attempt are independent; a new attempt does not create another agent.
type Agent struct {
	ID         runtimecontract.OperativeID `json:"id"`
	Owner      runtimecontract.OwnerID     `json:"owner"`
	Seat       runtimecontract.SeatID      `json:"seat,omitempty"`
	Kind       AgentKind                   `json:"kind"`
	Supervisor Supervisor                  `json:"supervisor"`
}

type Role string

const (
	Viewer     Role = "viewer"
	Controller Role = "controller"
)

type Attachment struct {
	ID      string                  `json:"id"`
	Binding runtimecontract.Binding `json:"binding"`
	Role    Role                    `json:"role"`
}
type EventKind string

const (
	Input    EventKind = "input"
	Output   EventKind = "output"
	Notice   EventKind = "notice"
	Approval EventKind = "approval"
)

type Event struct {
	Cursor    runtimecontract.CommittedCursor `json:"cursor"`
	Binding   runtimecontract.Binding         `json:"binding"`
	Kind      EventKind                       `json:"kind"`
	ID        string                          `json:"id"`
	Text      string                          `json:"text,omitempty"`
	Reference *runtimecontract.Reference      `json:"reference,omitempty"`
	At        time.Time                       `json:"at"`
}

type conversation struct {
	Record      session.Record                  `json:"record"`
	Parent      *runtimecontract.ConversationID `json:"parent,omitempty"`
	Events      []Event                         `json:"events"`
	Lease       *session.Lease                  `json:"lease,omitempty"`
	Attachments map[string]Attachment           `json:"attachments"`
	Dedupe      map[string]Event                `json:"dedupe"`
}
type state struct {
	Agents        map[string]Agent         `json:"agents"`
	Conversations map[string]*conversation `json:"conversations"`
	Attempts      map[string]bool          `json:"attempts"`
	Version       uint64                   `json:"version"`
}

type Host struct {
	root      string
	authorize Authorize
	now       func() time.Time
}

func sameLease(a, b session.Lease) bool {
	return a.ID == b.ID && a.Binding == b.Binding && a.Writer == b.Writer && a.ExpiresAt.Equal(b.ExpiresAt)
}

// Open uses one lock file for transactions across processes and atomic, synced
// snapshots for crash recovery. The directory must be on one local filesystem.
func Open(root string, authorize Authorize) (*Host, error) {
	if root == "" || authorize == nil {
		return nil, session.ErrInvalid
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return &Host{root: root, authorize: authorize, now: time.Now}, nil
}

func (h *Host) check(ctx context.Context, owner runtimecontract.OwnerID, p Permission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(string(owner)) == "" {
		return session.ErrInvalid
	}
	return h.authorize(ctx, owner, p)
}
func key(b runtimecontract.Binding) string {
	return tupleKey(string(b.OwnerID), string(b.OperativeID), string(b.ConversationID))
}
func scopeKey(s session.Scope) string {
	return tupleKey(string(s.OwnerID), string(s.OperativeID), string(s.ConversationID))
}
func tupleKey(parts ...string) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}
func equalScope(b runtimecontract.Binding, s session.Scope) bool {
	return b.OwnerID == s.OwnerID && b.OperativeID == s.OperativeID && b.ConversationID == s.ConversationID
}
func token() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func clone[T any](v T) T { b, _ := json.Marshal(v); var out T; _ = json.Unmarshal(b, &out); return out }

// transact never publishes a partial snapshot. The lock also covers readers,
// so two host processes cannot both acquire a controller or writer.
func (h *Host) transact(write bool, f func(*state) error) error {
	lf, err := os.OpenFile(filepath.Join(h.root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	s := state{Agents: map[string]Agent{}, Conversations: map[string]*conversation{}, Attempts: map[string]bool{}}
	b, err := os.ReadFile(filepath.Join(h.root, "state.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(b) > 0 {
		if err = json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("corrupt chat state: %w", err)
		}
	}
	if err = f(&s); err != nil {
		return err
	}
	if !write {
		return nil
	}
	b, err = json.Marshal(&s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.root, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(h.root, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(h.root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// RegisterAgent creates an app-owned embedded agent or records a shared agent's
// one supervisor. Attaching an existing agent never changes that owner.
func (h *Host) RegisterAgent(ctx context.Context, a Agent) error {
	if a.ID == "" || a.Owner == "" || strings.TrimSpace(string(a.ID)) == "" || strings.TrimSpace(string(a.Owner)) == "" {
		return session.ErrInvalid
	}
	switch a.Kind {
	case Stable, Task, Embedded:
	default:
		return session.ErrInvalid
	}
	switch a.Supervisor {
	case App, Nanite, Cerberus, Tether:
	default:
		return session.ErrInvalid
	}
	if a.Seat != "" && strings.TrimSpace(string(a.Seat)) == "" {
		return session.ErrInvalid
	}
	if a.Kind == Embedded && a.Supervisor != App {
		return session.ErrInvalid
	}
	if err := h.check(ctx, a.Owner, Admin); err != nil {
		return err
	}
	return h.transact(true, func(s *state) error {
		if _, ok := s.Agents[string(a.ID)]; ok {
			return session.ErrConflict
		}
		s.Agents[string(a.ID)] = a
		return nil
	})
}

// Create reserves generation one for an already registered durable agent.
func (h *Host) Create(ctx context.Context, r session.Record) (session.Record, error) {
	b := r.Binding
	if err := b.Validate(); err != nil {
		return session.Record{}, err
	}
	if r.Phase != session.Fenced || r.Parent != nil || b.Generation != 1 || r.Version != "" {
		return session.Record{}, session.ErrInvalid
	}
	if err := h.check(ctx, b.OwnerID, Admin); err != nil {
		return session.Record{}, err
	}
	var out session.Record
	err := h.transact(true, func(s *state) error {
		a, ok := s.Agents[string(b.OperativeID)]
		if !ok || a.Owner != b.OwnerID || a.Seat != b.SeatID {
			return session.ErrInvalid
		}
		if _, ok = s.Conversations[key(b)]; ok {
			return session.ErrConflict
		}
		if s.Attempts[string(b.AttemptID)] {
			return session.ErrConflict
		}
		s.Version++
		r.Version = strconv.FormatUint(s.Version, 10)
		s.Attempts[string(b.AttemptID)] = true
		s.Conversations[key(b)] = &conversation{Record: r, Attachments: map[string]Attachment{}, Dedupe: map[string]Event{}}
		out = clone(r)
		return nil
	})
	return out, err
}

func (h *Host) Load(ctx context.Context, sc session.Scope) (session.Record, error) {
	if err := h.check(ctx, sc.OwnerID, Read); err != nil {
		return session.Record{}, err
	}
	var out session.Record
	err := h.transact(false, func(s *state) error {
		c := s.Conversations[scopeKey(sc)]
		if c == nil || !equalScope(c.Record.Binding, sc) {
			return session.ErrNotFound
		}
		out = clone(c.Record)
		return nil
	})
	return out, err
}

// CompareAndSwap is the durable generation fence used by session.Manager.
func (h *Host) CompareAndSwap(ctx context.Context, sc session.Scope, version string, next session.Record) (session.Record, error) {
	if err := next.Binding.Validate(); err != nil {
		return session.Record{}, err
	}
	if !equalScope(next.Binding, sc) || version == "" {
		return session.Record{}, session.ErrInvalid
	}
	if err := h.check(ctx, sc.OwnerID, Admin); err != nil {
		return session.Record{}, err
	}
	var out session.Record
	err := h.transact(true, func(s *state) error {
		c := s.Conversations[scopeKey(sc)]
		if c == nil {
			return session.ErrNotFound
		}
		old := c.Record
		if old.Version != version {
			return session.ErrConflict
		}
		if next.Reason != old.Reason && next.Binding == old.Binding {
			return session.ErrInvalid
		}
		if old.Binding.SeatID != next.Binding.SeatID {
			return session.ErrInvalid
		}
		if next.Binding == old.Binding {
			if old.Phase != session.Fenced || next.Phase != session.Active || (next.Parent == nil) != (old.Parent == nil) {
				return session.ErrInvalid
			}
			if old.Parent != nil && (next.Parent == nil || *next.Parent != *old.Parent) {
				return session.ErrInvalid
			}
		} else {
			if next.Phase != session.Fenced || next.Parent == nil || *next.Parent != old.Binding || next.Binding.Generation != old.Binding.Generation+1 || s.Attempts[string(next.Binding.AttemptID)] {
				return session.ErrStale
			}
			s.Attempts[string(next.Binding.AttemptID)] = true
			c.Lease = nil
			c.Attachments = map[string]Attachment{}
		}
		s.Version++
		next.Version = strconv.FormatUint(s.Version, 10)
		c.Record = next
		out = clone(next)
		return nil
	})
	return out, err
}

// CreateChild makes a distinct conversation. It does not copy parent history.
func (h *Host) CreateChild(ctx context.Context, parent runtimecontract.Binding, child session.Record) (session.Record, error) {
	if err := parent.Validate(); err != nil {
		return session.Record{}, err
	}
	if err := child.Binding.Validate(); err != nil {
		return session.Record{}, err
	}
	if child.Binding.OwnerID != parent.OwnerID || child.Binding.OperativeID != parent.OperativeID || child.Binding.ConversationID == parent.ConversationID || child.Binding.Generation != 1 || child.Phase != session.Fenced || child.Parent != nil || child.Version != "" {
		return session.Record{}, session.ErrInvalid
	}
	if err := h.check(ctx, parent.OwnerID, Admin); err != nil {
		return session.Record{}, err
	}
	var out session.Record
	err := h.transact(true, func(s *state) error {
		p := s.Conversations[key(parent)]
		if p == nil || p.Record.Binding != parent || p.Record.Phase != session.Active {
			return session.ErrStale
		}
		b := child.Binding
		if _, ok := s.Conversations[key(b)]; ok {
			return session.ErrConflict
		}
		if s.Attempts[string(b.AttemptID)] {
			return session.ErrConflict
		}
		if b.SeatID != parent.SeatID {
			return session.ErrInvalid
		}
		s.Version++
		child.Version = strconv.FormatUint(s.Version, 10)
		id := parent.ConversationID
		s.Conversations[key(b)] = &conversation{Record: child, Parent: &id, Attachments: map[string]Attachment{}, Dedupe: map[string]Event{}}
		s.Attempts[string(b.AttemptID)] = true
		out = clone(child)
		return nil
	})
	return out, err
}

// Attach is observe-first. A controller attachment can later acquire the one
// writer lease; the view itself does not own history, queue or runtime lifetime.
func (h *Host) Attach(ctx context.Context, b runtimecontract.Binding, role Role) (Attachment, error) {
	if err := b.Validate(); err != nil {
		return Attachment{}, err
	}
	if role != Viewer && role != Controller {
		return Attachment{}, session.ErrInvalid
	}
	perm := Read
	if role == Controller {
		perm = Control
	}
	if err := h.check(ctx, b.OwnerID, perm); err != nil {
		return Attachment{}, err
	}
	id, err := token()
	if err != nil {
		return Attachment{}, err
	}
	a := Attachment{ID: id, Binding: b, Role: role}
	err = h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		c.Attachments[id] = a
		return nil
	})
	return a, err
}
func (h *Host) Detach(ctx context.Context, a Attachment) error {
	permission := Read
	if a.Role == Controller {
		permission = Control
	}
	if err := h.check(ctx, a.Binding.OwnerID, permission); err != nil {
		return err
	}
	return h.transact(true, func(s *state) error {
		c := s.Conversations[key(a.Binding)]
		if c == nil {
			return session.ErrNotFound
		}
		if c.Record.Binding != a.Binding || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		stored, ok := c.Attachments[a.ID]
		if !ok || stored != a {
			return session.ErrStale
		}
		delete(c.Attachments, a.ID)
		if c.Lease != nil && string(c.Lease.Writer) == a.ID {
			c.Lease = nil
		}
		return nil
	})
}

// ReadEvents replays only an explicitly selected conversation and exact
// attachment. Cursors are committed event positions, never PTY byte offsets.
func (h *Host) ReadEvents(ctx context.Context, a Attachment, after runtimecontract.CommittedCursor) ([]Event, error) {
	if err := h.check(ctx, a.Binding.OwnerID, Read); err != nil {
		return nil, err
	}
	n := uint64(0)
	if after != "" {
		v, err := strconv.ParseUint(string(after), 10, 64)
		if err != nil {
			return nil, session.ErrInvalid
		}
		n = v
	}
	var out []Event
	err := h.transact(false, func(s *state) error {
		c := s.Conversations[key(a.Binding)]
		if c == nil {
			return session.ErrNotFound
		}
		if c.Record.Binding != a.Binding || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		if c.Attachments[a.ID] != a {
			return session.ErrStale
		}
		if n > uint64(len(c.Events)) {
			return session.ErrInvalid
		}
		out = clone(c.Events[n:])
		return nil
	})
	return out, err
}

func (h *Host) Acquire(ctx context.Context, b runtimecontract.Binding, w session.WriterID) (session.Lease, error) {
	if err := h.check(ctx, b.OwnerID, Control); err != nil {
		return session.Lease{}, err
	}
	id, err := token()
	if err != nil {
		return session.Lease{}, err
	}
	var out session.Lease
	err = h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		a, ok := c.Attachments[string(w)]
		if !ok || a.Binding != b || a.Role != Controller {
			return session.ErrLease
		}
		if c.Lease != nil && h.now().Before(c.Lease.ExpiresAt) {
			return session.ErrLease
		}
		out = session.Lease{ID: session.LeaseID(id), Binding: b, Writer: w, ExpiresAt: h.now().Add(30 * time.Second)}
		c.Lease = &out
		return nil
	})
	return out, err
}

// Takeover atomically replaces the current writer lease with a fresh lease for
// an attached controller. A host must grant TakeoverControl explicitly; ordinary
// Control permission cannot displace an active writer.
func (h *Host) Takeover(ctx context.Context, b runtimecontract.Binding, w session.WriterID) (session.Lease, error) {
	if err := b.Validate(); err != nil {
		return session.Lease{}, err
	}
	if err := h.check(ctx, b.OwnerID, Control); err != nil {
		return session.Lease{}, err
	}
	if err := h.check(ctx, b.OwnerID, TakeoverControl); err != nil {
		return session.Lease{}, err
	}
	id, err := token()
	if err != nil {
		return session.Lease{}, err
	}
	var out session.Lease
	err = h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		a, ok := c.Attachments[string(w)]
		if !ok || a.Binding != b || a.Role != Controller {
			return session.ErrLease
		}
		out = session.Lease{ID: session.LeaseID(id), Binding: b, Writer: w, ExpiresAt: h.now().Add(30 * time.Second)}
		c.Lease = &out
		return nil
	})
	return out, err
}

func (h *Host) validLease(s *state, l session.Lease) error {
	c := s.Conversations[key(l.Binding)]
	if c == nil || c.Record.Binding != l.Binding || c.Record.Phase != session.Active {
		return session.ErrStale
	}
	if c.Lease == nil || !sameLease(*c.Lease, l) || !h.now().Before(l.ExpiresAt) {
		return session.ErrLease
	}
	a, ok := c.Attachments[string(l.Writer)]
	if !ok || a.Binding != l.Binding || a.Role != Controller {
		return session.ErrLease
	}
	return nil
}

// ValidateLease is a preflight check. External PTY effects still need exact
// binding admission at delivery; use WithLease for synchronous local effects.
func (h *Host) ValidateLease(ctx context.Context, l session.Lease) error {
	if err := h.check(ctx, l.Binding.OwnerID, Control); err != nil {
		return err
	}
	return h.transact(false, func(s *state) error { return h.validLease(s, l) })
}

// WithLease holds the durable binding lock while a synchronous PTY input or
// resize callback completes. This prevents a generation switch or takeover
// between admission and the local effect. Do not use for an asynchronous send:
// the receiving transport must recheck the binding and lease at delivery.
func (h *Host) WithLease(ctx context.Context, l session.Lease, effect func() error) error {
	if effect == nil {
		return session.ErrInvalid
	}
	if err := h.check(ctx, l.Binding.OwnerID, Control); err != nil {
		return err
	}
	return h.transact(false, func(s *state) error {
		if err := h.validLease(s, l); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return effect()
	})
}
func (h *Host) Renew(ctx context.Context, l session.Lease) (session.Lease, error) {
	if err := h.check(ctx, l.Binding.OwnerID, Control); err != nil {
		return session.Lease{}, err
	}
	var out session.Lease
	err := h.transact(true, func(s *state) error {
		c := s.Conversations[key(l.Binding)]
		if c == nil || c.Record.Binding != l.Binding || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		if c.Lease == nil || !sameLease(*c.Lease, l) || !h.now().Before(l.ExpiresAt) {
			return session.ErrLease
		}
		if c.Attachments[string(l.Writer)].ID != string(l.Writer) {
			return session.ErrLease
		}
		out = l
		out.ExpiresAt = h.now().Add(30 * time.Second)
		c.Lease = &out
		return nil
	})
	return out, err
}
func (h *Host) Revoke(ctx context.Context, b runtimecontract.Binding) error {
	if err := h.check(ctx, b.OwnerID, Admin); err != nil {
		return err
	}
	return h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil {
			return session.ErrNotFound
		}
		if c.Record.Binding == b {
			c.Lease = nil
		}
		return nil
	})
}
func (h *Host) Submit(ctx context.Context, in session.Input) (runtimecontract.CommittedCursor, error) {
	b := in.Lease.Binding
	if err := h.check(ctx, b.OwnerID, Control); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.ID) == "" || len(in.Text) > 1<<20 {
		return "", session.ErrInvalid
	}
	var cur runtimecontract.CommittedCursor
	err := h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		if c.Lease == nil || !sameLease(*c.Lease, in.Lease) || !h.now().Before(in.Lease.ExpiresAt) {
			return session.ErrLease
		}
		if c.Attachments[string(in.Lease.Writer)].ID != string(in.Lease.Writer) {
			return session.ErrLease
		}
		k := "input:" + string(b.AttemptID) + ":" + strconv.FormatUint(uint64(b.Generation), 10) + ":" + in.ID
		if old, ok := c.Dedupe[k]; ok {
			if old.Text != in.Text || old.Binding != b {
				return session.ErrConflict
			}
			cur = old.Cursor
			return nil
		}
		cur = runtimecontract.CommittedCursor(strconv.Itoa(len(c.Events) + 1))
		e := Event{Cursor: cur, Binding: b, Kind: Input, ID: in.ID, Text: in.Text, At: h.now()}
		c.Events = append(c.Events, e)
		c.Dedupe[k] = e
		return nil
	})
	return cur, err
}
func (h *Host) Admit(ctx context.Context, m session.Message) (runtimecontract.CommittedCursor, error) {
	b := m.Binding
	if err := h.check(ctx, b.OwnerID, Runtime); err != nil {
		return "", err
	}
	if strings.TrimSpace(m.ID) == "" || m.Reference.OwnerID != b.OwnerID || m.Reference.Validate() != nil {
		return "", session.ErrInvalid
	}
	kind := Notice
	if m.Kind == session.Approval {
		kind = Approval
	} else if m.Kind != session.Event {
		return "", session.ErrInvalid
	}
	var cur runtimecontract.CommittedCursor
	err := h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		k := "message:" + string(b.AttemptID) + ":" + strconv.FormatUint(uint64(b.Generation), 10) + ":" + m.ID
		if old, ok := c.Dedupe[k]; ok {
			if old.Kind != kind || old.Binding != b || old.Reference == nil || *old.Reference != m.Reference {
				return session.ErrConflict
			}
			cur = old.Cursor
			return nil
		}
		cur = runtimecontract.CommittedCursor(strconv.Itoa(len(c.Events) + 1))
		ref := m.Reference
		e := Event{Cursor: cur, Binding: b, Kind: kind, ID: m.ID, Reference: &ref, At: h.now()}
		c.Events = append(c.Events, e)
		c.Dedupe[k] = e
		return nil
	})
	return cur, err
}

// Publish records runtime output against the exact active attempt. The caller
// must still verify runtime authority through the injected Authorize callback.
func (h *Host) Publish(ctx context.Context, b runtimecontract.Binding, id, text string) (runtimecontract.CommittedCursor, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	if err := h.check(ctx, b.OwnerID, Runtime); err != nil {
		return "", err
	}
	if strings.TrimSpace(id) == "" || len(text) > 1<<20 {
		return "", session.ErrInvalid
	}
	var cur runtimecontract.CommittedCursor
	err := h.transact(true, func(s *state) error {
		c := s.Conversations[key(b)]
		if c == nil || c.Record.Binding != b || c.Record.Phase != session.Active {
			return session.ErrStale
		}
		k := "output:" + string(b.AttemptID) + ":" + strconv.FormatUint(uint64(b.Generation), 10) + ":" + id
		if old, ok := c.Dedupe[k]; ok {
			if old.Text != text || old.Binding != b {
				return session.ErrConflict
			}
			cur = old.Cursor
			return nil
		}
		cur = runtimecontract.CommittedCursor(strconv.Itoa(len(c.Events) + 1))
		e := Event{Cursor: cur, Binding: b, Kind: Output, ID: id, Text: text, At: h.now()}
		c.Events = append(c.Events, e)
		c.Dedupe[k] = e
		return nil
	})
	return cur, err
}

var _ session.Conversations = (*Host)(nil)
var _ session.InputLeases = (*Host)(nil)
