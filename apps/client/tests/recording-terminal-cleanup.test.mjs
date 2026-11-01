import assert from "node:assert/strict";
import test from "node:test";
import {readFileSync} from "node:fs";
import {runInNewContext} from "node:vm";
const bridge=readFileSync(new URL("../web/firebase-bridge.js",import.meta.url),"utf8");
function extract(name,next){const start=bridge.indexOf(`function ${name}(`);const end=bridge.indexOf(`function ${next}(`,start);assert.ok(start>=0&&end>start);return bridge.slice(start,end);}
for(const abandoned of [false,true])test(`recording settles once despite cleanup failures: abandoned=${abandoned}`,()=>{
 const calls=[];const fail=name=>()=>{calls.push(name);throw Error("device");};
 const recording={stopLatch:{isRequested:()=>true},rejectTurnEnded:fail("turn"),rejectEnd:()=>calls.push("end"),resolveEnd:()=>calls.push("end")};
 const name=abandoned?"abandonInterruptRecording":"rejectRecording";
 const source=extract(name,abandoned?"stopBargeInMonitoring":"resolveRecording");
 const run=runInNewContext(`${source};${name}`,{Blob,activeRecording:recording,activePlayback:undefined,
  stopVad:fail("vad"),setStreamTracksEnabled:fail("tracks"),discardCurrentCandidate:fail("candidate")});
 run(recording,"request_cancelled");run(recording,"request_cancelled");
 assert.deepEqual(calls,abandoned?["vad","candidate","turn","end"]:["vad","tracks","candidate","turn","end"]);
 assert.equal(recording.settled,true);assert.equal(recording.turnEnded,true);
});
test("candidate recorder still stops when buffer clearing throws",()=>{
 let stopped=0;const recording={candidate:{captureBuffer:{clear(){throw Error("buffer");}}},totalBytes:42};
 const run=runInNewContext(`${extract("discardCurrentCandidate","rejectRecording")};discardCurrentCandidate`,{stopDetachedCandidate:()=>{stopped++;return true;}});
 assert.equal(run(recording),false);assert.equal(stopped,1);assert.equal(recording.candidate,undefined);assert.equal(recording.totalBytes,0);
});
