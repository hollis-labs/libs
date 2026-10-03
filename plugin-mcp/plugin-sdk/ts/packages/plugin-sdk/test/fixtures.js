import { enableHooksFixture } from '../dist/hooks-fixture.js';
import { ErrCancelled, errNotFound, errConflict, errValidation, PluginError } from '../dist/index.js';

function fail(name) {
  if (name === 'not-found') throw new Error('wrapped: missing', { cause: errNotFound('missing') });
  if (name === 'conflict') throw errConflict('exists');
  if (name === 'validation') throw errValidation('invalid');
  if (name === 'cancelled') throw new Error('wrapped: plugin: action cancelled by hook', { cause: ErrCancelled });
  if (name === 'internal') throw new Error('failed');
  if (name === 'other-status') throw new PluginError(403, 'denied');
  if (name === 'panic') throw 'fixture panic';
}
export function fixturePlugin(profile) {
  const base = {
    init() { return { id: 'fixture', name: 'Fixture', version: '1.0.0', description: 'conformance', protocol: 2, capability_contract:1 }; },
    load() { return { skipped_registrations: [{ kind: 'command', id: 'optional', reason: 'no config' }] }; },
    unload() {},
  };
  if (profile === 'hooks-fixture' || profile === 'hooks-declined') {
    const plugin = {...base, init(){return {...base.init(),hooks_profile_version:1};}, health(){return {ok:true};}, hookHandle(ctx,p) {
      switch(p.metadata.fixture) {
        case 'cancelled': case 'approval_required': return {invocation_id:p.invocation_id,status:p.metadata.fixture,reason:'fixture veto'};
        case 'handler_error': throw new Error('private backend');
        case 'panic': throw 'fixture panic';
        case 'invalid_output': return {invocation_id:'wrong',status:'ok'};
        case 'wait': return new Promise(resolve=>ctx.signal.addEventListener('abort',()=>resolve({invocation_id:p.invocation_id,status:'ok'}),{once:true}));
      }
      return {invocation_id:p.invocation_id,status:'ok',...(p.kind==='filter'?{payloadJSON:p.payloadJSON}:{})};
    }};
    if(profile==='hooks-fixture') enableHooksFixture(plugin);
    return plugin;
  }
  if (profile === 'lifecycle-shutdown') {
    const effects = {unload_attempts:0,health_calls:0};
    return {...base,unload(){effects.unload_attempts++;},health(){effects.health_calls++;return {ok:true};},effects(){return {...effects};}};
  }
  if (profile === 'base') return base;
  if (profile === 'frame-output') return {...base,health(){return {ok:true,message:'x'.repeat(8*1024*1024)};}};
  if (profile === 'lifecycle-error') return { ...base, init() { throw errNotFound('missing'); }, load() { throw ErrCancelled; } };
  if (profile === 'health-error') return { ...base, health() { throw new Error('unhealthy'); } };
  if (profile !== 'full') throw new Error(`unknown fixture profile ${profile}`);
  return {
    ...base,
    command(ctx, r) {
      if(r.name === "echo-context") return {action:"message",content:ctx.forwardContext ? `${ctx.forwardContext.binding_id ?? ""}:${ctx.forwardContext.timeout_ms}` : "absent"};
      fail(r.name);
      return { action: 'message', content: r.args, envelopes: [{ type: 'fixture.echo', data: { name: r.name, identity: r.identity ?? null }, session_id: r.session_id }] };
    },
    eventHandle(_ctx, r) { fail(r.type); return r.pre_hook ? { cancel: true, reason: 'veto' } : {}; },
    create(_ctx, _type, data) { return data; },
    read(_ctx, type, id) { fail(id); return { id, resource_type: type }; },
    update(_ctx, _type, _id, data) { return data; },
    delete() {}, list() { return null; },
    mcpCallTool(_ctx, r) { fail(r.tool_name); return { content: r.arguments, is_error: r.tool_name === 'tool-error' }; },
    httpHandle(_ctx, r) { fail(r.path); return { status: 201, headers: { 'content-type': 'application/octet-stream' }, body: r.body }; },
    migrate(_ctx, from) { fail(from); }, health() { return { ok: true }; },
  };
}

export function initParams(overrides = {}) { return {plugin_dir:'/fixture',data_dir:'/fixture/data',cache_dir:'/fixture/cache',config:{},log_level:'info',host_info:{version:'fixture',protocol:2},capability_contract:1,incarnation:{host_instance:'fixture-host',owner_id:'fixture',owner_generation:1},grants:[],...overrides}; }
