import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
const bridge=readFileSync(new URL("../web/firebase-bridge.js",import.meta.url),"utf8");
const start=bridge.indexOf("function releaseMicrophone(");
const end=bridge.indexOf("function stoppedSessionCode(",start);
assert.ok(start>=0 && end>start);

test("microphone release detaches ownership before callbacks and tries every resource",async()=>{
 const calls=[];
 const run=new Function("calls",`let activeRecording={}; let mediaStream={old:true};
 let mediaStreamLossBinding={track:{removeEventListener(){calls.push('listener');throw Error();}}};
 let analyserSource={disconnect(){calls.push('source');throw Error();}};
 let analyser={disconnect(){calls.push('analyser');throw Error();}};let analyserStream={};
 let audioContext={state:'running',close(){calls.push('context');return Promise.reject(Error());}};
 function setVoiceReceiptVisible(){calls.push('receipt');}
 function rejectRecording(){if(activeRecording!==undefined)throw Error('owner'); mediaStream={replacement:true};calls.push('recording');throw Error();}
 function setStreamTracksEnabled(stream){if(!stream.old)throw Error('wrong stream');calls.push('disable');throw Error();}
 function stopTracks(stream){if(!stream.old)throw Error('wrong stream');calls.push('stop');throw Error();}
 ${bridge.slice(start,end)}
 releaseMicrophone();return {mediaStream,activeRecording,audioContext,analyserSource,analyser,analyserStream,mediaStreamLossBinding};`);
 const state=run(calls);
 await new Promise(resolve=>setImmediate(resolve));
 assert.deepEqual(calls,['receipt','recording','listener','disable','stop','source','analyser','context']);
 assert.deepEqual(state.mediaStream,{replacement:true});
 for(const [key,value] of Object.entries(state)) if(key!=='mediaStream') assert.equal(value,undefined,key);
});
