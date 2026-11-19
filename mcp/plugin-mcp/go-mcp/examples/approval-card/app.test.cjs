const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const http = require('node:http');

class Element {
  constructor(tag) {this.tag=tag;this.children=[];this.events={};this.value='';this.textContent='';}
  append(...children){this.children.push(...children);}
  replaceChildren(...children){this.children=children;}
  addEventListener(name, fn){this.events[name]=fn;}
  all(tag){return this.children.flatMap(e=>[...(e.tag===tag?[e]:[]),...e.all(tag)]);}
}
async function api(code=200) {
  const calls=[];
  const server=http.createServer(async(req,res)=>{
    let body='';for await(const part of req)body+=part;
    calls.push({path:req.url,headers:req.headers,body});res.writeHead(code);res.end('untrusted response body');
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  return {calls,origin:`http://127.0.0.1:${server.address().port}`,close:()=>new Promise(resolve=>server.close(resolve))};
}
function app(card, gateOrigin, intakeOrigin) {
  const root=new Element('main'),status=new Element('p'),messages=[],requests=[],logs=[];
  let listener;
  const parent={postMessage:msg=>messages.push(msg)};
  const context={config:{gateOrigin,intakeOrigin},window:{parent,addEventListener:(_,fn)=>listener=fn},
    document:{getElementById:id=>id==='card'?root:status,createElement:tag=>new Element(tag)},URL,URLSearchParams,
    fetch:async(url,opts)=>{requests.push({url:String(url),opts});return fetch(url,opts);},console:{log:(...x)=>logs.push(x),error:(...x)=>logs.push(x)}};
  vm.runInNewContext(fs.readFileSync('app.js','utf8'),context);
  const receive=data=>listener({source:parent,data:{jsonrpc:'2.0',...data}});
  receive({id:1,result:{protocolVersion:'2026-01-26'}});
  const toolResult={structuredContent:{card}};
  receive({method:'ui/notifications/tool-result',params:toolResult});
  return {root,status,messages,requests,logs,toolResult,receive};
}
const cardFor=type=>({version:'gatecrasher.card/v1',approval_id:'apr_test',type,status:'pending',tier:'critical',title:'<script>untrusted</script>',expires_at:'2099-01-01T00:00:00Z',
 prompt:{question:'Pick or answer',choices:[{id:'a',label:'Choice A'}]},actions:[]});
const action=kind=>({kind,label:kind,outcome:kind==='deny'?'denied':'approved',decide:'/api/gate/v1/approvals/apr_test/decide'});

test('four card kinds: direct decisions use only client credential; secret reaches only intake', async()=>{
 const gate=await api(),intake=await api();
 try {
  for(const [type,kind,value] of [['approve','approve',''],['choose','choose','a'],['answer','answer','An answer'],['request_secret','provide_secret','sentinel-'+crypto.randomUUID()]]) {
   const card=cardFor(type);card.actions=[action(kind),action('deny')];
   card.secret={post_url:intake.origin+'/write-only',field:'value',write_only:true,expires_at:card.expires_at};
   const a=app(card,gate.origin,intake.origin);
   const inputs=a.root.all('input');inputs[0].value='owner-client-'+type;
   if(inputs[1])inputs[1].value=value;
   if(type==='choose')a.root.all('select')[0].value=value;
   const before=JSON.stringify(a.toolResult);
   await a.root.all('button')[0].events.click();
   assert.equal(a.requests.length,1);assert.equal(inputs[0].value,'');
   if(type==='request_secret') {
    assert.equal(gate.calls.length,3);
    assert.equal(intake.calls.length,1);
    assert.equal(new URLSearchParams(intake.calls[0].body).get('value'),value);
    assert.equal(intake.calls[0].headers.authorization,undefined);
    assert.equal(inputs[1].value,'');
    assert.equal(JSON.stringify(a.messages).includes(value),false);
    assert.equal(JSON.stringify(a.toolResult).includes(value),false);
    assert.equal(JSON.stringify(a.logs).includes(value),false);
    assert.equal(JSON.stringify(a.messages).includes('owner-client'),false);
    assert.equal(JSON.stringify(a.toolResult),before);
    assert.equal(a.messages.length,2); // initialize and initialized only: no tools/call or model transcript messages
   } else {
    const req=gate.calls.at(-1);assert.equal(req.headers.authorization,'Bearer owner-client-'+type);
    const body=JSON.parse(req.body);assert.equal(body.outcome,'approved');
    if(type==='choose')assert.equal(body.choice,'a');if(type==='answer')assert.equal(body.answer,value);
   }
   assert.equal(a.requests[0].opts.redirect,'error');assert.equal(a.requests[0].opts.credentials,'omit');
  }
 } finally {await gate.close();await intake.close();}
});

test('decline works for every kind and never submits secret to Gatecrasher',async()=>{
 const gate=await api();try{
  for(const type of ['approve','choose','answer','request_secret']){
   const card=cardFor(type);card.actions=[action('deny')];const a=app(card,gate.origin,gate.origin);
   const inputs=a.root.all('input');inputs[0].value='client-owner';if(inputs[1])inputs[1].value='private-input';
   await a.root.all('button')[0].events.click();
   assert.deepEqual(JSON.parse(gate.calls.at(-1).body),{outcome:'denied'});
  }
 }finally{await gate.close();}
});

test('terminal and expired cards have no controls; forged origins and paths fail closed',async()=>{
 for(const patch of [{status:'approved'},{expires_at:'2000-01-01T00:00:00Z'}]){
  const card={...cardFor('approve'),...patch,actions:[action('approve')]};assert.equal(app(card,'https://gate.example','https://intake.example').root.all('button').length,0);
 }
 for(const decide of ['https://evil.example/decide','/api/gate/v1/approvals/other/decide','/api/gate/v1/approvals/apr_test/decide?token=x']){
  const card=cardFor('approve');card.actions=[{...action('approve'),decide}];const a=app(card,'https://gate.example','https://intake.example');
  a.root.all('input')[0].value='private-owner';await a.root.all('button')[0].events.click();assert.equal(a.requests.length,0);
 }
 const card=cardFor('request_secret');card.actions=[action('provide_secret')];card.secret={post_url:'https://evil.example/intake',field:'value',write_only:true,expires_at:card.expires_at};
 const a=app(card,'https://gate.example','https://intake.example');a.root.all('input')[1].value='private-secret';await a.root.all('button')[0].events.click();assert.equal(a.requests.length,0);
});

test('shadow, terminal, denied, and failed responses never echo response bodies or retry',async()=>{
 for(const code of [202,409,403,500]){
  const gate=await api(code);try{
   const card=cardFor('approve');card.actions=[action('approve')];const a=app(card,gate.origin,gate.origin);a.root.all('input')[0].value='client-owner';
   const button=a.root.all('button')[0];await button.events.click();assert.equal(gate.calls.length,1);assert.equal(a.root.all('button').length,0);
   assert.equal(a.status.textContent.includes('untrusted response body'),false);
   if(code===202)assert.match(a.status.textContent,/Shadow/);
  }finally{await gate.close();}
 }
});

test('secret intake must be write-only, unexpired, and use the value field',async()=>{
 for(const patch of [{write_only:false},{field:'other'},{expires_at:'2000-01-01T00:00:00Z'},{expires_at:'invalid'}]){
  const card=cardFor('request_secret');card.actions=[action('provide_secret')];card.secret={post_url:'https://intake.example/accept',field:'value',write_only:true,expires_at:card.expires_at,...patch};
  const a=app(card,'https://gate.example','https://intake.example');const secret=a.root.all('input')[1];secret.value='private-input';
  await a.root.all('button')[0].events.click();assert.equal(a.requests.length,0);assert.equal(secret.value,'');
 }
});
