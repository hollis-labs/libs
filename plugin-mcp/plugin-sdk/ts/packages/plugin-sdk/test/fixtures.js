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
    init() { return { id: 'fixture', name: 'Fixture', version: '1.0.0', description: 'conformance', protocol: 1 }; },
    load() { return { skipped_registrations: [{ kind: 'command', id: 'optional', reason: 'no config' }] }; },
    unload() {},
  };
  if (profile === 'base') return base;
  if (profile === 'lifecycle-error') return { ...base, init() { throw errNotFound('missing'); }, load() { throw ErrCancelled; } };
  if (profile === 'health-error') return { ...base, health() { throw new Error('unhealthy'); } };
  if (profile !== 'full') throw new Error(`unknown fixture profile ${profile}`);
  return {
    ...base,
    command(_ctx, r) {
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
