import assert from "node:assert/strict";
import test from "node:test";
import {createNativePreflightInvalidationGate as createGate,
  NATIVE_PREFLIGHT_RETIRE_REASONS as R} from "../web/native-preflight-lease-policy.mjs";

test("unknown and released preflight owners cannot retire an empty gate", () => {
 const gate=createGate();
 for(const value of [undefined,null,{},Object.freeze({generation:1})]) {
  assert.equal(gate.release(value),false);
  assert.equal(gate.retire(value,R.CANCELLED),false);
 }
 const owner=gate.begin(1,()=>{});
 assert.equal(gate.release(owner),true);
 assert.equal(gate.release(owner),false);
 assert.equal(gate.retire(owner,R.CANCELLED),false);
});

test("replacement cleanup reentry keeps the newest preflight owner", () => {
 const gate=createGate(), cancelled=[]; let newest;
 gate.begin(1,()=>{newest=gate.begin(3,reason=>cancelled.push([3,reason]));});
 const displaced=gate.begin(2,reason=>cancelled.push([2,reason]));
 assert.deepEqual(cancelled,[[2,R.REPLACED]]);
 assert.equal(gate.retire(displaced,R.CANCELLED),false);
 assert.equal(gate.retire(newest,R.PAGE_HIDDEN),true);
 assert.deepEqual(cancelled,[[2,R.REPLACED],[3,R.PAGE_HIDDEN]]);
 assert.equal(gate.hasActive(),false);
});

test("failed replacement cleanup cannot strand an unreachable owner",()=>{
 const gate=createGate(); let cancelled=0;
 gate.begin(1,()=>{throw new Error("device cleanup");});
 assert.throws(()=>gate.begin(2,()=>{cancelled++;}),/device cleanup/u);
 assert.equal(cancelled,1); assert.equal(gate.hasActive(),false);
});

test("cleanup failure after reentry cannot retire the newer connection",()=>{
 const gate=createGate(); let newest, cancelled=0;
 gate.begin(1,()=>{
  newest=gate.begin(3,()=>{cancelled++;});
  throw new Error("old cleanup");
 });
 assert.throws(()=>gate.begin(2,()=>{}),/old cleanup/u);
 assert.equal(cancelled,0);
 assert.equal(gate.release(newest),true);
});
