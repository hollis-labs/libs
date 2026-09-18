# Security model

What this SDK guarantees, what it deliberately leaves to the host, and where
the seams are. Read this before writing a plugin that handles anything you
would mind leaking.

The short version: **the SDK secures the plumbing, not the policy.** It gives
you a wire protocol, a credential channel that does not travel in the
environment, a logger that redacts what you tell it is secret, and panic
isolation. It does not authenticate the host, sandbox your plugin, bound your
resource use, or decide what you are allowed to do. Those belong to the host,
and they differ between hosts.

## The trust boundary

```
        host process                          plugin process
   ┌──────────────────────┐            ┌────────────────────────────┐
   │  launches, supervises│  stdin  →  │  subprocess.Serve          │
   │  resolves secrets    │  ← stdout  │    your Plugin impl        │
   │  owns policy         │  ← stderr  │    (JSON-lines log)        │
   └──────────────────────┘            └────────────────────────────┘
        ▲                                        │
        │  the host decides what you may do      │  you decide what you return
```

Two things follow from the direction of the arrows, and most mistakes come from
forgetting one of them.

**The host trusts your output.** Whatever you return from a tool call, a CRUD
read or a command goes wherever the host puts it — a terminal, a log file, an
HTTP response, an AI model's context window. There is no filter between you and
that destination unless the host built one. **What you return is published.**

**You should not trust the host's environment to protect you.** Your process
inherits whatever the host chose to pass. You do not control it, you cannot
audit it from inside, and it is not a capability grant you asked for. Treat
anything ambient as something you happened to be handed rather than something
you are entitled to use.

## What the SDK guarantees

**A panic will not take down the host.** `Serve` recovers panics per request
and returns a JSON-RPC error instead (`TestServe_PanicRecovery`). Removing that
recovery turns a plugin bug into a host crash, which is why it is pinned by a
test.

**Credentials do not travel in the environment.** A plugin declares what it
needs and the host passes resolved values in `InitParams.Config`. The
environment is ambient: anything placed there reaches every plugin, not the one
that asked. This is the single most important design decision in the contract
and it is worth understanding rather than working around.

**The logger redacts values you identify as secrets.** `ConfigReader.Secret(key)`
registers the value with a secret tracker, and the stderr logger replaces it
wherever it appears in a log field afterwards. This only works for values that
went through `Secret()` — see the seams below.

**The wire protocol is versioned and pinned.** `subprocess.ProtocolVersion` is
1, pinned by `TestProtocolVersionLockedAt1`. Hosts and plugins are separately
released binaries, so every message type has a roundtrip test and older plugins
are expected to survive newer init params.

**Requests are isolated from each other.** Each incoming request is dispatched
in its own goroutine.

## What the SDK does not do

None of these are oversights. Each is a host concern, and a host-neutral
contract that decided them would impose one host's answer on every other.

| Not provided | Whose job | What it means for you |
|---|---|---|
| Host authentication | host | You cannot verify who launched you. Anything on the other end of stdin is "the host". |
| Sandboxing / isolation | host | Your process has whatever access the OS gives it. Assume none is revoked. |
| Resource limits, per-call deadlines | host | A host may or may not bound your call. Bound your own work. |
| Authorization — what you may do | host | The host decides whether an operation runs. Do not implement your own parallel policy. |
| Output filtering | host | See "The host trusts your output". |
| Secret storage or rotation | host | You never reach a credential store. You receive values. |
| Registration vocabulary | host | Commands, events, resources and UI contributions are declared in the host's own manifest schema, not here. |

## Seams — where the guarantees stop

These are the places where something that looks protected is not. Each has
produced a real bug.

**Redaction only covers values you routed through `Secret()`.** A credential you
read from your own environment variable, or parsed out of a config file
yourself, is unknown to the tracker and will log in clear. If you must accept a
credential from somewhere the host did not give it to you, register it
deliberately.

**Redaction covers log *fields*, not arbitrary strings you compose.** Building
an error message by concatenating a token into a sentence produces a string the
tracker can still catch if the exact value appears — but a *transformed* value
(URL-encoded, truncated, base64'd) will not match. Do not transform a secret and
then log the result.

**A host's redaction runs over your error text, and it cannot read.** Hosts
commonly apply regex redaction to plugin error output on the way to an operator.
That means an error message of yours can arrive mangled. Two known shapes to
avoid:

- The word `Bearer` followed by a word: the word after it may be rewritten,
  including when it was guidance rather than a token.
- `NAME=value` and `--flag value`: the value may be swallowed as a credential
  assignment, including when it was the recovery instruction the operator
  needed.

If your error carries a recovery instruction, phrase it so nothing in it looks
like a credential — and add a test that the instruction survives.

**`Init` and `Load` are not guaranteed to be serialized with your other
calls.** `Serve` dispatches each request in its own goroutine. A well-behaved
host sends `Init`, waits, sends `Load`, waits, and only then calls a tool — but
your plugin should not *depend* on that, and a test driver that pipes all three
at once will race the handshake and get an empty answer from a plugin that is
working correctly. If your state depends on init config, guard it.

**`DataDir` may be empty.** `InitParams.ResolvedDataDir()` returns
`ErrNoDataDir` if the host did not populate it. The SDK deliberately provides no
fallback for data, because writing a plugin's persistent state to a guessed
location is worse than failing. `ResolvedCacheDir()` does fall back to
`os.TempDir()`, because losing a cache is survivable.

**A missing credential should not be fatal.** The host may legitimately load you
without a secret — because it is not configured yet, or because the operator is
diagnosing why it is not. Fail the *operation* that needed it, naming the
recovery, and keep whatever diagnostics work without it working. An unauthenticated
health check that still reports reachability is often the single most useful
thing a plugin offers.

## Writing a plugin that handles credentials

1. **Declare, do not resolve.** Name what you need in the host's manifest and
   read it from `InitParams.Config`. Never reach for a credential store.
2. **Read secrets through `ConfigReader.Secret`**, not `params.Config[...]`
   directly, so redaction tracking is armed.
3. **Never return a vendor SDK type.** Map it onto your own type. Vendor
   response structs routinely carry credential fields you did not ask for, and
   a field the vendor adds in a minor release becomes part of your output
   without anyone deciding it should. Map explicitly, allow-list the fields, and
   test that a populated credential field does not appear in the serialized
   result.
4. **Emit names, never values.** An operator needs to know *that* a credential
   is configured, and which one. They rarely need the value, and the places
   plugin output ends up — logs, transcripts, model context — are not places a
   value should be.
5. **Do not log a credential even at debug level.** Host log capture does not
   have a debug-only destination.

## Reporting a vulnerability

Open an issue for anything non-exploitable. For something that should not be
public first, describe the class of problem rather than a working exploit.
