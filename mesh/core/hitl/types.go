package hitl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ContractVersion is the wire contract_version of every command and result.
// It is Tangent's wire version and is independent of the module version (and
// of Tangent's definition version, which is "1.1").
const ContractVersion = "1.0"

// Wire limits taken from the schema bundle.
const (
	MaxWaitMs         = 50000
	DefaultWaitMs     = 30000
	MaxIdempotencyKey = 200
	MaxKind           = 64
)

// CancelCause says why an interaction was canceled.
type CancelCause string

// Cancel causes. Withdraw always uses CauseCallerWithdrawn.
const (
	CauseCallerWithdrawn     CancelCause = "caller_withdrawn"
	CauseCallerCanceled      CancelCause = "caller_canceled"
	CauseParticipantCanceled CancelCause = "participant_canceled"
	CauseAdministratorCancel CancelCause = "administrator_canceled"
	CauseSurfacePolicy       CancelCause = "surface_policy"
)

// Valid reports whether c is one of the five known causes.
func (c CancelCause) Valid() bool {
	switch c {
	case CauseCallerWithdrawn, CauseCallerCanceled, CauseParticipantCanceled,
		CauseAdministratorCancel, CauseSurfacePolicy:
		return true
	}
	return false
}

// Handle is the core of what enqueue returns: identity, state and revision.
// Implementations may add presentation fields (Tangent adds surface_id,
// queue_* and urls); they are ignored on decode.
type Handle struct {
	ContractVersion string `json:"contract_version"`
	ItemID          string `json:"item_id"`
	State           State  `json:"state"`
	Revision        int64  `json:"revision"`
}

// SourceAssertion is who asked. Every value is an assertion, not proof.
type SourceAssertion struct {
	ApplicationID    string `json:"application_id"`
	ApplicationLabel string `json:"application_label,omitempty"`
	AgentID          string `json:"agent_id"`
	AgentLabel       string `json:"agent_label,omitempty"`
}

// CallerAssertion is the caller identity on Get, Await and Withdraw. The
// application id selects the caller scope; scopes are advisory partitions
// that prevent accidents, not a security boundary.
type CallerAssertion struct {
	ApplicationID string `json:"application_id"`
	PrincipalRef  string `json:"principal_ref,omitempty"`
}

func (c CallerAssertion) validate() error {
	if strings.TrimSpace(c.ApplicationID) == "" || len(c.ApplicationID) > 128 {
		return fmt.Errorf("%w: caller.application_id is required (1-128 characters)", ErrInvalidRequest)
	}
	if len(c.PrincipalRef) > 256 {
		return fmt.Errorf("%w: caller.principal_ref is longer than 256 characters", ErrInvalidRequest)
	}
	return nil
}

func validateCommandHeader(version, itemID string, caller CallerAssertion) error {
	if version != ContractVersion {
		return fmt.Errorf("%w: contract_version must be %q, got %q", ErrInvalidRequest, ContractVersion, version)
	}
	if itemID == "" || len(itemID) > 256 {
		return fmt.Errorf("%w: item_id is required (1-256 characters)", ErrInvalidRequest)
	}
	return caller.validate()
}

// GetCommand asks for the current projection of one item. It never mutates.
type GetCommand struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	Caller          CallerAssertion `json:"caller"`
}

// Validate checks the command against the wire rules.
func (c GetCommand) Validate() error {
	return validateCommandHeader(c.ContractVersion, c.ItemID, c.Caller)
}

// DecodeGetCommand strictly decodes and validates a get command: unknown
// fields are rejected.
func DecodeGetCommand(doc []byte) (GetCommand, error) {
	var c GetCommand
	if err := strictDecode(doc, &c); err != nil {
		return GetCommand{}, err
	}
	return c, c.Validate()
}

// AwaitCommand waits up to WaitMs for the item to become terminal. It never
// mutates and ending the wait never cancels the item.
type AwaitCommand struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	Caller          CallerAssertion `json:"caller"`
	// WaitMs is 0..50000, default 30000; out-of-range values are rejected,
	// never clamped.
	WaitMs *int `json:"wait_ms,omitempty"`
}

// Validate checks the command against the wire rules.
func (c AwaitCommand) Validate() error {
	if err := validateCommandHeader(c.ContractVersion, c.ItemID, c.Caller); err != nil {
		return err
	}
	if c.WaitMs != nil && (*c.WaitMs < 0 || *c.WaitMs > MaxWaitMs) {
		return fmt.Errorf("%w: wait_ms must be between 0 and %d, got %d", ErrInvalidRequest, MaxWaitMs, *c.WaitMs)
	}
	return nil
}

