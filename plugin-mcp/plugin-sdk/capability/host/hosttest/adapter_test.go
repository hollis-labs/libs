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
	"regexp"
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
type requestKey struct {
	actor      host.Subject
	connection string
	id         capability.RequestID
}
type toolDefinition struct {
	revision string
	effect   capability.Effect
}
type referenceInstance struct {
	mu, guard              sync.Mutex
	fixture                Fixture
	observer               *Observer
	catalog                *capability.Catalog
	server                 *httptest.Server
	redirectTarget         *httptest.Server
	plugin                 *httptest.Server
	proxy                  *httptest.Server
	client                 *http.Client
	credentials            *host.CredentialStore
	token, binding, broken string
	active                 bool
	unavailable            bool
	rateUsed               int64
	graph                  map[string][]string
	tools                  map[string]toolDefinition
	nativeEffects          map[string]capability.Effect
	generation             uint64
	audience, wrongOwner   string
	policy                 capability.Scope
	bindingScopes          map[string]capability.Scope
	lease                  context.Context
	cancel                 context.CancelFunc
	grants                 capability.GrantSet
	inflight               int
	depth                  int
	releases               []func()
	requests               map[requestKey]context.CancelFunc
	store                  map[string]int
	artifacts              map[string]string
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
	r := &referenceInstance{fixture: f, observer: o, catalog: catalog, client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, broken: a.broken, generation: 1, audience: "stdio", policy: cloneScope(f.Policy), grants: append(capability.GrantSet{}, f.Grants...), lease: lease, cancel: cancel, requests: map[requestKey]context.CancelFunc{}, graph: map[string][]string{}, tools: map[string]toolDefinition{"server/tool": {"1", capability.Write}}, store: map[string]int{}, artifacts: map[string]string{}}
	r.nativeEffects = map[string]capability.Effect{}
	for _, d := range extensions {
		for _, op := range d.Operations {
			r.nativeEffects[op] = d.EffectCeiling
		}
	}
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
	r.redirectTarget = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.execute(host.Call{Capability: capability.MCPReach, Operation: req.URL.Query().Get("operation"), Target: "owner", Effect: capability.Write, RequestID: 1})
		r.writeReply(w, Reply{})
	}))
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	if r.broken == "follow redirects" || r.broken == "redirect executes" || r.broken == "redirect hides execution" {
		r.client.CheckRedirect = nil
	}
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
func (r *referenceInstance) Access() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.binding, r.token
}
func (r *referenceInstance) Close() error {
	r.fixture.Gate.Release()
	r.fixture.SecondGate.Release()
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	cancel()
	if r.credentials != nil {
		r.credentials.Close()
	}
	if r.server != nil {
		r.server.Close()
	}
	if r.plugin != nil {
		r.plugin.Close()
	}
	if r.redirectTarget != nil {
		r.redirectTarget.Close()
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
	if a.Proxy && r.broken == "proxy routing" {
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
	if a.Redirect && r.broken == "redirect hides execution" {
		refusal := application(capability.ScopeDenied, a.Call.RequestID, "")
		refusal.HTTPStatus = http.StatusFound
		return refusal, nil
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		// Normalize an observed HTTP redirect as a client-side refusal. A client
		// that follows it reaches the separately instrumented execution endpoint.
		refusal := application(capability.ScopeDenied, a.Call.RequestID, "")
		if r.broken == "redirect wrong refusal" {
			refusal = application(capability.Conflict, a.Call.RequestID, "")
		}
		refusal.HTTPStatus = response.StatusCode
		if r.broken == "redirect wrong status" {
			refusal.HTTPStatus = http.StatusOK
		}
		return refusal, nil
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
		reply.Wire = append(json.RawMessage(nil), raw...)
		reply.WireBytes = int64(len(raw))
		reply.HTTPStatus = response.StatusCode
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
		if r.broken == "scope revoked" && r.fixture.Catalog[0].Name == workflowName {
			return nil
		}
		if r.broken == "revoked ignored" {
			return nil
		}
		r.grants = nil
		r.cancel()
	case RestoreExpiry:
		for index := range r.grants {
			r.grants[index].IssuedAt, r.grants[index].ExpiresAt = fixtureTimes()
		}
	case RestoreRevocation:
		r.grants = append(capability.GrantSet{}, r.fixture.Grants...)
		r.lease, r.cancel = context.WithCancel(context.Background())
	case Reconnect:
		if r.broken == "reconnect broken" {
			return nil
		}
		r.fixture.Runtime.OwnerGeneration = r.generation
		for index := range r.grants {
			r.grants[index].OwnerGeneration = r.generation
		}
		r.lease, r.cancel = context.WithCancel(context.Background())
		var raw [32]byte
		rand.Read(raw[:])
		r.binding = base64.RawURLEncoding.EncodeToString(raw[:])
	case HostAvailable:
		r.unavailable = false
	case HostUnavailable:
		r.unavailable = true
		if r.broken == "unavailable as stop" {
			r.active = false
			r.cancel()
			r.credentials.RevokeOwner(r.fixture.Runtime)
		}
	case "expired":
		if r.broken == "scope expired" && r.fixture.Catalog[0].Name == workflowName {
			return nil
		}
		if r.broken == "expire as revoke" {
			r.cancel()
			return nil
		}
		for index := range r.grants {
			r.grants[index].IssuedAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
			r.grants[index].ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		}
	case "audience":
		if r.broken == "error leak" {
			return fmt.Errorf("audience control failed for bearer %s", r.token)
		}
		r.audience = "different"
	case "host epoch":
		for index := range r.grants {
			r.grants[index].HostInstance = "old-host"
		}
	case "owner":
		if r.broken != "owner ignored" {
			r.wrongOwner = "different"
		}
	case "generation":
		for index := range r.grants {
			r.grants[index].OwnerGeneration++
		}
	case "replace generation":
		r.generation++
		r.cancel()
		r.lease, r.cancel = context.WithCancel(context.Background())
	case "widen binding":
		parts := strings.SplitN(c.Value, "/", 2)
		if len(parts) != 2 {
			return errors.New("invalid binding control")
		}
		scope := r.bindingScopes[parts[0]]
		scope.Allowlists[parts[1]] = append(scope.Allowlists[parts[1]], "other")
		r.bindingScopes[parts[0]] = scope
	case "deny caller":
		caller := host.Subject{Kind: host.UserActor, ID: "verified-user"}
		r.fixture.Caller = &caller
		scope := cloneScope(r.policy)
		scope.Allowlists["server_tools"] = nil
		r.fixture.CallerPolicy = &scope
	case "tool revision":
		d := r.tools["server/tool"]
		d.revision = c.Value
		r.tools["server/tool"] = d
	case "tool effect":
		d := r.tools["server/tool"]
		d.effect = capability.Effect(c.Value)
		r.tools["server/tool"] = d
	case "cycle":
		r.graph["server/tool@1"] = []string{"parent"}
		r.graph["parent"] = []string{"server/tool@1"}
	case "depth":
		r.policy.Limits["call_depth"] = 1
		r.depth = 2
	case "rate exhausted":
		r.rateUsed = r.policy.Limits["rate_per_minute"]
	case "disable", "stop", "reload", "disconnect":
		r.active = false
		r.cancel()
		r.credentials.RevokeOwner(r.fixture.Runtime)
		if r.broken == "post stop admission" {
			r.active = true
			r.lease, r.cancel = context.WithCancel(context.Background())
		}
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
		candidates := capability.SharedDescriptors()
		for _, d := range r.fixture.Catalog {
			if strings.HasPrefix(d.Name, "host.") || strings.HasPrefix(d.Name, "plugin.") {
				candidates = append(candidates, d)
			}
		}
		for _, d := range candidates {
			if known, err := r.catalog.Lookup(d.Name, d.SchemaVersion); err == nil {
				descriptors = append(descriptors, known)
			}
		}

		if r.broken == "publish reinterpreted" {
			for index := range descriptors {
				if descriptors[index].Name == capability.StorageWrite {
					descriptors[index].Description = "host reinterpretation of a shared name"
					descriptors[index].Operations = []string{"host/storage/put"}
					descriptors[index].Proposed = false
				}
			}
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
		var input struct{ Attempt Attempt }
		if json.NewDecoder(req.Body).Decode(&input) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		if r.broken != "redirect unobserved" {
			r.observer.RedirectResponse()
		}
		http.Redirect(w, req, r.redirectTarget.URL+"/?operation="+url.QueryEscape(input.Attempt.Call.Operation), http.StatusFound)
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
	// Negative variant admits a route absent from the installed catalog.
	if r.broken == "unsupported accepted" {
		if _, err := r.catalog.Lookup(a.Call.Capability, 1); err != nil {
			r.execute(a.Call)
			r.writeReply(w, Reply{})
			return
		}
	}

	if r.broken == "catalog overclaim" && op == "unimplemented/read" {
		r.writeReply(w, application(capability.UnsupportedCapability, a.Call.RequestID, ""))
		return
	}
	effect := capability.Write
	if reviewed, ok := r.nativeEffects[op]; ok {
		effect = reviewed
	}
	switch op {
	case "host/readonly/query", "register", "retrieve", "host/storage/get", "host/mcp/list_tools":
		effect = capability.Read
	case "host/storage/delete", "workflow/destructive":
		effect = capability.Destructive
	case "host/mcp/call_tool":
		r.mu.Lock()
		effect = r.tools[a.Call.Server+"/"+a.Call.Tool].effect
		if effect == "" {
			effect = capability.Write
		}
		r.mu.Unlock()
	}
	if r.broken == "scope effect" && a.Call.Capability == workflowName {
		effect = capability.Write
	}
	a.Call.Effect = effect
	if r.broken == "effect hint" && a.EffectHint != "" {
		a.Call.Effect = a.EffectHint
	}
	if _, ok := a.Call.Usage["request_bytes"]; ok {
		a.Call.Usage["request_bytes"] = int64(len(envelope.Payload))
		if r.broken == "scope budget" && a.Call.Capability == workflowName {
			a.Call.Usage["request_bytes"] = 1
		}
	}
	if _, ok := a.Call.Usage["response_bytes"]; ok {
		result := Reply{Payload: strings.Repeat("x", int(max(1, r.fixture.OutputBytes)))}
		raw, _ := json.Marshal(result)
		a.Call.Usage["response_bytes"] = int64(len(raw) + 1)
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
		event.TraceID = r.adaptOutput("audit", "broker="+event.TraceID)
		r.observer.Audit(event)
		raw, _ := json.Marshal(event)
		r.mu.Lock()
		r.artifacts["audit"] = string(raw)
		r.mu.Unlock()
		if r.fixture.AuditFailure {
			return errors.New("sink unavailable")
		}
		return nil
	}))
	requestContext, cancelRequest := context.WithCancel(req.Context())
	if bridge && r.broken == "cancel credential" {
		cancelRequest()
		requestContext, cancelRequest = context.WithCancel(context.WithoutCancel(req.Context()))
		stop := context.AfterFunc(req.Context(), func() { r.credentials.RevokeOwner(r.fixture.Runtime); cancelRequest() })
		defer stop()
	}
	defer cancelRequest()

	enforcer, err := host.NewEnforcer(host.EnforcerConfig{HostInstance: r.fixture.Runtime.HostInstance, Audience: audience, Catalog: r.catalog, Resolver: resolver, Budget: budget, Audit: auditor})
	if err != nil {
		panic(err)
	}
	if r.broken == "skip enforcement" {
		r.execute(a.Call)
		r.writeReply(w, Reply{})
		return
	}
	if r.broken == "list without admission" && op == "host/mcp/list_tools" {
		reply := Reply{}
		r.mu.Lock()
		for _, tool := range []string{"tool", "ungranted-tool"} {
			if slices.Contains(r.policy.Allowlists["server_tools"], "server/"+tool+"@1") {
				reply.Tools = append(reply.Tools, tool)
			}
		}
		r.mu.Unlock()
		r.writeReply(w, reply)
		return
	}
	if (r.broken == "measured output" || r.broken == "late read limit" || r.broken == "output after write") && bridge {
		if _, ok := a.Call.Usage["response_bytes"]; ok {
			a.Call.Usage["response_bytes"] = 1
		}
	}
	started := false
	err = enforcer.Run(requestContext, a.Call, func(ctx context.Context, p *host.Permit) error {
		started = true
		actor := host.Subject{Kind: host.PluginActor, ID: r.fixture.Runtime.OwnerID}
		if bridge {
			actor = host.Subject{Kind: host.SessionClient, ID: "session-client"}
		}
		connection := req.Header.Get("X-Binding")
		if bridge {
			connection = ""
		}
		key := requestKey{actor: actor, connection: connection, id: a.Call.RequestID}
		if op == "host/mcp/call_tool" {
			r.mu.Lock()
			r.requests[key] = cancelRequest
			r.mu.Unlock()
			defer func() { r.mu.Lock(); delete(r.requests, key); r.mu.Unlock() }()
		}
		gate := r.fixture.Gate
		if a.Call.RequestID == 3 && r.fixture.SecondGate != nil {
			gate = r.fixture.SecondGate
		}
		if gate != nil && !r.fixture.AfterCommit && op != "host/mcp/cancel_call" && op != "host/mcp/list_tools" {
			gate.Wait(ctx)
		}
		r.guard.Lock()
		if r.broken != "commit recheck" && !(r.broken == "bridge cancel" && bridge) {
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
			if r.broken == "claimed caller" && a.ClaimedCaller != "" {
				caller.ID = a.ClaimedCaller
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
		if op == "host/mcp/cancel_call" && r.broken != "cancel ignored" {
			r.mu.Lock()
			target := r.requests[requestKey{actor: actor, connection: connection, id: a.CancelID}]
			if r.broken == "cross actor cancel" {
				for key, stop := range r.requests {
					if key.id == a.CancelID {
						target = stop
					}
				}
			}
			r.mu.Unlock()
			if target != nil {
				target()
			}
			if r.broken == "cancel all" {
				r.mu.Lock()
				for _, stop := range r.requests {
					stop()
				}
				r.mu.Unlock()
				if r.fixture.SecondGate != nil {
					<-r.fixture.SecondGate.Cancelled
				}
			}
		}
		actualCall := a.Call
		if r.broken == "output after write" && bridge && r.fixture.OutputBytes > 1000 {
			actualCall.Effect = capability.Write
		}
		r.execute(actualCall)
		r.guard.Unlock()
		if (r.broken == "measured output" && bridge && a.ResponseBytes > 1000) || ((r.broken == "late read limit" || r.broken == "output after write") && bridge && r.fixture.OutputBytes > 1000) {
			// Host bounds the ACTUAL produced response after the read ran.
			return &capability.Error{Code: capability.BudgetExceeded, EffectState: capability.NotCommitted}
		}
		if r.fixture.Gate != nil && r.fixture.AfterCommit {
			r.fixture.Gate.Wait(ctx)
		}
		if r.fixture.FailAfterCommit {
			if r.broken == "retry unknown" {
				r.execute(a.Call)
			}
			diagnostic := fmt.Sprintf("backend outcome lost: broker=%s", r.fixture.Secret)
			diagnostic = r.adaptOutput("errors", diagnostic)
			diagnostic = r.adaptOutput("replies", "broker="+r.fixture.Secret+" "+diagnostic)
			r.mu.Lock()
			r.artifacts["logs"] += r.adaptOutput("logs", "broker="+r.fixture.Secret)
			r.mu.Unlock()
			return errors.New(diagnostic)
		}
		return nil
	})
	if r.broken == "cancel all" && r.fixture.SecondGate != nil && a.Call.RequestID == 3 && a.Call.Operation == "host/mcp/call_tool" && err != nil {
		r.execute(a.Call)
		err = nil
	}
	if r.broken == "audit changes decision" && auditor.Failures() > 0 {
		err = &capability.Error{Code: capability.CapabilityDenied, RequestID: a.Call.RequestID, EffectState: capability.NotStarted}
	}
	if r.broken == "withdrawal executes" && started && err != nil && r.fixture.Gate != nil && !r.fixture.AfterCommit {
		r.execute(a.Call)
	}
	if r.broken == "definite failure" && r.fixture.AfterCommit && err == nil {
		err = &capability.Error{Code: capability.Cancelled, EffectState: capability.NotStarted, RequestID: a.Call.RequestID}
	}
	reply := Reply{Payload: strings.Repeat("x", int(max(1, r.fixture.OutputBytes)))}
	if err != nil {
		if r.broken == "unsupported side effect" {
			if _, err := r.catalog.Lookup(a.Call.Capability, 1); err != nil {
				r.execute(a.Call)
			}
		}
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
			if r.broken == "discovery leak" || (slices.Contains(r.policy.Allowlists["server_tools"], "server/"+tool+"@1") && (r.broken == "list caller ignored" || r.fixture.CallerPolicy == nil || slices.Contains(r.fixture.CallerPolicy.Allowlists["server_tools"], "server/"+tool+"@1"))) {
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
		if r.broken == "planning ignores version" {
			for index := range input.Requests {
				input.Requests[index].SchemaVersion = 1
			}
		}
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
		r.publishConfiguration(params.Config["broker_secret"])
		r.observer.Activate()
	}
	if r.broken == "activation before refusal" && failure != nil {
		r.observer.Activate()
	}
	if r.broken == "notice hidden" {
		notices = nil
	}
	if r.broken == "narrowing hidden" {
		filtered := []host.PlanNotice{}
		for _, n := range notices {
			if !n.Narrowed {
				filtered = append(filtered, n)
			}
		}
		notices = filtered
	}
	if r.broken == "wide plan marked narrowed" && len(grants) > 0 {
		notices = append(notices, host.PlanNotice{Capability: grants[0].Name, GrantID: grants[0].GrantID, Narrowed: true})
	}
	if r.broken == "planning name" && failure != nil {
		failure.Capability = "wrong"
	}
	if r.broken == "activation receipt changed" && len(grants) > 0 && len(input.Requests) == 0 {
		grants = append(capability.GrantSet{}, grants...)
		grants[0].PolicyRevision = "changed"
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
	base := host.Authority{Authenticated: true, Background: true, Actor: host.Subject{Kind: host.PluginActor, ID: r.fixture.Runtime.OwnerID}, Owner: r.fixture.Runtime, Audience: r.audience, PolicyRevision: "review", Policy: cloneScope(r.policy), LeaseContext: r.lease, Active: r.active, TargetAvailable: r.active && !r.unavailable}
	if bridge {
		base.Audience = "loopback"
	}
	if !r.active {
		return base, nil
	}
	if !bridge && req.Header.Get("X-Binding") != r.binding && !(r.broken == "plugin bearer accepted" && req.Header.Get("Authorization") != "") {
		if r.broken == "denial reservation" {
			r.inflight++
			r.observer.Reserve()
		}
		code := capability.CapabilityDenied
		if req.Header.Get("Authorization") != "" {
			code = capability.Unauthenticated
		}
		return base, &capability.Error{Code: code, EffectState: capability.NotStarted}
	}

	for _, g := range r.grants {
		if g.GrantID == c.GrantID {
			base.Grant = g
			break
		}
	}
	if base.Grant.GrantID == "" && r.broken == "ambient empty" && len(r.grants) == 0 {
		issued, expiry := fixtureTimes()
		raw, _ := json.Marshal(r.policy)
		base.Grant = capability.Grant{GrantID: c.GrantID, Name: c.Capability, SchemaVersion: 1, Scope: raw, HostInstance: r.fixture.Runtime.HostInstance, OwnerID: r.fixture.Runtime.OwnerID, OwnerGeneration: r.fixture.Runtime.OwnerGeneration, Audience: "stdio", IssuedAt: issued, ExpiresAt: expiry, PolicyRevision: "review"}
		r.bindingScopes[c.GrantID] = cloneScope(r.policy)
	}

	if r.broken == "expiry ignored" || (r.broken == "list expiry ignored" && c.Operation == "host/mcp/list_tools") || r.broken == "expire as revoke" {
		base.Grant.IssuedAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
		base.Grant.ExpiresAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	}
	if r.broken == "audience ignored" && !bridge {
		base.Audience = "stdio"
	}
	base.Owner.OwnerGeneration = r.generation
	base.Grant.Scope = append(json.RawMessage(nil), base.Grant.Scope...)
	if r.broken == "generation replay" {
		base.Owner.OwnerGeneration = 1
		base.Grant.OwnerGeneration = 1
		base.LeaseContext = context.Background()
	}
	if r.wrongOwner != "" {
		base.Grant.OwnerID = r.wrongOwner
	}

	if c.Operation == "host/mcp/call_tool" && graphCycle(r.graph, c.Server+"/"+c.Tool+"@1", map[string]bool{}, map[string]bool{}) {
		return base, &capability.Error{Code: capability.ScopeDenied, Detail: capability.CallbackCycle, EffectState: capability.NotStarted}
	}
	if c.Capability == capability.MCPReach && r.tools[c.Server+"/"+c.Tool].revision != "" && !slices.Contains(r.bindingScopes[c.GrantID].Allowlists["server_tools"], c.Server+"/"+c.Tool+"@"+r.tools[c.Server+"/"+c.Tool].revision) {
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
	if r.fixture.UnsafeInstall && r.broken == "unsafe widens keys" {
		var widened capability.Scope
		json.Unmarshal(base.Grant.Scope, &widened)
		widened.Allowlists["keys"] = append(widened.Allowlists["keys"], "other")
		base.Grant.Scope, _ = json.Marshal(widened)
		base.Policy = cloneScope(widened)
		scope = cloneScope(widened)
	}
	if r.fixture.UnsafeInstall && strings.HasPrefix(r.broken, "unsafe widen ") {
		var widened capability.Scope
		json.Unmarshal(base.Grant.Scope, &widened)
		switch strings.TrimPrefix(r.broken, "unsafe widen ") {
		case "operations":
			widened.Allowlists["operations"] = append(widened.Allowlists["operations"], "host/storage/delete")
		case "effects":
			widened.Allowlists["effects"] = append(widened.Allowlists["effects"], "destructive")
		case "budget":
			widened.Limits["request_bytes"] = 10000
		}
		base.Grant.Scope, _ = json.Marshal(widened)
		base.Policy = cloneScope(widened)
		scope = cloneScope(widened)
	}
	if strings.HasPrefix(r.broken, "scope ") && c.Capability == workflowName {
		dim := strings.TrimPrefix(r.broken, "scope ")
		var modified capability.Scope
		json.Unmarshal(base.Grant.Scope, &modified)
		if slices.Contains([]string{"provider", "run", "step", "attempt", "fork"}, dim) {
			modified.Allowlists[dim] = append(modified.Allowlists[dim], "another")
		}
		if dim == "deadline" {
			modified.Limits["deadline_ms"] = 10000
		}
		base.Grant.Scope, _ = json.Marshal(modified)
		base.Policy = cloneScope(modified)
		scope = cloneScope(modified)
	}
	base.TransportScope = &scope
	if r.fixture.Caller != nil && r.broken == "background bypass" {
		// treat delegated call as background: caller dropped, caller policy ignored
	} else if r.fixture.Caller != nil {
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
		if r.broken == "claimed caller" && a.ClaimedCaller != "" {
			base.InitiatingCaller.ID = a.ClaimedCaller
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
	if limit, ok := a.Policy.Limits["rate_per_minute"]; ok && r.rateUsed >= limit && r.broken != "rate ignored" {
		return nil, &capability.Error{Code: capability.RateLimited, EffectState: capability.NotStarted}
	}
	if limit, ok := a.Policy.Limits["concurrency"]; ok && r.inflight >= int(limit) && r.broken != "concurrency overflow" {
		if r.broken == "steal reservation" {
			r.inflight--
			r.observer.Release()
		}
		code := capability.BudgetExceeded
		if r.broken == "concurrency wrong code" {
			code = capability.Conflict
		}
		return nil, &capability.Error{Code: code, EffectState: capability.NotStarted}
	}
	r.inflight++
	r.rateUsed++
	r.observer.Reserve()
	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			if r.broken != "release missing" && !(r.broken == "steal reservation" && r.inflight == 0) {
				r.inflight--
				r.observer.Release()
			}
			r.mu.Unlock()
		})
	}
	r.releases = append(r.releases, release)
	return release, nil
}

type planningResolver struct{ instance *referenceInstance }

func (p planningResolver) Scopes(ctx context.Context, request host.Request) (host.ScopeInputs, error) {
	r := p.instance
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.broken == "planning trusts request" || r.broken == "wide plan marked narrowed" {
		return host.ScopeInputs{Supported: cloneScope(request.Scope), Approved: cloneScope(request.Scope), Policy: cloneScope(request.Scope)}, nil
	}
	return host.ScopeInputs{Supported: cloneScope(r.policy), Approved: cloneScope(r.policy), Policy: cloneScope(r.policy)}, nil
}
func (r *referenceInstance) writeReply(w http.ResponseWriter, reply Reply) {
	w.Header().Set("Content-Type", "application/json")
	if reply.Failure != nil {
		if r.broken == "oversized denial" && reply.Failure.Code == capability.BudgetExceeded {
			reply.Payload = strings.Repeat("x", 1001)
		}
		switch r.broken {
		case "wrong code":
			reply.Failure.Code = capability.Conflict
		case "wrong state":
			reply.Failure.EffectState = capability.NotCommitted
		case "retryable denial":
			reply.Failure.Retryable = true
		case "unknown state":
			if reply.Failure.Code == capability.UnknownOutcome {
				reply.Failure.EffectState = capability.Committed
			}
		}
	}
	raw, _ := json.Marshal(reply)
	r.mu.Lock()
	r.artifacts["replies"] = string(raw)
	r.artifacts["errors"] = string(raw)
	r.mu.Unlock()
	if r.broken == "error payload leak" && reply.Failure != nil {
		raw, _ := json.Marshal(reply.Failure)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		fields["stack"] = "private diagnostic"
		json.NewEncoder(w).Encode(map[string]any{"RPCCode": reply.RPCCode, "Failure": fields})
		return
	}
	if r.broken == "reply wire leak" && r.fixture.FailAfterCommit && r.fixture.Secret != "" {
		reply.Payload = "broker=" + r.fixture.Secret
	}
	json.NewEncoder(w).Encode(reply)
}

func TestReferenceAdapterConformance(t *testing.T) { Run(t, referenceAdapter{}) }
func TestBrokenAdaptersMustFail(t *testing.T) {
	cases := map[string]struct{ requirement, probe string }{
		"token only":                {"C08", "unsafe install does not widen grants and artifacts redact"},
		"secret only":               {"C08", "unsafe install does not widen grants and artifacts redact"},
		"hex leak":                  {"C08", "unsafe install does not widen grants and artifacts redact"},
		"prefix leak":               {"C08", "unsafe install does not widen grants and artifacts redact"},
		"wrong code":                {"C03", "direct absent binding"},
		"wrong state":               {"C03", "direct absent binding"},
		"retryable denial":          {"C03", "direct absent binding"},
		"unknown state":             {"C06", "unknown write outcome never retries"},
		"definite failure":          {"C06", "definite commit survives late revoke"},
		"post stop admission":       {"C06", "stop before commit"},
		"cancel credential":         {"C07", "bridge cancellation is request-local"},
		"activation before refusal": {"C01", "missing capability_contract"},
		"notice hidden":             {"C01", "unknown request optional=true"},
		"planning name":             {"C01", "unknown request optional=false"},
		"denial reservation":        {"C03", "direct absent binding"},
		"release missing":           {"C01", "unknown request optional=true"},
		"steal reservation":         {"C05", "concurrency is reserved atomically"},
		"scope provider":            {"S09", "workflow provider"},
		"scope run":                 {"S09", "workflow run"},
		"scope step":                {"S09", "workflow step"},
		"scope attempt":             {"S09", "workflow attempt"},
		"scope fork":                {"S09", "workflow fork"},
		"scope effect":              {"S09", "workflow effect"},
		"scope deadline":            {"S09", "workflow deadline"},
		"scope budget":              {"S09", "workflow budget"},
		"scope expired":             {"S09", "workflow expired"},
		"scope revoked":             {"S09", "workflow revoked"},

		"claimed caller":             {"C04", "delegated identity reaches audit"},
		"list without admission":     {"C05", "discovery after stop"},
		"unsafe widens keys":         {"C08", "unsafe dimension keys"},
		"planning ignores version":   {"C02", "planning wrong version"},
		"planning trusts request":    {"C02", "planning wider than policy"},
		"bridge cancel":              {"C07", "bridge cancellation is request-local"},
		"publish reinterpreted":      {"C10", capability.StorageWrite + " scoped and direct bypass"},
		"encoded leak":               {"C08", "unsafe install does not widen grants and artifacts redact"},
		"expire as revoke":           {"C03", "expiry distinct from revocation"},
		"expiry ignored":             {"C03", "expiry distinct from revocation"},
		"revoked ignored":            {"C03", "revocation distinct from expiry"},
		"audience ignored":           {"C03", "direct audience"},
		"ambient empty":              {"C01", "empty grants deny"},
		"background bypass":          {"C04", "forged caller cannot replace verified caller"},
		"unsafe widen operations":    {"C08", "unsafe dimension operations"},
		"unsafe widen effects":       {"C08", "unsafe dimension effects"},
		"unsafe widen budget":        {"C08", "unsafe limit request_bytes"},
		"redaction unexercised":      {"C08", "secret diagnostic output"},
		"unavailable as stop":        {"C07", "bridge unavailable"},
		"list caller ignored":        {"C05", "discovery intersects caller permission"},
		"narrowing hidden":           {"C02", "planning wider than policy"},
		"wide plan marked narrowed":  {"C02", "planning wider than policy"},
		"plugin bearer accepted":     {"C04", "plugin cannot substitute proxy credential"},
		"oversized denial":           {"C07", "bridge output"},
		"measured output":            {"C07", "bridge output"},
		"unsupported accepted":       {"C10", capability.StorageRead + " declared unsupported refuses"},
		"output after write":         {"C07", "bridge output"},
		"reply wire leak":            {"C08", "secret diagnostic output"},
		"concurrency wrong code":     {"C05", "concurrency is reserved atomically"},
		"withdrawal executes":        {"C06", "stop before commit"},
		"suffix leak":                {"C08", "unsafe install does not widen grants and artifacts redact"},
		"base64 only":                {"C08", "unsafe install does not widen grants and artifacts redact"},
		"cross actor cancel":         {"C07", "cancel_call cannot cross actors"},
		"cancel all":                 {"C07", "cancel_call leaves concurrent request running"},
		"owner ignored":              {"C03", "direct owner"},
		"reconnect broken":           {"C04", "replayed generation binding is fenced"},
		"list expiry ignored":        {"C05", "discovery after expired"},
		"redirect hides execution":   {"C07", "bridge redirect"},
		"redirect wrong status":      {"C07", "bridge redirect"},
		"redirect unobserved":        {"C07", "bridge redirect"},
		"unsupported side effect":    {"C10", capability.StorageRead + " declared unsupported refuses"},
		"follow redirects":           {"C07", "bridge redirect"},
		"redirect wrong refusal":     {"C07", "bridge redirect"},
		"redirect executes":          {"C07", "bridge redirect"},
		"activation receipt changed": {"C03", "direct absent binding"},
		"cancel ignored":             {"C07", "cancel_call leaves concurrent request running"},

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

func graphCycle(graph map[string][]string, node string, path, seen map[string]bool) bool {
	if path[node] {
		return true
	}
	if seen[node] {
		return false
	}
	path[node] = true
	for _, next := range graph[node] {
		if graphCycle(graph, next, path, seen) {
			return true
		}
	}
	delete(path, node)
	seen[node] = true
	return false
}

var sensitiveField = regexp.MustCompile(`(?i)(broker|credential|authorization|token)=([^\s,;]+)`)

func (r *referenceInstance) adaptOutput(surface, value string) string {
	if r.broken != "redaction unexercised" {
		r.observer.SecretInput(surface, value)
	}
	return sensitiveField.ReplaceAllString(value, "$1=[redacted]")
}

func TestDeclaredSubsetWithoutStorageWrite(t *testing.T) {
	f, _ := basic(capability.ReadonlyQuery)
	report := Evaluate(context.Background(), rejectStorageAdapter{}, Profile{Supported: f.Catalog})
	for _, v := range report.Violations {
		t.Errorf("%s/%s: %s", v.Requirement, v.Probe, v.Reason)
	}
	found := false
	for _, d := range report.Descriptors {
		if d.Name == capability.StorageWrite && d.Status == DeclaredUnsupported {
			found = true
		}
	}
	if !found {
		t.Fatal("missing visible unsupported storage descriptor")
	}
	for _, r := range report.Requirements {
		if r.ID == "C09" && r.Status != HostOwned {
			t.Fatal("workflow subsystem coverage overclaimed")
		}
	}
}

type rejectStorageAdapter struct{}

func (rejectStorageAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	for _, d := range f.Catalog {
		if d.Name == capability.StorageWrite {
			return nil, errors.New("unsupported storage")
		}
	}
	return referenceAdapter{}.Open(ctx, f, o)
}

func TestWatchdogAndWorkerPanic(t *testing.T) {
	t.Run("worker panic", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		report := Evaluate(context.Background(), boundedAdapter{"worker panic", release}, Profile{Timeout: 5 * time.Second})
		found := false
		for _, v := range report.Violations {
			if v.Reason == "adapter panicked" {
				found = true
			}
		}
		if !found {
			t.Fatal("worker panic not contained and reported")
		}
	})
	t.Run("hung callback", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan Report, 1)
		go func() { done <- Evaluate(ctx, blockedOpenAdapter{entered, release, returned}, Profile{}) }()
		<-entered
		cancel() // Deterministic watchdog trigger, independent of machine speed.
		report := <-done
		select {
		case <-returned:
			t.Fatal("fixture was not actually blocked")
		default:
		}
		close(release)
		<-returned
		requireViolation(t, report, "C01", "missing capability_contract")
		requireStatus(t, report, "C01", Failed)
		for _, id := range []string{"C06", "C07", "C08", "C10"} {
			requireStatus(t, report, id, NotRun)
			for _, q := range report.Requirements {
				if q.ID == id && q.Reason == "" {
					t.Fatal("truncation reason omitted")
				}
			}
		}
		if !strings.Contains(report.String(), "probe deadline exceeded") {
			t.Fatal("watchdog reason lost")
		}
	})
}

type blockedOpenAdapter struct{ entered, release, returned chan struct{} }

func (a blockedOpenAdapter) Open(context.Context, Fixture, *Observer) (Instance, error) {
	close(a.entered)
	<-a.release
	close(a.returned)
	return nil, errors.New("private hung callback")
}

type boundedAdapter struct {
	mode    string
	release <-chan struct{}
}

func (a boundedAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	i, e := referenceAdapter{}.Open(ctx, f, o)
	if e != nil {
		return i, e
	}
	return boundedInstance{i, a.mode, a.release, f.Gate != nil}, nil
}

type boundedInstance struct {
	Instance
	mode    string
	release <-chan struct{}
	gate    bool
}

func (i boundedInstance) Invoke(ctx context.Context, c Attempt) (Reply, error) {
	if i.mode == "worker panic" && i.gate {
		panic("private diagnostic")
	}
	if i.mode == "hung invoke" && c.Call.Target == "other" {
		<-i.release
		return Reply{}, errors.New("private error")
	}
	return i.Instance.Invoke(ctx, c)
}
func TestAdapterErrorTextAndInvalidEnumsArePrivate(t *testing.T) {
	for _, e := range []error{errors.New("private bearer diagnostic"), &observedFailure{capability.Code("private-secret-token"), capability.EffectState("private-secret-token")}} {
		if strings.Contains(safeReason(e), "private") {
			t.Fatal("diagnostic leaked")
		}
	}
	failures := Check(context.Background(), referenceAdapter{"error leak"})
	if len(failures) == 0 {
		t.Fatal("adapter error accepted")
	}
	for _, f := range failures {
		if strings.Contains(f.Reason, "bearer") {
			t.Fatal("adapter text leaked")
		}
	}
}
func TestParsedFailureMustMatchReceivedData(t *testing.T) {
	failures := Check(context.Background(), mismatchedDataAdapter{})
	found := false
	for _, f := range failures {
		if f.Requirement == "C03" && f.Probe == "direct absent binding" {
			found = true
		}
	}
	if !found {
		t.Fatal("adapter mismatch passed C03")
	}
}

type mismatchedDataAdapter struct{}

func (mismatchedDataAdapter) Open(ctx context.Context, f Fixture, o *Observer) (Instance, error) {
	i, e := referenceAdapter{}.Open(ctx, f, o)
	if e != nil {
		return i, e
	}
	return mismatchedDataInstance{i}, nil
}

type mismatchedDataInstance struct{ Instance }

func (i mismatchedDataInstance) Invoke(ctx context.Context, c Attempt) (Reply, error) {
	r, e := i.Instance.Invoke(ctx, c)
	if r.Failure != nil {
		changed := *r.Failure
		changed.Code = capability.Conflict
		r.Data, _ = json.Marshal(changed)
	}
	return r, e
}
func TestEmptySubsetDoesNotWaiveHandshake(t *testing.T) {
	r := Evaluate(context.Background(), referenceAdapter{"missing contract"}, Profile{Supported: []capability.Descriptor{}})
	if len(r.Violations) == 0 {
		t.Fatal("empty supported set waived handshake")
	}
}

func TestActualOutputMayBeBoundedAfterRead(t *testing.T) {
	for _, v := range Check(context.Background(), referenceAdapter{"late read limit"}) {
		t.Errorf("%s/%s: %s", v.Requirement, v.Probe, v.Reason)
	}
}

func (r *referenceInstance) publishConfiguration(secret string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var logs bytes.Buffer
	fmt.Fprintln(&logs, r.adaptOutput("logs", "credential="+r.token), r.adaptOutput("logs", "broker="+secret))
	if r.broken == "token only" {
		fmt.Fprintln(&logs, r.token)
	}
	if r.broken == "secret only" {
		fmt.Fprintln(&logs, secret)
	}
	if r.broken == "hex leak" {
		fmt.Fprintln(&logs, fmt.Sprintf("%x", []byte(secret)))
	}
	if r.broken == "prefix leak" && len(secret) > 16 {
		fmt.Fprintln(&logs, secret[:16])
	}
	if r.broken == "suffix leak" && len(secret) > 8 {
		fmt.Fprintln(&logs, r.token[8:], secret[4:])
	}
	if r.broken == "base64 only" {
		fmt.Fprintln(&logs, base64.StdEncoding.EncodeToString([]byte(secret)))
	}
	if r.broken == "secret leak" {
		fmt.Fprintln(&logs, r.token, secret)
	}
	if r.broken == "encoded leak" {
		fmt.Fprintln(&logs, "auth", base64.StdEncoding.EncodeToString([]byte("Bearer "+r.token)), "cfg", base64.StdEncoding.EncodeToString([]byte(secret)), "prefix", r.token[:min(24, len(r.token))])
	}
	registry, _ := json.Marshal(map[string]any{"credential": r.adaptOutput("registry", "credential="+r.token), "capability": "fixture", "config": r.adaptOutput("registry", "broker="+secret)})
	browser, _ := json.Marshal(map[string]any{"credential": r.adaptOutput("browser", "credential="+r.token), "ui": "fixture", "config": r.adaptOutput("browser", "broker="+secret)})
	r.artifacts = map[string]string{"logs": logs.String(), "registry": string(registry), "browser": string(browser)}
}
