import assert from "node:assert/strict";
import test from "node:test";
import {readFileSync} from "node:fs";
const bridge=readFileSync(new URL("../web/firebase-bridge.js",import.meta.url),"utf8");
const start=bridge.indexOf("function haltStreamingPlayback(");
const end=bridge.indexOf("async function awaitValidatedPlaybackCompletion(",start);
assert.ok(start>=0 && end>start);
const source=bridge.slice(start,end);

test("playback stop continues after every independent cleanup throws",()=>{
 const calls=[];
 const fail=name=>()=>{calls.push(name);throw new Error("device");};
 const playback={reject:fail("reject"),stopWorklet:fail("worklet"),
  sources:new Set([0,1,2].map(i=>({stop:fail(`stop${i}`),disconnect:fail(`disconnect${i}`)}))),
  gainNode:{disconnect:fail("gain")}};
 const run=new Function("playback","monitor",`let activePlayback=playback;
 const stopBargeInMonitoring=monitor;
 ${source}
 haltStreamingPlayback(playback,new Error("cancelled"));return activePlayback;`);
 assert.equal(run(playback,fail("monitor")),undefined);
 assert.deepEqual(calls,["reject","monitor","worklet","stop0","disconnect0","stop1","disconnect1","stop2","disconnect2","gain"]);
 assert.equal(playback.sources.size,0);
});

test("cleanup reentry preserves a replacement playback owner",()=>{
 const replacement={};
 const run=new Function("replacement",`let activePlayback;
 const stopBargeInMonitoring=()=>{};
 ${source}
 const old={reject(){activePlayback=replacement;},sources:new Set()};
 activePlayback=old;haltStreamingPlayback(old,new Error("cancelled"));return activePlayback;`);
 assert.equal(run(replacement),replacement);
});
