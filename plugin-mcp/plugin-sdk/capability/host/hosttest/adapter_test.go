package hosttest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/capability"
	"github.com/hollis-labs/plugin-sdk/capability/host"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// referenceAdapter is a real HTTP dispatcher over an isolated policy ledger,
// credential store, budget and mutable backend. Mutants alter those adapters;
// they do not manufacture a failure list or a canned probe reply.
type referenceAdapter struct{ broken string }
type referenceInstance struct {
	mu, guard                                  sync.Mutex
	fixture                                    Fixture
	observer                                   *Observer
	catalog                                    *capability.Catalog
	server                                     *httptest.Server
	plugin                                     *httptest.Server
	proxy                                      *httptest.Server
	client                                     *http.Client
	credentials                                *host.CredentialStore
	token, binding, broken                     string
	active, revoked, cycle, exhausted          bool
	generation                                 uint64
	audience, wrongOwner, revision, toolEffect string
	policy                                     capability.Scope
	bindingScopes                              map[string]capability.Scope
	lease                                      context.Context
	cancel                                     context.CancelFunc
	grants                                     capability.GrantSet
	inflight                                   int
	depth                                      int
	releases                                   []func()
	store                                      map[string]int
	artifacts                                  map[string]string
}

func (a referenceAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	supported := []string{}
	extensions := []capability.Descriptor{}
	for _, d := range f.Catalog {
		shared := false
		for _, known := range capability.SharedDescriptors() {
			if known.Name == d.Name {
				shared = true
				break
			}
		}
		if shared {
			supported = append(supported, d.Name)
		} else {
			extensions = append(extensions, d)
		}
	}
	extensions = append(extensions, f.Overrides...)
	if a.broken == "catalog override" {
		extensions = nil
	}
	catalog, err := capability.NewCatalog(supported, extensions)
	if err != nil {
		return nil, err
	}
	lease, cancel := context.WithCancel(context.Background())
	r := &referenceInstance{fixture: f, observer: o, catalog: catalog, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, broken: a.broken, generation: 1, audience: "stdio", revision: "1", toolEffect: "write", policy: cloneScope(f.Policy), grants: append(capability.GrantSet{}, f.Grants...), lease: lease, cancel: cancel, store: map[string]int{}, artifacts: map[string]string{}}
	r.bindingScopes = map[string]capability.Scope{}
	for _, g := range f.Grants {
		var scope capability.Scope
		json.Unmarshal(g.Scope, &scope)
		r.bindingScopes[g.GrantID] = scope
	}
	r.credentials, err = host.NewCredentialStore(host.CredentialConfig{HostInstance: f.Runtime.HostInstance, Audience: "loopback"})
	if err != nil {
		cancel()
		return nil, err
	}
	if err = r.credentials.ActivateOwner(f.Runtime); err != nil {
		r.Close()
		return nil, err
	}
	var binding [32]byte
	if _, err = rand.Read(binding[:]); err != nil {
		r.Close()
		return nil, err
	}
	r.binding = base64.RawURLEncoding.EncodeToString(binding[:])
	if len(f.Grants) > 0 {
		claims := host.CredentialClaims{Subject: host.Subject{Kind: host.SessionClient, ID: "session-client"}, Owner: f.Runtime, Audience: "loopback", GrantIDs: []string{}, Scopes: map[string]capability.Scope{}, CapabilityNames: map[string]string{}, GrantExpiresAt: map[string]time.Time{}}
		for _, g := range f.Grants {
			claims.GrantIDs = append(claims.GrantIDs, g.GrantID)
			claims.Scopes[g.GrantID] = cloneScope(r.bindingScopes[g.GrantID])
			claims.CapabilityNames[g.GrantID] = g.Name
			expiry, _ := time.Parse(time.RFC3339Nano, g.ExpiresAt)
			claims.GrantExpiresAt[g.GrantID] = expiry
		}
		issued, issueErr := r.credentials.Issue(claims, time.Minute)
		if issueErr != nil {
			r.Close()
			return nil, issueErr
		}
		r.token, err = issued.Reveal()
		if err != nil {
			r.Close()
			return nil, err
		}
		var logs bytes.Buffer
		fmt.Fprintln(&logs, "credential", issued, "broker", "[secret redacted]")
		if a.broken == "secret leak" {
			fmt.Fprintln(&logs, r.token, f.Secret)
		}
		registry, _ := json.Marshal(map[string]any{"credential": issued, "capability": f.Catalog[0].Name})
		browser, _ := json.Marshal(map[string]any{"credential": issued, "ui": "fixture"})
		r.artifacts = map[string]string{"logs": logs.String(), "registry": string(registry), "browser": string(browser)}
	}
	r.plugin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var caller host.Subject
		if json.NewDecoder(req.Body).Decode(&caller) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		r.observer.ReceivedCaller(caller)
		w.WriteHeader(http.StatusNoContent)
	}))
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	r.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.observer.ProxyRequest()
		forward := req.Clone(req.Context())
		forward.RequestURI = ""
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		response, err := transport.RoundTrip(forward)
		if err != nil {
			http.Error(w, "proxy failed", 502)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)
	}))
	return r, nil
}
func (r *referenceInstance) Access() (string, string) { return r.binding, r.token }
func (r *referenceInstance) Close() error {
	r.fixture.Gate.Release()
	r.cancel()
	if r.credentials != nil {
		r.credentials.Close()
	}
	if r.server != nil {
		r.server.Close()
	}
	if r.plugin != nil {
		r.plugin.Close()
	}
	if r.proxy != nil {
		r.proxy.Close()
	}
	r.client.CloseIdleConnections()
	return nil
}
func (r *referenceInstance) Artifacts(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMap(r.artifacts), nil
}
func (r *referenceInstance) Activate(ctx context.Context, raw json.RawMessage, requests []host.Request) (capability.GrantSet, []host.PlanNotice, error) {
	body, _ := json.Marshal(struct {
		Init     json.RawMessage
		Requests []host.Request
	}{raw, requests})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server.URL+"/activate", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	var result struct {
		Grants  capability.GrantSet
		Notices []host.PlanNotice
		Error   *capability.Error
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, nil, err
	}
	if result.Error != nil {
		return nil, nil, result.Error
	}
	return result.Grants, result.Notices, nil
}
func application(code capability.Code, id capability.RequestID, detail capability.FailureDetail) Reply {
	data, err := (&capability.Error{Code: code, RequestID: id, EffectState: capability.NotStarted, Detail: detail}).RPCData()
	if err != nil {
		panic(err)
	}
	raw, _ := json.Marshal(data)
	return Reply{RPCCode: capability.HostRPCErrorCode, Failure: &data, Data: raw}
}
func (r *referenceInstance) Invoke(ctx context.Context, a Attempt) (Reply, error) {
	if a.Proxy && r.broken != "proxy routing" {
		return application(capability.ScopeDenied, a.Call.RequestID, ""), nil
	}
	path := "/rpc/"
	if a.Bridge {
		path = "/bridge/"
	}
	if a.Redirect {
		path = "/redirect/"
	}
	binding, token := a.BindingID, a.Credential
	a.BindingID = ""
	a.Credential = ""
	body, _ := json.Marshal(struct {
		Attempt Attempt
		Payload string
	}{a, strings.Repeat("x", int(a.BodyBytes))})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server.URL+path+url.PathEscape(a.Call.Operation), bytes.NewReader(body))
	if err != nil {
		return Reply{}, err
	}
	request.Header.Set("X-Binding", binding)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if a.Origin != "" {
		request.Header.Set("Origin", a.Origin)
	}
	client := r.client
	if a.Proxy {
		proxyURL, _ := url.Parse(r.proxy.URL)
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		copy := *r.client
		copy.Transport = transport
		client = &copy
	}
	response, err := client.Do(request)
	if err != nil {
		return Reply{}, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return application(capability.ScopeDenied, a.Call.RequestID, ""), nil
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return Reply{}, err
	}
	var reply Reply
	err = json.Unmarshal(raw, &reply)
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(raw, &fields)
		reply.Data = fields["Failure"]
	}
	return reply, err
}
func (r *referenceInstance) Change(ctx context.Context, c Change) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var early []func()
	defer func() {
		for _, release := range early {
			release()
		}
	}()
	r.guard.Lock()
	defer r.guard.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	switch c.Kind {
	case "revoked":
		r.revoked = true
		r.cancel()
	case "expired":
		for index := range r.grants {
			r.grants[index].IssuedAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
			r.grants[index].ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		}
	case "audience":
		r.audience = "different"
	case "host epoch":
		for index := range r.grants {
			r.grants[index].HostInstance = "old-host"
		}
	case "owner":
		r.wrongOwner = "different"
	case "generation":
		for index := range r.grants {
			r.grants[index].OwnerGeneration++
		}
	case "replace generation":
		r.generation++
		for index := range r.grants {
			r.grants[index].OwnerGeneration = r.generation
		}
		r.cancel()
	case "widen binding":
		scope := r.bindingScopes["grant"]
		scope.Allowlists["keys"] = append(scope.Allowlists["keys"], "other")
		r.bindingScopes["grant"] = scope
	case "deny caller":
		caller := host.Subject{Kind: host.UserActor, ID: "verified-user"}
		r.fixture.Caller = &caller
		scope := cloneScope(r.policy)
		scope.Allowlists["server_tools"] = nil
		r.fixture.CallerPolicy = &scope
	case "tool revision":
		r.revision = c.Value
	case "tool effect":
		r.toolEffect = c.Value
	case "cycle":
		r.cycle = true
	case "depth":
		r.policy.Limits["call_depth"] = 1
		r.depth = 2
	case "rate exhausted":
		r.exhausted = true
	case "disable", "stop", "reload", "disconnect":
		r.active = false
		r.cancel()
		r.credentials.RevokeOwner(r.fixture.Runtime)
		if r.broken == "early release" {
			early = append(early, r.releases...)
		}
	default:
		return fmt.Errorf("unknown fixture control %q", c.Kind)
	}
	return nil
}
func (r *referenceInstance) serve(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/catalog" {
		descriptors := []capability.Descriptor{}
		for _, d := range r.fixture.Catalog {
			known, err := r.catalog.Lookup(d.Name, d.SchemaVersion)
			if err != nil {
				http.Error(w, "catalog unavailable", 500)
				return
			}
			descriptors = append(descriptors, known)
		}
		if r.broken == "catalog overclaim" {
			d := descriptors[0]
			d.Name = "host.example.unimplemented"
			d.Operations = []string{"unimplemented/read"}
			descriptors = append(descriptors, d)
		}
		json.NewEncoder(w).Encode(descriptors)
		return
	}
	if req.URL.Path == "/activate" {
		r.activate(w, req)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/redirect/") {
		http.Redirect(w, req, r.server.URL+"/bridge/host%2Freadonly%2Fquery", http.StatusTemporaryRedirect)
		return
	}
	var envelope struct {
		Attempt Attempt
		Payload string
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 1<<20)).Decode(&envelope); err != nil {
		http.Error(w, "invalid", 400)
		return
	}
	a := envelope.Attempt
	bridge := strings.HasPrefix(req.URL.Path, "/bridge/")
	// The route supplies the actual operation/effect. Client effect hints cannot
	// relabel a write as a read. Client usage is replaced by measured byte demand.
	op := strings.TrimPrefix(strings.TrimPrefix(req.URL.Path, "/rpc/"), "/bridge/")
	a.Call.Operation = op
	if r.broken == "catalog overclaim" && op == "unimplemented/read" {
		r.writeReply(w, application(capability.UnsupportedCapability, a.Call.RequestID, ""))
		return
	}
	effect := capability.Write
	switch op {
	case "host/readonly/query", "register", "retrieve", "host/storage/get", "host/mcp/list_tools":
		effect = capability.Read
	case "host/storage/delete", "workflow/destructive":
		effect = capability.Destructive
	case "host/mcp/call_tool":
		r.mu.Lock()
		effect = capability.Effect(r.toolEffect)
		r.mu.Unlock()
	}
	a.Call.Effect = effect
	if r.broken == "effect hint" && a.EffectHint != "" {
		a.Call.Effect = a.EffectHint
	}
	if _, ok := a.Call.Usage["request_bytes"]; ok {
		a.Call.Usage["request_bytes"] = int64(len(envelope.Payload))
	}
	if _, ok := a.Call.Usage["response_bytes"]; ok {
		a.Call.Usage["response_bytes"] = a.ResponseBytes
	}
	if a.Call.Capability == capability.MCPReach {
		a.Call.Dimensions["server_tools"] = a.Call.Server + "/" + a.Call.Tool + "@1"
		r.mu.Lock()
		a.Call.Usage["call_depth"] = int64(max(1, r.depth))
		r.mu.Unlock()
	}
	audience := "stdio"
	if bridge {
		audience = "loopback"
	}
	if bridge && req.Header.Get("Origin") != "" {
		origin, err := url.Parse(req.Header.Get("Origin"))
		if err != nil || net.ParseIP(origin.Hostname()) == nil || !net.ParseIP(origin.Hostname()).IsLoopback() {
			r.writeReply(w, application(capability.ScopeDenied, a.Call.RequestID, ""))
			return
		}
	}
	resolver := resolverFunc(func(ctx context.Context, c host.Call) (host.Authority, error) {
		return r.authority(ctx, c, a, req, bridge)
	})
	budget := budgetAdapter{instance: r}
	auditor := host.NewAuditor(sinkFunc(func(_ context.Context, event host.AuditEvent) error {
		r.observer.Audit(event)
		if r.fixture.AuditFailure {
			return errors.New("sink unavailable")
		}
		return nil
	}))
	enforcer, err := host.NewEnforcer(host.EnforcerConfig{HostInstance: r.fixture.Runtime.HostInstance, Audience: audience, Catalog: r.catalog, Resolver: resolver, Budget: budget, Audit: auditor})
	if err != nil {
		panic(err)
	}
	if r.broken == "skip enforcement" {
		r.execute(a.Call)
		r.writeReply(w, Reply{})
		return
	}
	err = enforcer.Run(req.Context(), a.Call, func(ctx context.Context, p *host.Permit) error {
		if r.fixture.Gate != nil && !r.fixture.AfterCommit {
			r.fixture.Gate.Wait(ctx)
		}
		r.guard.Lock()
		if r.broken != "commit recheck" {
			if err := p.Recheck(); err != nil {
				r.guard.Unlock()
				return err
			}
		}
		if r.fixture.Caller != nil {
			caller := *r.fixture.Caller
			if r.broken == "delivery caller" {
				caller.ID = "unrelated-init-user"
			}
			body, _ := json.Marshal(caller)
			forward, _ := http.NewRequestWithContext(ctx, http.MethodPost, r.plugin.URL, bytes.NewReader(body))
			response, err := r.client.Do(forward)
			if err != nil {
				r.guard.Unlock()
				return err
			}
			response.Body.Close()
		}
		r.execute(a.Call)
		r.guard.Unlock()
		if r.fixture.Gate != nil && r.fixture.AfterCommit {
			r.fixture.Gate.Wait(ctx)
		}
		if r.fixture.FailAfterCommit {
			if r.broken == "retry unknown" {
				r.execute(a.Call)
			}
			return errors.New("backend outcome lost")
		}
		return nil
	})
	if r.broken == "audit changes decision" && auditor.Failures() > 0 {
		err = &capability.Error{Code: capability.CapabilityDenied, RequestID: a.Call.RequestID, EffectState: capability.NotStarted}
	}
	reply := Reply{}
	if err != nil {
		if r.broken == "denial after effect" {
			r.execute(a.Call)
		}
		var failure *capability.Error
		if !errors.As(err, &failure) {
			panic(err)
		}
		data, serialization := failure.RPCData()
		if serialization != nil {
			panic(serialization)
		}
		reply = Reply{RPCCode: capability.HostRPCErrorCode, Failure: &data}
	}
	if op == "host/mcp/list_tools" && reply.Failure == nil {
		r.mu.Lock()
		for _, tool := range []string{"tool", "ungranted-tool"} {
			if r.broken == "discovery leak" || slices.Contains(r.policy.Allowlists["server_tools"], "server/"+tool+"@1") {
				reply.Tools = append(reply.Tools, tool)
			}
		}
		r.mu.Unlock()
	}
	r.writeReply(w, reply)
}
func (r *referenceInstance) execute(c host.Call) {
	r.mu.Lock()
	r.store[c.Target+"/"+c.Operation]++
	r.mu.Unlock()
	r.observer.Execute(c)
}
func (r *referenceInstance) activate(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Init     json.RawMessage
		Requests []host.Request
	}
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		http.Error(w, "invalid", 400)
		return
	}
	var params subprocess.InitParams
	var err error
	if r.broken == "missing contract" {
		params.Grants = r.fixture.Grants
	} else {
		err = json.Unmarshal(input.Init, &params)
	}
	var grants capability.GrantSet
	var notices []host.PlanNotice
	if err == nil && len(input.Requests) > 0 {
		issued, expiry := fixtureTimes()
		grants, notices, err = host.ResolveGrants(req.Context(), r.catalog, planningResolver{r}, input.Requests, host.GrantPlan{Runtime: r.fixture.Runtime, Audience: "stdio", IssuedAt: issued, ExpiresAt: expiry, PolicyRevision: "review", NewGrantID: func() string { return "planned" }})
	} else if err == nil {
		grants = params.Grants
	}
	var failure *capability.Error
	if err != nil {
		if !errors.As(err, &failure) {
			failure = &capability.Error{Code: capability.InvalidRequest, EffectState: capability.NotStarted}
		}
	} else {
		r.mu.Lock()
		r.active = true
		r.grants = grants
		r.bindingScopes = map[string]capability.Scope{}
		for _, grant := range grants {
			var scope capability.Scope
			json.Unmarshal(grant.Scope, &scope)
			r.bindingScopes[grant.GrantID] = scope
		}
		r.mu.Unlock()
		r.observer.Activate()
	}
	json.NewEncoder(w).Encode(struct {
		Grants  capability.GrantSet
		Notices []host.PlanNotice
		Error   *capability.Error
	}{grants, notices, failure})
}
func (r *referenceInstance) authority(ctx context.Context, c host.Call, a Attempt, req *http.Request, bridge bool) (host.Authority, error) {
	if err := ctx.Err(); err != nil {
		return host.Authority{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	base := host.Authority{Authenticated: true, Background: true, Actor: host.Subject{Kind: host.PluginActor, ID: r.fixture.Runtime.OwnerID}, Owner: r.fixture.Runtime, Audience: r.audience, PolicyRevision: "review", Policy: cloneScope(r.policy), LeaseContext: r.lease, Active: r.active, TargetAvailable: r.active}
	if bridge {
		base.Audience = "loopback"
	}
	if !r.active {
		return base, nil
	}
	if !bridge && req.Header.Get("X-Binding") != r.binding {
		code := capability.CapabilityDenied
		if req.Header.Get("Authorization") != "" {
			code = capability.Unauthenticated
		}
		return base, &capability.Error{Code: code, EffectState: capability.NotStarted}
	}
	if r.generation != 1 && r.broken != "generation replay" {
		return base, &capability.Error{Code: capability.TargetUnavailable, Detail: capability.StaleBinding, EffectState: capability.NotStarted}
	}
	for _, g := range r.grants {
		if g.GrantID == c.GrantID {
			base.Grant = g
			break
		}
	}
	if base.Grant.GrantID == "" {
		return base, &capability.Error{Code: capability.CapabilityDenied, EffectState: capability.NotStarted}
	}
	base.Grant.Scope = append(json.RawMessage(nil), base.Grant.Scope...)
	if r.broken == "generation replay" {
		base.Grant.OwnerGeneration = 1
		base.LeaseContext = context.Background()
	}
	if r.wrongOwner != "" {
		base.Grant.OwnerID = r.wrongOwner
	}
	if r.revoked {
		return base, &capability.Error{Code: capability.CapabilityDenied, EffectState: capability.NotStarted}
	}
	if r.cycle {
		return base, &capability.Error{Code: capability.ScopeDenied, Detail: capability.CallbackCycle, EffectState: capability.NotStarted}
	}
	if c.Capability == capability.MCPReach && r.revision != "1" {
		return base, &capability.Error{Code: capability.TargetUnavailable, Detail: capability.StaleBinding, EffectState: capability.NotStarted}
	}
	scope := cloneScope(r.bindingScopes[c.GrantID])
	if r.broken == "grant union" && len(r.grants) > 1 {
		var combined capability.Scope
		json.Unmarshal(base.Grant.Scope, &combined)
		for _, g := range r.grants {
			var extra capability.Scope
			json.Unmarshal(g.Scope, &extra)
			combined.Allowlists["keys"] = append(combined.Allowlists["keys"], extra.Allowlists["keys"]...)
		}
		slices.Sort(combined.Allowlists["keys"])
		combined.Allowlists["keys"] = slices.Compact(combined.Allowlists["keys"])
		base.Grant.Scope, _ = json.Marshal(combined)
		scope = cloneScope(combined)
	}
	if r.fixture.UnsafeInstall && r.broken == "unsafe widens" {
		var widened capability.Scope
		json.Unmarshal(base.Grant.Scope, &widened)
		widened.Allowlists["targets"] = append(widened.Allowlists["targets"], "other")
		base.Grant.Scope, _ = json.Marshal(widened)
		base.Policy = cloneScope(widened)
		scope = cloneScope(widened)
	}
	base.TransportScope = &scope
	if r.fixture.Caller != nil {
		caller := *r.fixture.Caller
		base.InitiatingCaller = &caller
		base.Background = false
		policy := cloneScope(*r.fixture.CallerPolicy)
		base.CallerPolicy = &policy
		if r.broken == "init caller" {
			base.InitiatingCaller.ID = "unrelated-init-user"
			copy := cloneScope(base.Policy)
			base.CallerPolicy = &copy
		}
		if r.broken == "caller policy" {
			copy := cloneScope(base.Policy)
			base.CallerPolicy = &copy
		}
	}
	if bridge {
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		verified, err := r.credentials.Verify(token, host.Subject{Kind: host.SessionClient, ID: "session-client"}, r.fixture.Runtime, "loopback")
		if err != nil {
			return base, err
		}
		base.Background = false
		base.Actor = verified.Claims.Subject
		base.InitiatingCaller = &verified.Claims.Subject
		policy := cloneScope(base.Policy)
		base.CallerPolicy = &policy
		base.Audience = "loopback"
		base.Grant.Audience = "loopback"
		scope = cloneScope(verified.Claims.Scopes[c.GrantID])
		base.TransportScope = &scope
		base.LeaseContext = verified.Context
	}
	return base, nil
}

type resolverFunc func(context.Context, host.Call) (host.Authority, error)

func (f resolverFunc) Resolve(ctx context.Context, c host.Call) (host.Authority, error) {
	return f(ctx, c)
}

type sinkFunc func(context.Context, host.AuditEvent) error

func (f sinkFunc) Record(ctx context.Context, e host.AuditEvent) error { return f(ctx, e) }

type budgetAdapter struct{ instance *referenceInstance }

func (b budgetAdapter) Reserve(ctx context.Context, a host.Authority, c host.Call) (func(), error) {
	r := b.instance
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exhausted {
		return nil, &capability.Error{Code: capability.RateLimited, EffectState: capability.NotStarted}
	}
	if limit, ok := a.Policy.Limits["concurrency"]; ok && r.inflight >= int(limit) && r.broken != "concurrency overflow" {
		return nil, &capability.Error{Code: capability.BudgetExceeded, EffectState: capability.NotStarted}
	}
	r.inflight++
	r.observer.Reserve()
	var once sync.Once
	release := func() { once.Do(func() { r.mu.Lock(); r.inflight--; r.mu.Unlock(); r.observer.Release() }) }
	r.releases = append(r.releases, release)
	return release, nil
}

type planningResolver struct{ instance *referenceInstance }

func (p planningResolver) Scopes(ctx context.Context, request host.Request) (host.ScopeInputs, error) {
	r := p.instance
	r.mu.Lock()
	defer r.mu.Unlock()
	return host.ScopeInputs{Supported: cloneScope(r.policy), Approved: cloneScope(r.policy), Policy: cloneScope(r.policy)}, nil
}
func (r *referenceInstance) writeReply(w http.ResponseWriter, reply Reply) {
	w.Header().Set("Content-Type", "application/json")
	if r.broken == "error payload leak" && reply.Failure != nil {
		raw, _ := json.Marshal(reply.Failure)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		fields["stack"] = "private diagnostic"
		json.NewEncoder(w).Encode(map[string]any{"RPCCode": reply.RPCCode, "Failure": fields})
		return
	}
	json.NewEncoder(w).Encode(reply)
}

func TestReferenceAdapterConformance(t *testing.T) { Run(t, referenceAdapter{}) }
func TestBrokenAdaptersMustFail(t *testing.T) {
	cases := map[string]struct{ requirement, probe string }{
		"missing contract":       {"C01", "missing capability_contract"},
		"catalog override":       {"C02", "shared descriptor cannot be overwritten"},
		"denial after effect":    {"C03", "direct absent binding"},
		"skip enforcement":       {"C03", "direct absent binding"},
		"grant union":            {"C03", "single grant cannot borrow another scope"},
		"caller policy":          {"C04", "forged caller cannot replace verified caller"},
		"delivery caller":        {"C04", "delegated identity reaches audit"},
		"init caller":            {"C04", "delegated identity reaches audit"},
		"error payload leak":     {"C03", "direct target"},
		"generation replay":      {"C04", "replayed generation binding is fenced"},
		"discovery leak":         {"C05", "discovery filters ungranted tools"},
		"effect hint":            {"C05", "read hint cannot authorize a write tool"},
		"concurrency overflow":   {"C05", "concurrency is reserved atomically"},
		"unsafe widens":          {"C08", "unsafe install does not widen grants and artifacts redact"},
		"commit recheck":         {"C06", "stop before commit"},
		"early release":          {"C06", "stop before commit"},
		"proxy routing":          {"C07", "bridge proxy"},
		"secret leak":            {"C08", "unsafe install does not widen grants and artifacts redact"},
		"audit changes decision": {"C10", "audit failure never changes decisions"},
		"retry unknown":          {"C06", "unknown write outcome never retries"},
		"catalog overclaim":      {"C10", capability.StorageWrite + " scoped and direct bypass"},
	}
	for broken, expected := range cases {
		t.Run(broken, func(t *testing.T) {
			failures := Check(context.Background(), referenceAdapter{broken})
			found := false
			for _, failure := range failures {
				if failure.Requirement == expected.requirement && failure.Probe == expected.probe {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("broken adapter passed %s/%s: %v", expected.requirement, expected.probe, failures)
			}
		})
	}
}

func (r *referenceInstance) Descriptors(ctx context.Context) ([]capability.Descriptor, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server.URL+"/catalog", nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var descriptors []capability.Descriptor
	err = json.NewDecoder(response.Body).Decode(&descriptors)
	return descriptors, err
}

func TestCheckRejectsMissingAdapterAndContainsPanic(t *testing.T) {
	if len(Check(context.Background(), nil)) == 0 || len(Check(nil, referenceAdapter{})) == 0 {
		t.Fatal("missing adapter/context passed")
	}
	failures := Check(context.Background(), panicAdapter{})
	if len(failures) == 0 {
		t.Fatal("panicking adapter passed")
	}
	for _, f := range failures {
		if f.Reason != "adapter panicked" {
			t.Fatalf("unsafe panic disclosure: %v", f)
		}
	}
}

type panicAdapter struct{}

func (panicAdapter) Open(context.Context, Fixture, *Observer) (Instance, error) {
	panic("private credential diagnostic")
}
