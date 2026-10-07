# Runtime payloads in protocol 2

Validate params before invoking an implemented callback. A required field must
be present, have the listed type, and be non-null. Identifier strings must contain
at least one non-whitespace character. Empty command `session_id` and `args` are
valid. Optional means omission is permitted; a present structural null is invalid.
Unknown or wrongly cased structural fields and duplicate decoded keys are invalid.
A missing handler returns -32601 before its payload is decoded; an implemented
handler's invalid params return -32602. Init retains its plugin-init/2 error data.
Notifications suppress replies but still validate before invocation.

Hosts must await the Init reply before sending ordinary requests. While Init is
pending, ordinary requests receive -32600 (successful init required); they are
not queued for later invocation. A live duplicate request ID fences the connection
in base v2 too: no second callback or second reply executes for that ID. IDs stay
live through the whole terminal reply write. Base IDs may be reused after that
write completes; numeric zero and strings remain distinct IDs.

| Method | Required fields | Optional fields and defaults |
| --- | --- | --- |
| plugin/init | Existing strict Init fields | identity, host_services, hooks_profile, context absent |
| plugin/load, plugin/unload, plugin/health | None | Params absent or closed object; context absent |
| command/execute | name: identifier; session_id, args: string | identity, context absent |
| event/handle | type, source: identifier; data: object; pre_hook: boolean | session_id: empty string; identity, context absent |
| crud/create | resource_type: identifier; data: object | id, filters, context absent |
| crud/read, crud/delete | resource_type, id: identifier | data, filters, context absent |
| crud/update | resource_type, id: identifier; data: object | filters, context absent |
| crud/list | resource_type: identifier | filters: empty object; id, data, context absent |
| mcp/call_tool | tool_name: identifier; arguments: object | session_id: empty string; identity, context absent |
| http/handle | method, path: identifier | raw_path, raw_query, session_id: empty string; query, headers: absent string maps; body: absent bytes; identity, context absent |
| plugin/migrate | from_version, to_version, data_dir: identifier | context absent |

CRUD uses one shared Go DTO: irrelevant but known fields are accepted when they
have the listed type (id is an identifier, data/filters are objects). Required
fields vary by operation, as recorded in schema.json's method table. High-level
handler signatures remain unchanged. Migration's data_dir is validated on the
wire, while its existing callback still receives the two versions.

HTTP body is canonical padded standard base64, including the empty string. Byte
arrays, URL-safe alphabets, omitted padding, nonzero padding bits and embedded
newlines are rejected. HTTP callback byte buffers are encoded back to base64.
No HTTP policy, URL routing, identity authentication or descriptor-specific data
schema is inferred here.

Opaque data and identity retain their own field names and casing; identity may be
any non-null JSON value, while the business data/arguments fields above require
objects. Duplicate decoded keys and unpaired Unicode are rejected at every depth.
Runtime number tokens must convert to finite binary64 values; integer tokens in
context follow its stricter range/form rules. Go preserves raw identity tokens; TS reconstructs objects from individual scanner
members, avoiding the V8 object-key cache bug for successive escaped keys. Payload
validation does not use JSON.parse object keys for structural decisions.

Every forward params DTO can carry optional `context`, reusing ForwardContext from
host-rpc.schema.json: required positive integer timeout_ms (at most 4294967295),
optional nonblank bounded binding_id. The object is closed: no identity, depth,
parent_call or extra fields. Omission is valid; null is invalid. This is metadata,
not authority or a local timer. Hosts validate bindings and clip deadlines; the
SDK does neither in this slice. Go callbacks read ForwardContextFromContext(ctx);
TS callbacks read context.forwardContext. It is scoped to the invocation, including
Init and explicit cleanup, and is not inherited from Init by later calls. Implicit
EOF/signal cleanup has no invocation metadata. A malformed unload payload does not
fence work; a valid unload retains terminal behavior, even when Init failed.

# Result encoding and defaults

Validate authored results before replying. Missing required fields, wrong types,
unknown structural fields, invalid nested JSON, serialization errors and cycles
become -32603. No partial success replaces an unserializable CRUD item. Go's typed
scalar zero values are encoded for required fields; TS must supply those fields.
Optional zero/empty values follow Go's documented omission behavior. TS permits
undefined only for an omitted optional top-level result field, never inside opaque
objects or array items.

| Result | Required encoding | Optional omission/default |
| --- | --- | --- |
| InitResult | Existing strict Init fields | Profile acknowledgements remain absent |
| LoadResult | Object, possibly empty | skipped_registrations omitted when empty; items require kind/id identifiers and reason string |
| CommandExecResult | action string, including empty | content empty and envelopes empty omitted |
| EventHandleResult | Object, possibly empty | cancel false, reason empty, envelopes empty omitted |
| HealthResult | ok boolean | message empty omitted; no callback defaults ok true |
| CRUDResult | data object or null | Nil/null map is intentional null |
| CRUDListResult | items array | Nil/null list becomes empty array; nil/null map items are allowed |
| MCPCallResult | content JSON, including null | is_error false and envelopes empty omitted |
| HTTPResponse | status integer | headers empty and body empty omitted |
| MigrateResult | Object, possibly empty | notes empty omitted (Serve currently emits empty object) |
| OKResult | ok true | None |
| EnvelopeOut | type identifier; data object or null | session_id empty omitted |

Returned envelopes are structurally closed, with opaque data keys retained.
This matrix does not implement frame budgets, reverse dispatch, hook/handle,
cancellation negotiation or optional-profile acknowledgement. Those have separate
stages. The shared payload-validation transcript covers required/null/type/casing
rules, defaults, context, malformed-unload recovery and the escaped-key vector pair.
