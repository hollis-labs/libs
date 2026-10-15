import { serve } from '../dist/index.js';
import { fixturePlugin } from './fixtures.js';
const p = fixturePlugin('full');
const init = p.init;
p.init = (ctx,params) => { ctx.config.secret('token'); ctx.logger.info('initialized', {token:ctx.config.string('token')}); return init(ctx,params); };
const command = p.command;
p.command = async (ctx,params) => {
  if (params.name !== 'wait-for-abort') return command(ctx,params);
  ctx.logger.info('waiting-for-abort');
  await new Promise(resolve => { if(ctx.signal.aborted) resolve(); else ctx.signal.addEventListener('abort',resolve,{once:true}); });
  return {action:'noop'};
};
p.unload = ctx => { ctx.logger.info('unloaded'); };
await serve(p);