// Wait returns the effective wait, applying the 30000 ms default.
func (c AwaitCommand) Wait() time.Duration {
	if c.WaitMs == nil {
		return DefaultWaitMs * time.Millisecond
	}
	return time.Duration(*c.WaitMs) * time.Millisecond
}

// DecodeAwaitCommand strictly decodes and validates an await command.
func DecodeAwaitCommand(doc []byte) (AwaitCommand, error) {
	var c AwaitCommand
	if err := strictDecode(doc, &c); err != nil {
		return AwaitCommand{}, err
	}
	return c, c.Validate()
}

// WithdrawCommand cancels a nonterminal item with cause caller_withdrawn.
type WithdrawCommand struct {
	ContractVersion  string          `json:"contract_version"`
	ItemID           string          `json:"item_id"`
	Caller           CallerAssertion `json:"caller"`
	ExpectedRevision *int64          `json:"expected_revision,omitempty"`
	Reason           string          `json:"reason,omitempty"`
}

// Validate checks the command against the wire rules.
func (c WithdrawCommand) Validate() error {
	if err := validateCommandHeader(c.ContractVersion, c.ItemID, c.Caller); err != nil {
		return err
	}
	if c.ExpectedRevision != nil && *c.ExpectedRevision < 1 {
		return fmt.Errorf("%w: expected_revision must be positive", ErrInvalidRequest)
	}
	if c.Reason != "" && strings.TrimSpace(c.Reason) == "" {
		return fmt.Errorf("%w: reason must not be blank", ErrInvalidRequest)
	}
	if len(c.Reason) > 1000 {
		return fmt.Errorf("%w: reason is longer than 1000 characters", ErrInvalidRequest)
	}
	return nil
}

// DecodeWithdrawCommand strictly decodes and validates a withdraw command.
func DecodeWithdrawCommand(doc []byte) (WithdrawCommand, error) {
	var c WithdrawCommand
	if err := strictDecode(doc, &c); err != nil {
		return WithdrawCommand{}, err
	}
	return c, c.Validate()
}

// Responder says who answered as an open (kind, ref) pair. Core does not
// enumerate kinds.
type Responder struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

// ProofBind is one thing a Proof is cryptographically bound to, for example
// an approval id, a plan hash, an args digest, an operation, a requester or a
// nonce. The binding set is defined by the caller.
type ProofBind struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Proof is optional, type-tagged evidence that a specific credential vouched
// for the answer (for example a WebAuthn user-verification assertion). Core
// only carries it: it does not verify a proof, does not enumerate schemes, and
// does not rank a proof against Participant.Assurance. Whether to require a
// proof before honoring an outcome is caller policy.
type Proof struct {
	Scheme string      `json:"scheme"`
	KeyRef string      `json:"key_ref"`
	Binds  []ProofBind `json:"binds"`
}

// Participant records who answered. Either Responder or PrincipalRef must
// identify the answerer (PrincipalRef and Authority are Tangent's spelling);
// Assurance is an open string; Proof is optional.
type Participant struct {
	Responder    *Responder `json:"responder,omitempty"`
	PrincipalRef string     `json:"principal_ref,omitempty"`
	Authority    string     `json:"authority,omitempty"`
	Assurance    string     `json:"assurance"`
	Proof        *Proof     `json:"proof,omitempty"`
}

// Validate checks that the participant names an answerer and states an
// assurance, and that a proof, if present, is well formed.
func (p Participant) Validate() error {
	if p.Responder == nil && p.PrincipalRef == "" {
		return fmt.Errorf("%w: participant needs responder or principal_ref", ErrInvalidRequest)
	}
	if p.Responder != nil && (p.Responder.Kind == "" || p.Responder.Ref == "") {
		return fmt.Errorf("%w: participant.responder needs kind and ref", ErrInvalidRequest)
	}
	if p.Assurance == "" {
		return fmt.Errorf("%w: participant.assurance is required", ErrInvalidRequest)
	}
	if p.Proof != nil {
		if p.Proof.Scheme == "" || p.Proof.KeyRef == "" || p.Proof.Binds == nil {
			return fmt.Errorf("%w: participant.proof needs scheme, key_ref and binds", ErrInvalidRequest)
		}
		for _, b := range p.Proof.Binds {
			if b.Name == "" || b.Value == "" {
				return fmt.Errorf("%w: participant.proof.binds entries need name and value", ErrInvalidRequest)
			}
		}
	}
	return nil
}

