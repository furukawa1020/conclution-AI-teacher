import assert from "node:assert/strict";
import test from "node:test";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
const bridge=readFileSync(new URL("../web/firebase-bridge.js",import.meta.url),"utf8");
const start=bridge.indexOf("async function consumeVoiceStream(");
const end=bridge.indexOf("async function finishTurn(",start);
assert.ok(start>=0 && end>start);
function consumer(extra={}) {
 return runInNewContext(`${bridge.slice(start,end)};consumeVoiceStream`,{
  TextDecoder,Uint8Array,sessionEpoch:2,stoppedSessionCode:()=>"request_cancelled",
  fail:code=>{throw Error(code);},
  isNdjsonContentType:type=>type==="application/x-ndjson",
  VOICE_STREAM_LIMITS:{maximumResponseBytes:1024},...extra,
 });
}
for(const mode of ["type","length"]) test(`rejected ${mode} cancels the body`,async()=>{
 let cancelled=0;
 const response={headers:new Headers({"Content-Type":mode==="type"?"text/html":"application/x-ndjson","Content-Length":mode==="length"?"1025":"1"}),
  body:{getReader(){assert.fail("must not read");},async cancel(){cancelled++;}}};
 await assert.rejects(consumer()(response,{},2,false),/voice_response_invalid/);
 assert.equal(cancelled,1);
});
test("stale EOF is rejected before parser finalization",async()=>{
 let cancelled=0,released=0;
 const response={headers:new Headers({"Content-Type":"application/x-ndjson"}),body:{getReader(){return {
  read:async()=>({done:true}),cancel:async()=>{cancelled++;},releaseLock(){released++;}
 };}}};
 const run=consumer({createVoiceStreamParser:()=>({push(){assert.fail("stale parser used");},finish(){assert.fail("stale parser finished");}})});
 await assert.rejects(run(response,{},1,false),/request_cancelled/);
 assert.equal(cancelled,1);assert.equal(released,1);
});
