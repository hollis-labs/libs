import type { BuildManifest } from './types.js';
import { treeDigest, validBundlePath } from './artifact.js';
import { parseJSON } from './json.js';

type Shape = 'string' | 'boolean' | 'integer' | 'raw' | { map: Shape } | { array: Shape } | { fields: Record<string, Shape>; optional?: string[]; preserve?: string[] };
const range: Shape = { fields: { min: 'string', max: 'string' }, optional: ['min','max'] };
const field: Shape = { fields: { type:'string',label:'string',description:'string',required:'boolean',default:'string',env:'string',options:{array:'string'} }, optional:['label','description','required','default','env','options'] };
const secret: Shape = { fields:{label:'string',description:'string',required:'boolean',env:'string'},optional:['label','description','required','env'] };
const annotations: Shape = {fields:{title:'string',readOnlyHint:'boolean',destructiveHint:'boolean',idempotentHint:'boolean',openWorldHint:'boolean'},optional:['title','readOnlyHint','destructiveHint','idempotentHint','openWorldHint'],preserve:['readOnlyHint','destructiveHint','idempotentHint','openWorldHint']};
const shape: Shape = { fields: {
  schema_version:'integer',id:'string',name:'string',description:'string',version:'string',license:'string',homepage:'string',repository:'string',protocol:'integer',runtime:'string',
  server:{fields:{runtime:'string',engines:{map:range},entry:'string'}},
  ui:{fields:{bundle:'string',stylesheet:'string',isolation:'string'},optional:['stylesheet']},
  artifact:{fields:{files:{array:{fields:{path:'string',sha256:'string',executable:'boolean'},optional:['executable']}},tree_sha256:'string'}},
  hooks:{array:{fields:{name:'string',priority:'integer',once:'boolean',view:'string',schema_digest:'string',mode:'string',timeout:'integer',on_error:'string'},optional:['priority','once','view','schema_digest'],preserve:['priority','once','view','schema_digest']}},
  capabilities:{array:{fields:{name:'string',reason:'string',optional:'boolean',metadata:'raw'},optional:['reason','optional','metadata'],preserve:['metadata']}},
  config:{fields:{fields:{map:field},secrets:{map:secret}},optional:['fields','secrets']},
  tools:{array:{fields:{name:'string',description:'string',input_schema:'raw',effect:'string',annotations},optional:['annotations'],preserve:['annotations']}},
  hosts:{map:range},cerberus:'raw',tangent:'raw',nanite:'raw',
},optional:['description','license','homepage','repository','ui','hooks','capabilities','config','tools','cerberus','tangent','nanite'],preserve:['ui','config','cerberus','tangent','nanite']};
function record(v: unknown): v is Record<string, unknown> { return v !== null && typeof v === 'object' && !Array.isArray(v) && [Object.prototype,null].includes(Object.getPrototypeOf(v)); }
function normalize(v: unknown, s: Shape, name: string): unknown {
  if (s === 'raw') return v;
  if (typeof s === 'string') {
    if (s === 'integer' ? !Number.isSafeInteger(v) : typeof v !== s) throw new Error(`${name} must be ${s}`);
    return v;
  }
  if ('array' in s) { if (!Array.isArray(v)) throw new Error(`${name} must be array`); return v.map((x,i)=>normalize(x,s.array,`${name}[${i}]`)); }
  if (!record(v)) throw new Error(`${name} must be object`);
  const out: Record<string,unknown> = Object.create(null) as Record<string,unknown>;
  if ('map' in s) { for (const key of Object.keys(v).sort()) out[key] = normalize(v[key],s.map,`${name}.${key}`); return out; }
  for (const key of Object.keys(v)) if (!Object.hasOwn(s.fields,key)) throw new Error(`${name}: unknown field ${key}`);
  for (const [key,child] of Object.entries(s.fields)) {
    if (!Object.hasOwn(v,key)) { if (!s.optional?.includes(key)) throw new Error(`${name}.${key} is required`); continue; }
    const value = normalize(v[key],child,`${name}.${key}`);
    if (s.optional?.includes(key) && !s.preserve?.includes(key) && (value === '' || value === false || (Array.isArray(value) && !value.length) || (record(value) && !Object.keys(value).length))) continue;
    out[key] = value;
  }
  return out;
}
function jsonValue(value:unknown, ancestors=new Set<object>(), depth=0):void {
  if(depth>64)throw new Error('JSON nesting exceeds 64 levels');
  if(value===null||typeof value==='string'||typeof value==='boolean')return;
  if(typeof value==='number'){if(!Number.isFinite(value)||(Number.isInteger(value)&&!Number.isSafeInteger(value)))throw new Error('unsafe JSON number');return;}
  if(!Array.isArray(value)&&!record(value))throw new Error('author values must be plain JSON');
  if(ancestors.has(value))throw new Error('cyclic JSON');
  if(Object.getOwnPropertySymbols(value).length)throw new Error('JSON symbol keys are forbidden');
  if(Array.isArray(value)&&Object.keys(value).length!==value.length)throw new Error('sparse/non-JSON array');
  ancestors.add(value);for(const child of Object.values(value))jsonValue(child,ancestors,depth+1);ancestors.delete(value);
}
const id = /^[a-z0-9]+([.-][a-z0-9]+)*$(?![\s\S])/u;
const token = /^[a-zA-Z_][a-zA-Z0-9_.-]*$(?![\s\S])/u;
const env = /^[a-zA-Z_][a-zA-Z0-9_]*$(?![\s\S])/u;
const semver = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$(?![\s\S])/u;
function validVersion(v:string):boolean { const m=semver.exec(v); return !!m && (!m[4] || m[4].split('.').every(p=>!/^0\d+$(?![\s\S])/u.test(p))); }
function numeric(a:string,b:string):number { return a.length-b.length || (a<b?-1:a>b?1:0); }
function compare(a:string,b:string):number {
  const aa=semver.exec(a)!,bb=semver.exec(b)!;
  for(let i=1;i<=3;i++){const c=numeric(aa[i],bb[i]);if(c)return c;}
  if(aa[4]===bb[4])return 0;if(!aa[4])return 1;if(!bb[4])return -1;
  const ap=aa[4].split('.'),bp=bb[4].split('.');
  for(let i=0;i<Math.min(ap.length,bp.length);i++){const an=/^\d+$(?![\s\S])/u.test(ap[i]),bn=/^\d+$(?![\s\S])/u.test(bp[i]);const c=an&&bn?numeric(ap[i],bp[i]):an?-1:bn?1:ap[i]<bp[i]?-1:ap[i]>bp[i]?1:0;if(c)return c;}
  return ap.length-bp.length;
}
function demand(ok:unknown,message:string):asserts ok { if(!ok)throw new Error(message); }
function ranges(value:Record<string,{min?:string;max?:string}>,name:string):void {
  demand(Object.keys(value).length,`${name} is required`);
  for(const [key,r] of Object.entries(value)) {
    demand(id.test(key),`${name} has invalid name`);demand(r.min||r.max,`${name}.${key} requires min or max`);
    demand(!r.min||validVersion(r.min),`${name}.${key}.min invalid`);demand(!r.max||validVersion(r.max),`${name}.${key}.max invalid`);
    demand(!r.min||!r.max||compare(r.min,r.max)<=0,`${name}.${key} min exceeds max`);
  }
}
/** Validate the shared declaration shape; host policy/schema semantics remain host-owned. */
export function validateManifest(value:unknown): asserts value is BuildManifest {
  jsonValue(value);
  const m=normalize(value,shape,'manifest') as BuildManifest;
  demand(m.schema_version===2&&m.protocol===2&&m.runtime==='subprocess','manifest requires schema/protocol 2 and subprocess');
  demand(id.test(m.id)&&m.name.trim()&&validVersion(m.version),'invalid plugin identity/version');
  for(const u of [m.homepage,m.repository])if(u){const url=new URL(u);demand(['http:','https:'].includes(url.protocol)&&url.host&&!url.username&&!url.password,'invalid manifest URL');}
  ranges(m.hosts,'hosts');ranges(m.server.engines,'server.engines');
  demand(['node','deno','bun','binary'].includes(m.server.runtime),'unsupported server runtime');
  demand(Object.keys(m.server.engines).length===1&&Object.hasOwn(m.server.engines,m.server.runtime),'engines must name selected runtime');
  demand(validBundlePath(m.server.entry)&&m.server.entry.startsWith('bin/'),'entry must be under bin/');
  demand(m.server.runtime==='binary'||/\.(?:js|mjs|cjs)$(?![\s\S])/u.test(m.server.entry),'entry must be compiled JavaScript');
  if(m.ui){demand(validBundlePath(m.ui.bundle)&&m.ui.bundle.startsWith('ui/')&&/\.(?:js|mjs)$(?![\s\S])/u.test(m.ui.bundle),'invalid ui bundle');demand(!m.ui.stylesheet||(validBundlePath(m.ui.stylesheet)&&m.ui.stylesheet.startsWith('ui/')&&m.ui.stylesheet.endsWith('.css')),'invalid stylesheet');demand(['sandboxed-frame','main-origin'].includes(m.ui.isolation),'invalid isolation preference');}
  const seenHooks=new Set<string>();for(const h of m.hooks??[]){demand(/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$(?![\s\S])/u.test(h.name)&&!seenHooks.has(h.name),'invalid/duplicate hook');seenHooks.add(h.name);demand(['sequential','parallel','bail','waterfall','async','after_commit'].includes(h.mode)&&h.timeout>0&&['open','closed'].includes(h.on_error),'invalid hook options');demand(h.view===undefined||token.test(h.view),'invalid hook view');demand(h.schema_digest===undefined||!/^\p{White_Space}*$/u.test(h.schema_digest),'invalid hook schema_digest');}
  const seenCaps=new Set<string>();for(const c of m.capabilities??[]){demand(c.name.trim()&&!seenCaps.has(c.name),'invalid/duplicate capability');seenCaps.add(c.name);}
  for(const [name,f] of Object.entries(m.config?.fields??{})){
    demand(token.test(name)&&!Object.hasOwn(m.config?.secrets??{},name),'invalid/duplicate config field');demand(!f.env||env.test(f.env),'invalid field env');
    demand(['string','boolean','integer','number','select'].includes(f.type),'invalid config field type');
    if(f.default){if(f.type==='boolean')demand(['true','false'].includes(f.default),'invalid bool default');if(f.type==='integer')demand(/^[+-]?\d+$(?![\s\S])/u.test(f.default)&&BigInt(f.default)>=-(2n**63n)&&BigInt(f.default)<2n**63n,'invalid integer default');if(f.type==='number')demand(typeof JSON.parse(f.default)==='number'&&Number.isFinite(Number(f.default)),'invalid number default');}
    if(f.type==='select')demand(f.options?.length&&(!f.default||f.options.includes(f.default)),'invalid select');else demand(!f.options?.length,'options require select');
    demand((f.options??[]).every((v,i,a)=>v!==''&&a.indexOf(v)===i),'invalid select options');
  }
  for(const [name,s] of Object.entries(m.config?.secrets??{}))demand(token.test(name)&&(!s.env||env.test(s.env)),'invalid secret declaration');
  const seenTools=new Set<string>();for(const t of m.tools??[]){demand(token.test(t.name)&&!seenTools.has(t.name)&&t.description.trim()&&t.effect.trim(),'invalid/duplicate tool');seenTools.add(t.name);demand(record(t.input_schema)&&t.input_schema.type==='object','tool schema must be inline object');demand(!(t.annotations?.readOnlyHint&&t.annotations?.destructiveHint),'inconsistent tool hints');}
  for(const name of ['cerberus','tangent','nanite'] as const)if(m[name]!==undefined)demand(record(m[name])&&Object.hasOwn(m.hosts,name),'host extension requires object and host range');
  demand(m.artifact.tree_sha256===treeDigest(m.artifact.files),'artifact tree digest mismatch');
  const refs=[m.server.entry,...(m.ui?[m.ui.bundle,...(m.ui.stylesheet?[m.ui.stylesheet]:[])]:[])];
  for(const ref of refs){const file=m.artifact.files.find(f=>f.path===ref);demand(file,'declared artifact file missing');demand(!!file.executable===(ref===m.server.entry&&m.server.runtime==='binary'),'artifact executable mode mismatch');}
}
export function decodeManifest(raw:string):BuildManifest { const m=parseJSON(raw);validateManifest(m);return m; }
function jsonText(value:unknown, s:Shape, depth=0):string {
  const indent='  '.repeat(depth);
  if(s==='raw')return JSON.stringify(value,null,2).replace(/\n/gu,'\n'+indent);
  if(typeof s==='string')return JSON.stringify(value);
  if('array' in s){const a=value as unknown[];return a.length?'[\n'+a.map(v=>indent+'  '+jsonText(v,s.array,depth+1)).join(',\n')+'\n'+indent+']':'[]';}
  const obj=value as Record<string,unknown>;
  const keys='map' in s?Object.keys(obj).sort():Object.keys(s.fields).filter(k=>Object.hasOwn(obj,k));
  return keys.length?'{\n'+keys.map(k=>indent+'  '+JSON.stringify(k)+': '+jsonText(obj[k],'map' in s?s.map:s.fields[k],depth+1)).join(',\n')+'\n'+indent+'}':'{}';
}
/** Canonical Go struct order, lexically sorted maps, two spaces and newline. */
export function encodeManifest(value:BuildManifest):string {
  validateManifest(value);
  const text=jsonText(normalize(value,shape,'manifest'),shape).replace(/[<>&\u2028\u2029]/gu,c=>'\\u'+c.charCodeAt(0).toString(16).padStart(4,'0'))+'\n';
  parseJSON(text);return text;
}
