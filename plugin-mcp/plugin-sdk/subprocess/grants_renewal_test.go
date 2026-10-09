package subprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
)

type renewalPlugin struct {
	calls  int
	accept func(context.Context, capability.GrantSet) error
}

func (p *renewalPlugin) Init(context.Context, InitParams) (InitResult, error) {
	return InitResult{ID: "test", Name: "Test", Version: "1", Protocol: 2, CapabilityContract: 1}, nil
}
func (p *renewalPlugin) Load(context.Context) (LoadResult, error) { return LoadResult{}, nil }
func (p *renewalPlugin) Unload(context.Context) error             { return nil }
func (p *renewalPlugin) GrantsRenewed(ctx context.Context, g capability.GrantSet) error {
	p.calls++
	if p.accept != nil {
		return p.accept(ctx, g)
	}
	return nil
}
func renewalFixture() (capability.RuntimeIdentity, capability.GrantSet, capability.GrantSet) {
	now := time.Now().UTC()
	owner := capability.RuntimeIdentity{HostInstance: "host", OwnerID: "plugin", OwnerGeneration: 1}
	old := capability.GrantSet{{GrantID: "grant", Name: "host.nanite.readonly.query", SchemaVersion: 1, Scope: json.RawMessage(`{"resources":["sessions"],"all_sessions":true}`), HostInstance: "host", OwnerID: "plugin", OwnerGeneration: 1, Audience: "nanite", IssuedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), PolicyRevision: "review"}}
	next := old.Clone()
	next[0].IssuedAt = now.Format(time.RFC3339Nano)
	next[0].ExpiresAt = now.Add(24 * time.Hour).Format(time.RFC3339Nano)
	return owner, old, next
}
func TestGrantRenewalAcknowledgedSnapshotAndRefusals(t *testing.T) {
	owner, old, next := renewalFixture()
	p := &renewalPlugin{}
	var frames []string
	var fenced bool
	s := &server{plugin: p, grants: grantState{initialized: true, enabled: true, owner: owner, grants: old}, writeFrame: func(b []byte) { frames = append(frames, string(b)) }, fence: func(error) { fenced = true }, outputLimit: 8 << 20}
	params := GrantsRenewParams{RenewalVersion: 1, Sequence: 1, Incarnation: owner, Grants: next, Context: ForwardContext{TimeoutMS: 1000}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	s.renewGrants(context.Background(), RPCRequest{ID: NumberID(2), Method: MethodGrantsRenew, Params: json.RawMessage(raw)})
	if p.calls != 1 || fenced || len(frames) != 1 || !strings.Contains(frames[0], `"sequence":1`) {
		t.Fatalf("ack/callback %d %v %v", p.calls, fenced, frames)
	}
	current, ok := s.grants.snapshot()
	if !ok || current[0].ExpiresAt != next[0].ExpiresAt {
		t.Fatal("snapshot not replaced")
	}
	s.renewGrants(context.Background(), RPCRequest{ID: NumberID(3), Method: MethodGrantsRenew, Params: json.RawMessage(raw)})
	if p.calls != 1 || !strings.Contains(frames[1], `"error"`) {
		t.Fatal("replay reached callback")
	}
	s.grants.end()
	params.Sequence = 2
	raw, _ = json.Marshal(params)
	s.renewGrants(context.Background(), RPCRequest{ID: NumberID(4), Method: MethodGrantsRenew, Params: json.RawMessage(raw)})
	if p.calls != 1 {
		t.Fatal("ended state revived")
	}
}

func TestGrantRenewalFutureClientsRefreshWithoutExtendingOldClients(t *testing.T) {
	init := hostClientInitFixture(t)
	now := time.Now().UTC()
	for i := range init.Grants {
		init.Grants[i].IssuedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
		init.Grants[i].ExpiresAt = now.Add(time.Second).Format(time.RFC3339Nano)
	}
	init.HostServices.Limits.MethodTimeoutMS["host/storage/get"] = 2000
	_, ctx, core, _ := clientFixture(t, init)
	s := &server{plugin: &renewalPlugin{}, grants: grantState{initialized: true, enabled: true, owner: init.Incarnation, grants: init.Grants.Clone()}, writeFrame: func([]byte) {}, fence: func(err error) { t.Fatal(err) }, outputLimit: DefaultFrameBytes}
	ctx = context.WithValue(ctx, grantsKey{}, &s.grants)
	oldCtx, err := hostClientContext(ctx, core, init, newSecretTracker(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	oldClient, _ := HostClientFromContext(oldCtx)
	next := init.Grants.Clone()
	for i := range next {
		next[i].IssuedAt = now.Format(time.RFC3339Nano)
		next[i].ExpiresAt = now.Add(24 * time.Hour).Format(time.RFC3339Nano)
	}
	params := GrantsRenewParams{1, 1, init.Incarnation, next, ForwardContext{TimeoutMS: 1000}}
	raw, _ := json.Marshal(params)
	s.renewGrants(context.Background(), RPCRequest{ID: NumberID(2), Method: MethodGrantsRenew, Params: json.RawMessage(raw)})
	newCtx, err := hostClientContext(ctx, core, init, newSecretTracker(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	newClient, _ := HostClientFromContext(newCtx)
	var id string
	for _, g := range init.Grants {
		if g.Name == "storage.read" {
			id = g.GrantID
			break
		}
	}
	if id == "" {
		t.Fatal("storage grant missing")
	}
	_, cancel, oldBudget, err := oldClient.begin(oldCtx, "host/storage/get", id, "storage.read")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, cancel, newBudget, err := newClient.begin(newCtx, "host/storage/get", id, "storage.read")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if oldBudget.TimeoutMS > 1000 || newBudget.TimeoutMS <= 1000 {
		t.Fatalf("old/new budgets %d/%d", oldBudget.TimeoutMS, newBudget.TimeoutMS)
	}
	s.grants.end()
	if _, _, _, err := newClient.begin(newCtx, "host/storage/get", id, "storage.read"); err == nil {
		t.Fatal("revoked client still starts work")
	}
}

func TestGrantRenewalCancellationAndEndedCallbackFence(t *testing.T) {
	for _, cause := range []string{"caller", "revocation", "callback", "panic"} {
		t.Run(cause, func(t *testing.T) {
			owner, old, next := renewalFixture()
			var fenced bool
			var frames []string
			s := &server{grants: grantState{initialized: true, enabled: true, owner: owner, grants: old}, fence: func(error) { fenced = true }, writeFrame: func(raw []byte) { frames = append(frames, string(raw)) }, outputLimit: DefaultFrameBytes}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s.plugin = &renewalPlugin{accept: func(call context.Context, _ capability.GrantSet) error {
				switch cause {
				case "caller":
					cancel()
					return call.Err()
				case "revocation":
					s.grants.end()
					return nil
				case "panic":
					panic("callback panic")
				default:
					return context.Canceled
				}
			}}
			p := GrantsRenewParams{1, 1, owner, next, ForwardContext{TimeoutMS: 1000}}
			raw, _ := json.Marshal(p)
			s.renewGrants(ctx, RPCRequest{ID: NumberID(2), Method: MethodGrantsRenew, Params: json.RawMessage(raw)})
			if !fenced || len(frames) != 1 || !strings.Contains(frames[0], `"error"`) {
				t.Fatal("failed update not fenced")
			}
			if _, live := s.grants.snapshot(); live {
				t.Fatal("failed discovery live")
			}
		})
	}
}

func TestGrantRenewalServeAckPrecedesHostCommit(t *testing.T) {
	owner, old, next := renewalFixture()
	entered, release := make(chan struct{}), make(chan struct{})
	p := &renewalPlugin{accept: func(ctx context.Context, grants capability.GrantSet) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	in, send := io.Pipe()
	receive, out := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- ServeWithOptions(p, ServeOptions{Input: in, Output: out}) }()
	t.Cleanup(func() { send.Close(); receive.Close(); in.Close(); out.Close() })
	reader := bufio.NewReader(receive)
	read := func() json.RawMessage {
		t.Helper()
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Error  *RPCError       `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Error != nil {
			t.Fatalf("reply %s", raw)
		}
		return response.Result
	}
	write := func(id int64, method string, params any) {
		t.Helper()
		raw, err := json.Marshal(RPCRequest{JSONRPC: "2.0", ID: NumberID(id), Method: method, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprintf(send, "%s\n", raw); err != nil {
			t.Fatal(err)
		}
	}
	init := validInitParams()
	init.Incarnation = owner
	init.Grants = old.Clone()
	version := GrantsRenewalVersion
	init.GrantsRenewalVersion = &version
	write(1, MethodInit, init)
	var initialized InitResult
	if err := json.Unmarshal(read(), &initialized); err != nil || initialized.GrantsRenewalVersion == nil {
		t.Fatalf("renewal not advertised %v", err)
	}
	hostGrants := old.Clone()
	write(2, MethodGrantsRenew, GrantsRenewParams{1, 1, owner, next, ForwardContext{TimeoutMS: 1000}})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback not reached")
	}
	if hostGrants[0].ExpiresAt != old[0].ExpiresAt {
		t.Fatal("host committed before ack")
	}
	close(release)
	var ack GrantsRenewResult
	if err := json.Unmarshal(read(), &ack); err != nil || ack.Sequence != 1 || ack.Incarnation != owner {
		t.Fatalf("invalid ack %v", err)
	}
	if err := hostGrants.ValidateRenewal(next, owner, time.Now()); err != nil {
		t.Fatal(err)
	}
	hostGrants = next.Clone()
	if hostGrants[0].ExpiresAt != next[0].ExpiresAt {
		t.Fatal("host commit not applied")
	}
	write(3, MethodUnload, map[string]any{})
	send.Close()
	read()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not reap")
	}
}

func TestGrantRenewalOfferAndClosedCodecs(t *testing.T) {
	owner, _, next := renewalFixture()
	params := GrantsRenewParams{1, 1, owner, next, ForwardContext{TimeoutMS: 1000}}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"sequence":1`, `"sequence":1e0`, 1), strings.Replace(string(raw), `"renewal_version":1`, `"renewal_version":2`, 1), strings.Replace(string(raw), `"grants":`, `"extra":true,"grants":`, 1)} {
		var p GrantsRenewParams
		if json.Unmarshal([]byte(bad), &p) == nil {
			t.Fatal("invalid renewal decoded")
		}
	}
	ack := GrantsRenewResult{1, 1, owner}
	encoded, err := json.Marshal(ack)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GrantsRenewResult
	if json.Unmarshal(encoded, &decoded) != nil || decoded != ack {
		t.Fatal("ack codec")
	}
	params.Context.TimeoutMS = 0
	if _, err := json.Marshal(params); err == nil {
		t.Fatal("invalid producer admitted")
	}
	init := validInitParams()
	version := GrantsRenewalVersion
	result := InitResult{ID: "test", Name: "Test", Version: "1", Protocol: 2, CapabilityContract: 1, GrantsRenewalVersion: &version}
	if ValidateInitResult(init, result) == nil {
		t.Fatal("unsolicited feature acknowledged")
	}
	init.GrantsRenewalVersion = &version
	if err := ValidateInitResult(init, result); err != nil {
		t.Fatal(err)
	}
}

func TestGrantRenewalRefreshesCleanupDiscovery(t *testing.T) {
	init := hostClientInitFixture(t)
	next := init.Grants.Clone()
	for i := range next {
		next[i].ExpiresAt = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339Nano)
	}
	n := &reverseNegotiation{offered: true, params: init}
	n.acceptedGrants(next)
	next[0].Scope[0] = 'X'
	if string(n.params.Grants[0].Scope) != string(init.Grants[0].Scope) {
		t.Fatal("renewed cleanup snapshot retained mutable scope")
	}
	if n.params.Grants[0].ExpiresAt == init.Grants[0].ExpiresAt {
		t.Fatal("cleanup retained initial expiry")
	}
	if n.params.Grants[0].GrantID != init.Grants[0].GrantID {
		t.Fatal("cleanup authority changed")
	}
}
