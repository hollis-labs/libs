# Stdio process shim

`shim.Start` starts one authorized executable, journals raw stdout/stderr and
mechanical control effects, and exposes a private Unix control socket. Closing
or replacing a client connection leaves the child running. The separate
`cmd/cairn-shim` executable runs the host until SIGTERM or SIGINT, and keeps the
socket available after the child exits so a controller can inspect its journal.
A service manager must place it outside the controller's teardown boundary.
This package does not install services or change account settings.

## Launch

The binary takes `--launch <absolute-path>` to a 0600 regular JSON file. The
library accepts the same fields in `Launch`. A host resolves and authorizes
these inputs before calling the shim:

- `session`, `instance`, `actor` (mesh actor), `subject`, and `generation`
  (nonzero decimal string);
- tokenized `argv` with an absolute executable path, absolute `cwd`, and `env`
  as the complete child environment (an empty list means empty environment);
- separate absolute `control_dir` and `journal_dir`, private 0700 directories;
- `secret`, a freshly minted capability with at least 32 bytes of entropy,
  never included in argv or automatically inherited into child environment;
- absolute `pin_path` to the launcher's existing 0600 regular pin file,
  `pin_key`, `boot_generation`, and `reservation`;
- optional `journal_bytes` (default 16 MiB, minimum 2 MiB), `wall_time_ns`
  (zero means unlimited), `stop_grace_ns` (default 2 seconds), `heartbeat_ns`
  (default 10 seconds), and `client_queue` (default 64, maximum 1024).

Use short control paths: `control_dir/control.sock` must be under 104 bytes.
No ambient home lookup, environment merge or credential provisioning happens
here. The launcher holds a shared lock on the stable pin inode before launch.
The shim opens that same existing file, obtains its own shared lock before
spawn, and exposes `pin_adopted`, key, boot generation and reservation in hello.
Only after verifying that receipt may the launcher release its shared hold.
The shim closes its noninherited pin descriptor on child exit. Mutators use a
separate ordered mutation lock and an exclusive nonblocking pin probe. Never
replace or unlink the pin inode while its identity is active. Kernel lock
release alone is not proof that all descendants exited.

The generic child boundary is a POSIX process group. Exit and kill clean up
that group; a child that deliberately escapes it requires an external host
sandbox/service-manager boundary. Required aggregate CPU/memory/task limits
and mandatory child isolation are unsupported in this cut. Same-UID directory
permissions do not isolate a malicious unsandboxed child: the host must hide
the control directory, journal and launch descriptor from agent-writable roots.
Linux checks peer UID in addition to the capability proof. The Darwin build
currently uses the capability proof without an additional peer-UID check.

A journal owner lock prevents concurrent hosts. Any prior durable launch intent
refuses a second spawn with `outcome_unknown`; restarting the executable never
silently restarts the runtime. Use `OpenJournal` for offline inspection after
the original host has stopped. Native resume/recovery is a host decision.

## Wire protocol

Each frame is a 4-byte big-endian length followed by JSON, bounded to 1 MiB.
The common fields are `protocol_major`, `protocol_minor`, `type`, `request_id`,
`reply_to`, `session`, `controller_epoch`, and `body`. Epoch/generation values
in control messages are decimal strings. Envelopes retain the shared
`mesh.Event` JSON schema; consumers must preserve its uint64 values exactly.
Unknown optional fields are ignored; malformed lengths/JSON disconnect only
that client. Request IDs correlate `result` and `error` replies.

The server first sends `hello` with a fresh nonce and current controller epoch.
The client responds with protocol major/minor, role (`controller` or
`observer`), instance, generation and `Proof(secret, nonce, session, role)`.
An optional journal ID pins a reconnect to the same log. Major 1/minor 0 is the
baseline; a higher client minor negotiates down to zero. Bad identity or proof
is refused. `auth` and attached login return `attached_mode_not_supported`.

One controller and one read-only observer may be active. A reconnect increments
the durable controller epoch. Replacing a live controller requires explicit
`takeover: true` and its current epoch; the old connection is closed. Commands
carry the current epoch and target generation. Takeover serializes with
intent/effect/outcome execution. Observers can replay and request health, and
cannot inject, control or acknowledge. Pending connections and per-client queues
are bounded; a slow client disconnects without blocking journal appends.

