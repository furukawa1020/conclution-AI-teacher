import assert from "node:assert/strict";
import test from "node:test";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
const bridge=readFileSync(new URL("../web/firebase-bridge.js",import.meta.url),"utf8");
const start=bridge.indexOf("function acquireMicrophoneWithinDeadline(");
const end=bridge.indexOf("async function ensureMediaStream(",start);
assert.ok(start>=0 && end>start);
function harness(){
 let timeout;const cleared=[];
 const acquire=runInNewContext(`${bridge.slice(start,end)};acquireMicrophoneWithinDeadline`,{
  setTimeout(fn,ms){assert.equal(ms,30000);timeout=fn;return 1;},clearTimeout:id=>cleared.push(id),
 });
 return {acquire,cleared,expire:()=>timeout()};
}
test("microphone acquisition clears deadline on success and failure",async()=>{
 for(const failure of [false,true]){
  const h=harness();const stream={};const error=Error("permission");
  const result=h.acquire(()=>{if(failure)throw error;return stream;});
  if(failure)await assert.rejects(result,e=>e===error);else assert.equal(await result,stream);
  assert.deepEqual(h.cleared,[1]);
 }
});
test("expired permission request releases every late track",async()=>{
 const h=harness();let release;let stopped=0;
 const pending=new Promise(resolve=>{release=resolve;});
 const result=h.acquire(()=>pending);h.expire();
 await assert.rejects(result,/microphone_unavailable/);
 release({getTracks:()=>[{stop(){stopped++;throw Error();}},{stop(){stopped++;}}]});
 await new Promise(resolve=>setImmediate(resolve));assert.equal(stopped,2);
});
test("late permission rejection is consumed after timeout",async()=>{
 const h=harness();let reject;
 const pending=new Promise((_,fail)=>{reject=fail;});
 const result=h.acquire(()=>pending);h.expire();await assert.rejects(result,/microphone_unavailable/);
 reject(Error("late"));await new Promise(resolve=>setImmediate(resolve));
});