// Response is a participant's answer: a kind and an open-string decision.
// Profiles constrain them (approval: approved|denied; attention:
// acknowledged); core does not. Members beyond the four known ones are kept
// in Extra.
type Response struct {
	Kind     string                     `json:"kind"`
	Decision string                     `json:"decision"`
	Note     string                     `json:"note,omitempty"`
	Reply    string                     `json:"reply,omitempty"`
	Extra    map[string]json.RawMessage `json:"-"`
}

type responseAlias Response

// MarshalJSON encodes the known members and Extra together.
func (r Response) MarshalJSON() ([]byte, error) { return joinExtra(responseAlias(r), r.Extra) }

// UnmarshalJSON decodes the known members and collects the rest in Extra.
func (r *Response) UnmarshalJSON(data []byte) error {
	var a responseAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	extra, err := splitExtra(data, "kind", "decision", "note", "reply")
	if err != nil {
		return err
	}
	a.Extra = extra
	*r = Response(a)
	return nil
}

func (r Response) validate() error {
	if r.Kind == "" || r.Decision == "" {
		return fmt.Errorf("%w: response needs kind and decision", ErrInvalidRequest)
	}
	if r.Note != "" && strings.TrimSpace(r.Note) == "" {
		return fmt.Errorf("%w: response.note must not be blank", ErrInvalidRequest)
	}
	if r.Reply != "" && strings.TrimSpace(r.Reply) == "" {
		return fmt.Errorf("%w: response.reply must not be blank", ErrInvalidRequest)
	}
	return nil
}

// ResolutionRecord is the immutable record of a participant's answer.
type ResolutionRecord struct {
	ResolutionID        string      `json:"resolution_id"`
	Response            Response    `json:"response"`
	Participant         Participant `json:"participant"`
	ResolvedAt          time.Time   `json:"resolved_at"`
	InteractionRevision int64       `json:"interaction_revision"`
	// PresentedProjectionRevision is optional: Tangent supplies it, others
	// have no presentation revision.
	PresentedProjectionRevision *int64 `json:"presented_projection_revision,omitempty"`
}

// EnqueueRequest is the core of an enqueue request. Members beyond the
// core ones (title, summary, evidence, kind-specific fields) are open and are
// kept in Extra; they take part in the idempotency digest.
type EnqueueRequest struct {
	ContractVersion string          `json:"contract_version"`
	Kind            string          `json:"kind"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Source          SourceAssertion `json:"source"`
	// ExpiresAt is an absolute deadline. Whether it is enforced is up to the
	// implementation; Service enforces it.
	ExpiresAt    *time.Time                 `json:"expires_at,omitempty"`
	Correlations json.RawMessage            `json:"correlations,omitempty"`
	Extra        map[string]json.RawMessage `json:"-"`
}

type enqueueAlias EnqueueRequest

// MarshalJSON encodes the core members and Extra together.
func (r EnqueueRequest) MarshalJSON() ([]byte, error) { return joinExtra(enqueueAlias(r), r.Extra) }

// UnmarshalJSON decodes the core members and collects the rest in Extra.
func (r *EnqueueRequest) UnmarshalJSON(data []byte) error {
	var a enqueueAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	extra, err := splitExtra(data, "contract_version", "kind", "idempotency_key", "source", "expires_at", "correlations")
	if err != nil {
		return err
	}
	a.Extra = extra
	*r = EnqueueRequest(a)
	return nil
}

// Validate checks the core rules of HITLEnqueueRequestCoreV1.
func (r EnqueueRequest) Validate() error {
	switch {
	case r.ContractVersion != ContractVersion:
		return fmt.Errorf("%w: contract_version must be %q, got %q", ErrInvalidRequest, ContractVersion, r.ContractVersion)
	case r.Kind == "" || len(r.Kind) > MaxKind:
		return fmt.Errorf("%w: kind is required (1-%d characters)", ErrInvalidRequest, MaxKind)
	case r.IdempotencyKey == "" || len(r.IdempotencyKey) > MaxIdempotencyKey:
		return fmt.Errorf("%w: idempotency_key is required (1-%d characters)", ErrInvalidRequest, MaxIdempotencyKey)
	case r.Source.ApplicationID == "" || len(r.Source.ApplicationID) > 128:
		return fmt.Errorf("%w: source.application_id is required (1-128 characters)", ErrInvalidRequest)
	case r.Source.AgentID == "" || len(r.Source.AgentID) > 256:
		return fmt.Errorf("%w: source.agent_id is required (1-256 characters)", ErrInvalidRequest)
	}
	if len(r.Correlations) > 0 {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(r.Correlations, &m); err != nil || m == nil {
			return fmt.Errorf("%w: correlations must be an object", ErrInvalidRequest)
		}
	}
	return nil
}

// ItemView is the retrieval projection of one item. Outputs are decoded
// tolerantly: presentation fields an implementation adds are ignored.
type ItemView struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	State           State           `json:"state"`
	Revision        int64           `json:"revision"`
	RequestSnapshot json.RawMessage `json:"request_snapshot"`
	EnqueuedAt      time.Time       `json:"enqueued_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// TerminalOutcome is set exactly when State is terminal.
	TerminalOutcome Outcome `json:"-"`
}