`Client` provides the handshake, serialized writes, heartbeat responses and a
bounded `Frames` stream. After reconnect, the controller does the following:

1. `Replay(session, afterCursor)` using its locally committed source cursor
   (empty means the beginning).
2. Receive the snapshot high-water result, then full `event` frames with
   `{event: mesh.Event, replay: bool}` through that snapshot and the live tail.
3. Persist each envelope transactionally, de-duplicate by event ID, and then
   `Ack(session, cursor)` for the highest contiguous locally persisted cursor.

Replay installs its tail subscription under the append lock at the same instant
as the snapshot, so concurrent output cannot fall between the two. Cursors are
journal-ID-qualified decimal positions. Foreign/ahead cursors and acknowledgments
outside delivered history return `cursor_invalid`; the controller asserts its
own local durability and contiguity. v0 does not evict history, so there is no
expired-history case. A new downstream event store assigns its own cursor and
retains the source cursor as provenance rather than comparing unrelated stores.

`health` pings occur every 10 seconds by default. Three missed intervals detach
the connection; they never kill a healthy runtime. Reconnect to the same socket
with bounded backoff and replay from the last locally committed cursor. The
client helper does not silently reconnect or resend effects for its caller.

## Mechanical effects

`inject` accepts the `Inject` body: idempotency key, actor, delegated actor URNs,
subject, mode, delivery, expected generation, optional deadline, and either
base64 `data` or `signal`. Input is at most 64 KiB and `input/immediate` writes
those exact bytes, with a bounded 2-second pipe-write deadline. A partial or
uncertain write returns `outcome_unknown` and the actual byte count. Supported
signals are SIGINT, SIGTERM, SIGHUP and SIGKILL. A signal targets the process
group and returns `signal_sent`, not proof of runtime consumption.

`turn` returns `unsupported_mode`. Default delivery is `at_idle`, which returns
`unsupported_delivery`, as do `interrupt` and all other non-immediate delivery
requests. The raw driver cannot establish a trustworthy idle or turn-cancel
boundary. Invalid, unsupported, stale and expired requests are attributable
journal evidence. The controller supplies already-authorized actor claims;
this package validates their shape, not mesh grants.

An accepted effect has fsynced intent before it touches the child, then a
fsynced outcome. Exact retries return the same receipt; a changed fingerprint
with the same key returns `idempotency_conflict`. An uncertain effect is never
implicitly retried. `control` supports `kill` and `signal` with expected
generation; kill sends SIGTERM then SIGKILL after the launch grace. Pause,
resume, resize and dynamic resource changes return `unsupported_control`.

## Durability and limits

Journal segments have a `SHIMLOG1` header and ordered segment filenames; each
record is `length | full mesh.Event JSON | CRC32`. One lock serializes append
and fsync. Observations become streamable only after fsync. Recovery validates
envelopes, cursor order and checksums. An incomplete final fragment is fsynced
into a quarantine file before truncation and a new segment; complete checksum
failures or incomplete non-final segments are `journal_corrupt`.

Raw output is base64 in private payloads, in chunks of at most 64 KiB. No launch
environment or capability material is logged by the host. Process output can
contain sensitive data; downstream public views require their own redaction.
Hardware/shim failure can lose uncommitted bytes; the API does not promise
exactly-once capture across those failures.

All history, including acknowledged receipts, is retained within the launch
cap. No automatic compaction or receipt eviction occurs. The resident replay
index and deduplication map are bounded by that cap as well. Quarantined tail
evidence counts toward it. Ordinary appends reserve 16 KiB for terminal/gap
records; exhaustion stops effects and terminates the child, with a truncated
gap event where storage still works. I/O failure stops the child even if the
failed disk cannot persist its final diagnostic. Offline cleanup/archival is
explicit host work after proving the process no longer uses the generation.

Tests run a marked fake child in the Go test executable, with a private test
home. They cover binary output across detach/replay, concurrent tail ordering,
intent/fsync failures, corruption/torn recovery, deduplication and uncertain
writes, heartbeat detach, pin overlap, stale fences and process-group shutdown.
No real model executable or OS service is launched by the suite.
