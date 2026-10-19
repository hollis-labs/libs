const fs = require('node:fs');
const crypto = require('node:crypto');
const d = JSON.parse(fs.readFileSync(process.argv[2]));
const canon = x => Array.isArray(x) ? '['+x.map(canon).join(',')+']' : x && typeof x==='object' ? '{'+Object.keys(x).sort().map(k=>JSON.stringify(k)+':'+canon(x[k])).join(',')+'}' : JSON.stringify(x);
const hash=x=>crypto.createHash('sha256').update(x).digest();
if(canon(d.cases[0].operation)!==d.operation_canonical_utf8)throw Error('operation encoding');
if('sha256:'+hash(d.operation_canonical_utf8).toString('hex')!==d.operation_digest)throw Error('operation hash');
if(hash(Buffer.concat([Buffer.from('hollis operation proof v1\0'),Buffer.from(d.binding_canonical_utf8)])).toString('base64url')!==d.challenge_base64url)throw Error('challenge');
for(const c of d.cases){
 const a=c.response.assurance_evidence.assertion;
 const ok=crypto.verify('sha256',Buffer.concat([Buffer.from(a.authenticator_data,'base64url'),hash(Buffer.from(a.client_data_json,'base64url'))]),d.public_key_pem,Buffer.from(a.signature,'base64url'));
 const expected=!['corrupt_signature','duplicate_client_data'].includes(c.name);
 if(ok!==expected)throw Error(c.name+': signature discrepancy');
}
console.log('Independent Node check: canonical bytes, digests and all ES256 signatures match expectations.');