type itemViewWire struct {
	ContractVersion string          `json:"contract_version"`
	ItemID          string          `json:"item_id"`
	State           State           `json:"state"`
	Revision        int64           `json:"revision"`
	RequestSnapshot json.RawMessage `json:"request_snapshot"`
	EnqueuedAt      time.Time       `json:"enqueued_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	TerminalOutcome json.RawMessage `json:"terminal_outcome,omitempty"`
}

// MarshalJSON encodes the view, including the terminal outcome when present.
func (v ItemView) MarshalJSON() ([]byte, error) {
	w := itemViewWire{
		ContractVersion: v.ContractVersion, ItemID: v.ItemID, State: v.State, Revision: v.Revision,
		RequestSnapshot: v.RequestSnapshot, EnqueuedAt: v.EnqueuedAt, UpdatedAt: v.UpdatedAt,
	}
	if v.TerminalOutcome != nil {
		b, err := json.Marshal(v.TerminalOutcome)
		if err != nil {
			return nil, err
		}
		w.TerminalOutcome = b
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes the view and its terminal outcome, and checks that the
// outcome is present exactly for terminal states and matches the state.
func (v *ItemView) UnmarshalJSON(data []byte) error {
	var w itemViewWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	out := ItemView{
		ContractVersion: w.ContractVersion, ItemID: w.ItemID, State: w.State, Revision: w.Revision,
		RequestSnapshot: w.RequestSnapshot, EnqueuedAt: w.EnqueuedAt, UpdatedAt: w.UpdatedAt,
	}
	hasOutcome := len(w.TerminalOutcome) > 0 && string(w.TerminalOutcome) != "null"
	switch {
	case hasOutcome:
		o, err := UnmarshalOutcome(w.TerminalOutcome)
		if err != nil {
			return err
		}
		if o.OutcomeState() != w.State {
			return fmt.Errorf("%w: terminal_outcome state %q does not match item state %q", ErrInvalidOutcome, o.OutcomeState(), w.State)
		}
		out.TerminalOutcome = o
	case w.State.IsTerminal():
		return fmt.Errorf("%w: terminal item %q has no terminal_outcome", ErrInvalidOutcome, w.ItemID)
	}
	*v = out
	return nil
}

// Retrieval modes and wait statuses.
const (
	ModeGet   = "get"
	ModeAwait = "await"

	WaitNotWaited = "not_waited"
	WaitTerminal  = "terminal"
	WaitTimeout   = "timeout"
)

// RetrievalResult is what Get and Await return. Exactly three shapes are
// legal: get/not_waited, await/terminal (item terminal) and await/timeout
// (item nonterminal).
type RetrievalResult struct {
	ContractVersion string    `json:"contract_version"`
	Mode            string    `json:"mode"`
	WaitStatus      string    `json:"wait_status"`
	RetrievedAt     time.Time `json:"retrieved_at"`
	Item            ItemView  `json:"item"`
}

// Validate checks the three-row discriminated union.
func (r RetrievalResult) Validate() error {
	term := r.Item.State.IsTerminal()
	switch {
	case r.Mode == ModeGet && r.WaitStatus == WaitNotWaited:
	case r.Mode == ModeAwait && r.WaitStatus == WaitTerminal && term:
	case r.Mode == ModeAwait && r.WaitStatus == WaitTimeout && !term:
	default:
		return fmt.Errorf("%w: illegal retrieval combination mode=%q wait_status=%q state=%q",
			ErrInvalidRequest, r.Mode, r.WaitStatus, r.Item.State)
	}
	return nil
}
