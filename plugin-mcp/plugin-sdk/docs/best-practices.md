# Best practices

Patterns that hold up, drawn from plugins built against this SDK. Each one here
cost someone real time to learn; none of them is style preference.

Examples reference two open-source hosts and their plugins:

- [`hollis-labs/cerberus`](https://github.com/hollis-labs/cerberus) — an
  infrastructure control plane. Its plugins are *connectors*: a plugin
  implements operations against an external system, and the host turns each one
  into a CLI command, an API operation and an MCP tool.
- [`hollis-labs/cerberus-plugins`](https://github.com/hollis-labs/cerberus-plugins)
  — three worked examples (`contextforge`, `azure`, `kubernetes`).
- [`hollis-labs/nanite`](https://github.com/hollis-labs/nanite) — a CLI agent
  framework. Its plugins are *feature contributors*: commands, events, CRUD
  resources, HTTP routes and UI.

The two hosts use almost disjoint slices of this SDK, which is a useful thing
to know before assuming a pattern from one applies to the other.

## Structure

### Put the vendor SDK behind an interface

Define a `Backend` interface in your plugin and keep the vendor client behind
it. Everything above that interface speaks in your own types.

```go
type Backend interface {
    ListGateways(ctx context.Context) ([]Gateway, error)
    GetHealth(ctx context.Context) (Health, error)
}
```

Three things this buys, in order of how much you will care:

- **Tests that need no network.** A fake backend exercises every code path,
  including the failure branches a live service will not produce on demand.
- **A swappable dependency.** Most provider SDKs are `v0.x`. When the vendor
  reshapes an API, one file changes.
- **A checkable boundary.** If no vendor type appears in the interface, the
  mapping layer is doing its job, and that is verifiable by reading one file.

### Generate your manifest from your code

Declare operations once, in Go, and emit the host's manifest from that
declaration at build time rather than maintaining YAML by hand. The manifest a
host installs then cannot drift from what the plugin actually serves.

Cerberus's plugins do this with a second mode on the same binary — the plugin
writes its own installable directory:

```go
func main() {
    if len(os.Args) > 2 && os.Args[1] == "write-dist" {
        if err := myplugin.WriteDist(os.Args[2]); err != nil { /* ... */ }
        return
    }
    if err := subprocess.Serve(myplugin.New()); err != nil {
        os.Exit(1)
    }
}
```

## Output

### Never return a vendor SDK type

This is the one that bites hardest, because the natural implementation is the
broken one.

A vendor's response struct carries whatever the vendor decided to put there —
routinely including credentials. Returning it publishes those to wherever the
host sends plugin output: a terminal, a log file, an HTTP response, an AI
model's context window.

Two real cases:

- ContextForge's `Gateway` type carries `AuthToken`, `AuthPassword`,
  `AuthHeaderValue`, `AuthValue`, `AuthUsername`, `AuthHeaders`,
  `AuthQueryParamValue` and `OAuthConfig` — live credentials for every upstream
  server behind the gateway. A `list_gateways` that returned the vendor struct
  would emit all of them.
- Kubernetes' `corev1.Pod` carries `Spec.Containers[].Env`, which in practice is
  where application credentials live, plus a
  `kubectl.kubernetes.io/last-applied-configuration` annotation that is often a
  verbatim copy of the original manifest — on an object whose live spec looks
  clean.

Map onto your own type and treat it as an **allow-list**: a field the vendor
adds in a minor release is not emitted unless someone adds it on purpose.

```go
// Names, never values. An operator can see that DB_PASSWORD is set
// without the value crossing into a log or a model's context.
type Container struct {
    Name     string   `json:"name"`
    Image    string   `json:"image,omitempty"`
    EnvNames []string `json:"env_names,omitempty"`
}
```

For credentials specifically, expose the *shape* — `auth_type`, and whether a
credential is configured — never a value.

**Test it.** Populate every credential-shaped field on the vendor type with one
sentinel, marshal your type, and assert the sentinel appears nowhere:

```go
func TestDTOEmitsNamesButNeverValues(t *testing.T) {
    dto := mapPod(podWithSecretEnv(sentinel))
    encoded, _ := json.Marshal(dto)
    if strings.Contains(string(encoded), sentinel) {
        t.Fatalf("DTO leaked a credential value: %s", encoded)
    }
    // ...and assert the NAMES survived, or the type is safe but useless.
}
```

Assert both halves. A mapper that drops everything passes the first check and is
worthless.

### Bound every list you return

If the host turns your operations into agent tools, an unbounded list is an
unbounded amount of text in a model's context window. Worse, silent truncation
is indistinguishable from a short list — an agent cannot tell "three results"
from "the first three of nine hundred" and will reason confidently from the
wrong one.

Return a bounded page and say when it was cut:

```go
type List[T any] struct {
    Items     []T    `json:"items"`
    Count     int    `json:"count"`
    Truncated bool   `json:"truncated,omitempty"`
    Continue  string `json:"continue,omitempty"`
}
```

Marshal an empty list as `[]`, not `null` — a tool result of `null` reads as an
error or a missing field rather than "nothing here".

### Name the thing that actually failed

A refused connection on a local tunnel port means the tunnel is down, not the
remote service. Telling an operator "the gateway is unreachable" sends them to
the wrong machine. Distinguish, in the message, between the transport, the
credential and the service.

```
cannot resolve the API server host in https://k8s.example.com:6443;
if the cluster is only reachable on the VPN, check the VPN before the cluster
```

## Credentials

### Declare, do not resolve

Name what you need in the host's manifest and read the resolved value from init
config. Never reach for a credential store yourself — you will get the wrong
one, you will get it at the wrong time, and you will bypass the host's audit of
who asked for what.

```go
func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
    // ConfigReader rather than the raw map: Secret() registers the value with
    // the logger's redaction tracker, so a later log line writes REDACTED.
    p.config = subprocess.NewConfigReader(params.Config)
    // ...
}
```

### A missing credential is not a failed load

Load successfully, and fail the *operation* that needed the credential with a
message that names the recovery. The host may be loading you precisely because
an operator is diagnosing why authentication is not working.

Keep the operations that work without a credential working. ContextForge's
`get_health` hits an unauthenticated endpoint, so it still answers when
`list_gateways` returns 401 — and that difference is exactly how an operator
tells a down tunnel from a down gateway.

### Resolve external binaries per call, not at startup

If you shell out — to a cloud CLI, a credential helper, a container runtime —
resolve the binary **on each call**, not once at load.

A host daemon frequently runs under a minimal `PATH` that a login shell does
not. Resolving once at boot, failing, and caching that failure produces a plugin
that reports healthy while every operation fails, for the lifetime of the
daemon. This has happened more than once.

Search wider than `PATH`, and when the binary is missing say so by name rather
than surfacing whatever authentication error came out the far end.

## Arguments and the wire

### Accept every typing a host might send

Arguments do not arrive with one consistent typing. Over an MCP tool call they
are decoded JSON, so a number is a `float64` and a boolean is a `bool`. Over a
CLI that passes `--arg key=value`, **everything is a string**.

A parser handling only the first shape fails silently: a limit falls back to its
default, a boolean flag reads as false, and the caller gets a confidently wrong
answer with no error. Accept both, and **reject what you cannot parse** rather
than defaulting:

```go
func (a *argMap) intOr(key string, fallback int) int {
    switch v := a.raw[key].(type) {
    case nil:
        return fallback
    case float64: // JSON numbers
        return int(v)
    case string: // CLI --arg
        if strings.TrimSpace(v) == "" { return fallback }
        parsed, err := strconv.Atoi(strings.TrimSpace(v))
        if err != nil {
            a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number, got %q", key, v))
            return fallback
        }
        return parsed
    }
    // ...
}
```

Report every argument problem at once, so a caller fixes one call rather than
discovering the next fault on the next attempt.

### Do not depend on request ordering

`Serve` dispatches each request in its own goroutine. A well-behaved host sends
`Init`, waits for the response, sends `Load`, waits, then calls a tool — but
your plugin should not assume it. Guard any state that depends on init config.

This also matters for *test drivers*: one that writes init, load and a tool call
into stdin without waiting for each response will race the handshake and get an
empty answer from a plugin that is working perfectly.

### Bound your own work

A host may not impose a deadline on your call. If it does not, a hang in your
plugin is a hang in the host's request. Set your own timeouts on anything that
touches a network or a subprocess.

## Testing

**Use the in-process harness.** `subprocess/subprocesstest` drives a plugin
without spawning one, which makes most of your tests ordinary Go tests.

**Turn on JSON roundtripping in CI:**

```bash
PLUGIN_SDK_JSON_ROUNDTRIP=1 go test ./...
```

Every request and response is marshaled and unmarshaled before the plugin sees
it, which catches wire-format bugs that in-process calls hide.

**Test against a real instance of the thing you wrap, at least once.** A fake
backend cannot tell you that the vendor's pagination behaves differently from
its documentation, or that a real object has a field shape your fixture does
not. The Kubernetes plugin was fully green against a fake clientset while two
real defects sat in it — an unbounded list and a dropped argument — both found
within minutes of pointing it at a throwaway `kind` cluster.

**Assert your read-only operations stay read-only.** If your plugin is scoped to
reads, make that a test rather than a convention, so the first write added fails
the suite instead of arriving unannounced.

## Honesty in the manifest

Mark destructive operations destructive. Do not mark reads destructive to be
safe — a host that prompts for everything trains an operator to approve
everything, and the gate stops meaning anything.

If an operation returns free text you did not compose — logs, command output,
arbitrary API responses — say so in its description. It may carry credentials or
personal data that no mapping layer of yours can filter, and a reader routing
that into an AI model's context should be able to know that in advance.
