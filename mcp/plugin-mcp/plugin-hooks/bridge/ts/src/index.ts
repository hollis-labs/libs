/** Never-published reference mapping. Inject codecs from the pinned SDK source.
 * Hosts own registration identity and binding ledgers; wire diagnostics grant nothing.
 */
export interface Incarnation {
  host_instance: string;
  owner_id: string;
  owner_generation: number;
}
export interface RemoteRequest {
  invocationID: string;
  catalogVersion: string;
  hook: string;
  schemaDigest: string;
  kind: "action" | "filter";
  mode:
    "sequential" | "parallel" | "bail" | "waterfall" | "async" | "after_commit";
  scope: {
    hostInstance: string;
    owner: string;
    generation: string;
    registrationID: string;
  };
  context: {
    bindingID?: string;
    connectionID: string;
    timeoutMS: number;
    aggregateBudgetMS: number;
    deadline: string;
    depth: number;
    trace: { trace_id: string; span_id: string };
    rootInvocationID: string;
    parentInvocationID?: string;
  };
  payloadJSON: string;
  metadata: Record<string, string>;
}
export interface RemoteFailure {
  code: string;
  message?: string;
}
export interface RemoteResult {
  invocationID: string;
  status: string;
  payloadJSON?: string;
  reason?: string;
  failure?: RemoteFailure;
}
export interface WireRequest {
  invocation_id: string;
  catalog_version: string;
  hook: string;
  schema_digest: string;
  kind: RemoteRequest["kind"];
  mode: RemoteRequest["mode"];
  scope: { incarnation: Incarnation; registration_id: string };
  context: { binding_id?: string; timeout_ms: number };
  payload?: unknown;
  payloadJSON?: string;
  metadata: Record<string, string>;
  deadline: string;
  aggregate_budget_ms: number;
  depth: number;
  trace: RemoteRequest["context"]["trace"];
  root_invocation_id: string;
  parent_invocation_id?: string;
}
export interface WireResult {
  invocation_id: string;
  status: string;
  payload?: unknown;
  payloadJSON?: string;
  reason?: string;
  error?: RemoteFailure;
}
// Structural codec interface keeps this private package independent of an npm
// release. Tests resolve the actual SDK from the Go module's pinned source.
export interface SDKCodecs {
  rawJSON(raw: string): string;
  encodeHookHandleParams(p: WireRequest): string;
  decodeHookHandleParams(raw: string): WireRequest;
  validateHookRequest(p: WireRequest, notification: boolean): void;
  encodeHookHandleResult(r: WireResult): string;
  decodeHookHandleResult(raw: string): WireResult;
  validateHookResultFor(p: WireRequest, r: WireResult): void;
  hookPayloadJSON(r: WireResult): string | undefined;
  decodeHookFailure(raw: string): RemoteFailure;
  encodeHookHandleBatchParams(p: { items: WireRequest[] }): string;
  decodeHookHandleBatchResult(raw: string): { items: WireResult[] };
  validateHookBatchRequest(
    p: { items: WireRequest[] },
    notification: boolean,
  ): void;
  validateHookBatchResultFor(
    p: { items: WireRequest[] },
    r: { items: WireResult[] },
  ): void;
}
export class Bridge {
  constructor(
    readonly sdk: SDKCodecs,
    readonly cancelled: Error,
    readonly approvalRequired: Error,
  ) {}
  request(
    q: RemoteRequest,
    incarnation: Incarnation,
    notification = false,
  ): WireRequest {
    if (
      incarnation.host_instance !== q.scope.hostInstance ||
      incarnation.owner_id !== q.scope.owner
    )
      throw new Error("host identity mismatch");
    if (notification && q.context.bindingID !== undefined)
      throw new Error("notification has binding");
    const p: WireRequest = {
      invocation_id: q.invocationID,
      catalog_version: q.catalogVersion,
      hook: q.hook,
      schema_digest: q.schemaDigest,
      kind: q.kind,
      mode: q.mode,
      scope: {
        incarnation: { ...incarnation },
        registration_id: q.scope.registrationID,
      },
      context: {
        timeout_ms: q.context.timeoutMS,
        ...(q.context.bindingID === undefined
          ? {}
          : { binding_id: q.context.bindingID }),
      },
      payloadJSON: this.sdk.rawJSON(q.payloadJSON),
      metadata: { ...q.metadata },
      deadline: q.context.deadline,
      aggregate_budget_ms: q.context.aggregateBudgetMS,
      depth: q.context.depth,
      trace: { ...q.context.trace },
      root_invocation_id: q.context.rootInvocationID,
      ...(q.context.parentInvocationID === undefined
        ? {}
        : { parent_invocation_id: q.context.parentInvocationID }),
    };
    this.sdk.validateHookRequest(p, notification);
    return p;
  }
  restoreRequest(
    p: WireRequest,
    host: RemoteRequest,
    incarnation: Incarnation,
  ): RemoteRequest {
    const expected = this.request(
      host,
      incarnation,
      host.context.bindingID === undefined,
    );
    const actualRaw = this.sdk.encodeHookHandleParams(p),
      expectedRaw = this.sdk.encodeHookHandleParams(expected);
    const actual = JSON.parse(actualRaw),
      wanted = JSON.parse(expectedRaw);
    delete actual.payload;
    delete wanted.payload;
    if (
      canonical(actual) !== canonical(wanted) ||
      this.sdk.decodeHookHandleParams(actualRaw).payloadJSON !==
        this.sdk.decodeHookHandleParams(expectedRaw).payloadJSON
    )
      throw new Error("host snapshot mismatch");
    return {
      ...host,
      scope: { ...host.scope },
      context: { ...host.context, trace: { ...host.context.trace } },
      metadata: { ...p.metadata },
      payloadJSON: host.payloadJSON,
    };
  }
  result(p: WireRequest, raw: string): RemoteResult {
    try {
      const r = this.sdk.decodeHookHandleResult(raw);
      this.sdk.validateHookResultFor(p, r);
      const payload = this.sdk.hookPayloadJSON(r);
      return {
        invocationID: r.invocation_id,
        status: r.status,
        ...(payload === undefined ? {} : { payloadJSON: payload }),
        ...(r.reason === undefined ? {} : { reason: r.reason }),
        ...(r.error === undefined ? {} : { failure: this.failure(r.error) }),
      };
    } catch {
      return {
        invocationID: p.invocation_id,
        status: "failed",
        failure: { code: "invalid_output" },
      };
    }
  }
  wireResult(p: WireRequest, r: RemoteResult): string {
    const wire: WireResult = {
      invocation_id: r.invocationID,
      status: r.status,
      ...(r.payloadJSON === undefined
        ? {}
        : { payloadJSON: this.sdk.rawJSON(r.payloadJSON) }),
      ...(r.reason === undefined ? {} : { reason: r.reason }),
      ...(r.failure === undefined ? {} : { error: this.failure(r.failure) }),
    };
    this.sdk.validateHookResultFor(p, wire);
    return this.sdk.encodeHookHandleResult(wire);
  }
  /** Deliberate sentinels are reconstructed only AFTER validated status mapping. */
  resultError(p: WireRequest, raw: string): Error | undefined {
    const r = this.result(p, raw);
    if (r.status === "cancelled") return this.cancelled;
    if (r.status === "approval_required") return this.approvalRequired;
    if (r.failure) return new Error(r.failure.code);
    return undefined;
  }
  failure(f: RemoteFailure): RemoteFailure {
    return { ...this.sdk.decodeHookFailure(JSON.stringify(f)) };
  }
  batchRequest(
    requests: RemoteRequest[],
    identities: Incarnation[],
  ): { items: WireRequest[] } {
    if (requests.length !== identities.length)
      throw new Error("identity count mismatch");
    const p = {
      items: requests.map((q, i) => this.request(q, identities[i]!)),
    };
    this.sdk.validateHookBatchRequest(p, false);
    return p;
  }
  batchResult(p: { items: WireRequest[] }, raw: string): RemoteResult[] {
    const r = this.sdk.decodeHookHandleBatchResult(raw);
    this.sdk.validateHookBatchResultFor(p, r);
    return r.items.map((item, i) =>
      this.result(p.items[i]!, this.sdk.encodeHookHandleResult(item)),
    );
  }
  notification(q: RemoteRequest, identity: Incarnation): WireRequest {
    return this.request(q, identity, true);
  }
}

function canonical(value: unknown): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
  const object = value as Record<string, unknown>;
  return (
    "{" +
    Object.keys(object)
      .sort()
      .map((key) => JSON.stringify(key) + ":" + canonical(object[key]))
      .join(",") +
    "}"
  );
}
