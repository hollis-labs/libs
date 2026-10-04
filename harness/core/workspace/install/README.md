# Installed artifacts

Use `workspace.Spec.Installed` with operation `workspace.Install`, an explicit existing operator root, a separate private control root, and exact `install.Grant` path/key authorities. The host supplies canonical root and file identities, a fence, filesystem case observations, complete target/control locks, and current authority validation. There is no ambient home resolution or artificial boot root.

`workspace.Plan` freezes those inputs. `workspace.Materialize` uses the concrete materialize engine as its only filesystem writer and the existing `Ports.ReceiptStore` as its only durable evidence sink. Installed calls additionally require `ControlledReceiptStore.ControlRoot()` to match the explicit external control root. The host must validate that store's custody, durability and exact semantic grants; a configured root or receipt alone grants no authority.

Every selected document and grant preflights before any receipt or artifact mutation. Aggregate intent precedes staging and parent creation; file intent precedes rename. Root, stage, bytes, directory observations, cancellation and authority are checked after callbacks. Frozen and fresh observation windows must both remain valid; a newer observation cannot renew an expired plan. Interrupted writes or committed-record failures return `Partial`, retained metadata and recovery obligations. The engine removes only its positively identified empty temporary stage, never user directories or uncertain content. This is per-file publication, not an atomic bundle or rollback.

Claude and Codex installed rows are supported. First creation writes only absent granted leaves. Refresh requires an original trusted committed receipt and unchanged owned state. Operator-owned keys survive. A read-only check uses the detached installed action request with `materialize.Engine.Plan`; it records or mutates nothing and issues no authority or readiness proof. Installed artifact completion remains `Partial` and never supplies `LaunchReady()`.

| Boundary | Installed behavior |
| --- | --- |
| Existing root and provider directories | Preserve modes and content; no ownership, chmod, chown or removal |
| Missing parents of granted files | Engine creation with exact operation-created identities and phases |
| Existing unreadable or ambiguous document | Refuse before mutation; present zero-byte JSON is not absence |
| Existing owned value changed by another writer | Conflict; no adoption or overwrite |
| Unknown JSON members | Preserve through the existing key merge |
| TOML refresh | Preserve semantic values; encoder may change comments and layout |
| Absent fully granted native document | Preserve renderer bytes, including its banner |
| Structural conflict at a granted nested leaf | Refuse when replacement would discard the operator's ancestor value |
| Credentials, repository or trust side effects | Unsupported in the installed operation |
| OpenCode and Antigravity installed apply | Unsupported; pure rendering remains separate |
| Interrupted previous intent | Retain and refuse replay; no cleanup or compensation |

The separately named `workspace.installed.input.v1` digest canonicalizes semantic sets and binds desired bytes, identity/fence/grants, root refs, physical observations and trusted prior evidence. Observation timestamps stay in the frozen plan and are validated during apply; they are excluded from the digest. This leaves the existing boot digest protocol unchanged.

Alias comparison uses `golang.org/x/text v0.39.0`, selected by the module graph, under its [BSD license](https://go.googlesource.com/text/+/refs/tags/v0.39.0/LICENSE). NFC and Unicode folding detect proposed and observed aliases without normalizing paths on disk. The host must attest filesystem case behavior; Linux/Darwin stable file identities and confined reads do not claim protection against a noncooperating writer replacing ancestors. Other platforms or unknown custody/case coverage refuse.

Installed reads use the module-selected `golang.org/x/sys v0.48.0` [BSD-licensed system calls](https://go.googlesource.com/sys/+/refs/tags/v0.48.0/LICENSE). Pinned directory descriptors and `openat` with `O_NOFOLLOW` reject links at each path component; a regular-file type check precedes the defensive nonblocking open. Linux runtime checks cover static FIFO and confined/intermediate link refusal. Darwin cross-build coverage does not establish runtime filesystem case or custody behavior.
