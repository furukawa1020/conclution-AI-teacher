import assert from "node:assert/strict";
import test from "node:test";
import { createGuestTrialController, validateGuestTrial } from "../web/guest-trial-controller.mjs";

const NOW = Date.parse("2026-10-19T00:00:00Z");
test("local expiry discards capability and audio before encoding or transport", async () => {
  let clock = NOW, controls = 0, voices = 0;
  const c = createGuestTrialController({
    send: async () => { controls++; return trial({state:"v2.sealed"}); },
    sendVoice: async () => { voices++; throw new Error("must not send"); },
    discardPrevious: async () => {}, now: () => clock,
  });
  await c.begin();
  clock += 15 * 60 * 1000;
  const pcm = new Uint8Array([1, 2]);
  await assert.rejects(c.submit(pcm), /guest_trial_expired/u);
  assert.deepEqual([...pcm], [0, 0]);
  assert.deepEqual(c.state(), {phase:"expired", trial:null});
  for (const action of ["read", "help", "stop", "begin"]) {
    await assert.rejects(c[action](), /guest_trial_expired/u);
  }
  assert.equal(controls, 1); assert.equal(voices, 0);
  c.reset(); assert.equal(c.state().phase, "idle");
  clock = NOW; await c.begin(); assert.equal(controls, 2);
});

test("reading idle state also releases expired capabilities", async () => {
  let clock = NOW;
  const c = createGuestTrialController({send:async()=>trial({state:"v2.sealed"}),
    discardPrevious:async()=>{}, now:()=>clock});
  await c.begin(); clock += 15 * 60 * 1000;
  assert.deepEqual(c.state(), {phase:"expired", trial:null});
});
function trial(overrides = {}) {
  return { version: 1, id: "a".repeat(64), questionId: "rest_activity_v1",
    question: "少し休めるとしたら、何をしたいですか？", generation: 1, assisted: false,
    status: "pending", expiresAt: "2026-10-19T00:15:00Z", ...overrides };
}
function make(send, discardPrevious = async () => {}) {
  return createGuestTrialController({ send, discardPrevious, now: () => NOW, timeoutMs: 100 });
}

function voiceController(sendVoice, send = async ({action}) => action === "stop"
  ? trial({status:"stopped",generation:2}) : trial({state:"v2.sealed"})) {
  return createGuestTrialController({send,sendVoice,discardPrevious:async()=>{},now:()=>NOW});
}

test("voice completion ends the same trial and clears consumed PCM",async()=>{
  const c=voiceController(async ({trial:previous})=>{
    assert.equal(previous.id,"a".repeat(64));
    return trial({status:"answer_first",generation:2});
  });
  await c.begin();const audio=new Uint8Array([1,2]);await c.submit(audio);
  assert.equal(c.state().phase,"ended");assert.ok(audio.every(x=>x===0));
  await assert.rejects(c.submit(new Uint8Array([1,2])),/reconcile/u);
});

test("stop cancels pending voice and a late answer cannot overwrite stopped state",async()=>{
  let release;let signal;
  const c=voiceController((_,opts)=>{signal=opts.signal;return new Promise(resolve=>{release=resolve;});});
  await c.begin();const audio=new Uint8Array([1,2]);const pending=c.submit(audio);
  const rejected=assert.rejects(pending,/cancelled/u);
  await assert.rejects(c.submit(new Uint8Array([1,2])),/busy/u);
  await c.stop();assert.equal(signal.aborted,true);
  release(trial({status:"answer_first",generation:2}));await rejected;
  assert.equal(c.state().trial.status,"stopped");assert.ok(audio.every(x=>x===0));
});

test("ambiguous voice failure is reconciled without automatically replaying audio",async()=>{
  let calls=0;
  const c=voiceController(async()=>{calls++;throw new Error("guest_trial_unavailable");},async ({action})=>
    action==="read"?trial({status:"answer_first",generation:2}):trial({state:"v2.sealed"}));
  await c.begin();await assert.rejects(c.submit(new Uint8Array([1,2])),/unavailable/u);
  assert.equal(c.state().phase,"uncertain");
  await assert.rejects(c.submit(new Uint8Array([1,2])),/reconcile/u);
  await c.read();assert.equal(c.state().trial.status,"answer_first");assert.equal(calls,1);
});

test("definite ASR rejection permits fresh recording without read or automatic replay", async () => {
  let voiceCalls = 0, controls = 0;
  const c = voiceController(async () => {
    voiceCalls++; throw new Error("guest_trial_not_recognized");
  }, async () => {controls++; return trial({state:"v2.sealed"});});
  await c.begin();
  for (let i = 0; i < 2; i++) {
    const pcm = new Uint8Array([1,2]);
    await assert.rejects(c.submit(pcm), /not_recognized/u);
    assert.ok(pcm.every(value => value === 0));
    assert.equal(c.state().phase, "active");
    assert.equal(c.state().trial.generation, 1);
    assert.equal(voiceCalls, i + 1);
  }
  assert.equal(controls, 1);
});

test("reset cancels late voice without changing a new trial",async()=>{
  let release;let begins=0;
  const c=voiceController(()=>new Promise(resolve=>{release=resolve;}),async()=>
    trial({id:(++begins===1?"a":"b").repeat(64),state:"v2.sealed"}));
  await c.begin();const pending=c.submit(new Uint8Array([1,2]));
  const rejected=assert.rejects(pending,/cancelled/u);c.reset();await c.begin();
  release(trial({status:"answer_first",generation:2}));await rejected;
  assert.equal(c.state().trial.id,"b".repeat(64));assert.equal(c.state().phase,"active");
});

test("pending control can carry a bounded voice capability but terminal control cannot", () => {
  const state = "v2." + "a".repeat(3000);
  assert.equal(validateGuestTrial(trial({state}), NOW).state, state);
  for (const state of [null, "", "v1.foo", "v2.a\n", "v2." + "a".repeat(16384), 123]) {
    assert.throws(() => validateGuestTrial(trial({state}), NOW), /invalid/u);
  }
  assert.throws(() => validateGuestTrial(trial({state, status:"stopped", generation:2}), NOW), /invalid/u);
});

test("trial schema rejects altered questions, extra content, expiry and inconsistent generations", () => {
  assert.ok(Object.isFrozen(validateGuestTrial(trial(), NOW)));
  for (const bad of [trial({question: "別の質問"}), trial({generation: 2}), trial({caption: "私の答え"}),
    trial({expiresAt: "2026-10-19T00:00:00Z"}), trial({expiresAt: "2026-10-20T00:00:00Z"}),
    trial({id: "../other"}), trial({assisted: "false"}), trial({status: "improved"})]) {
    assert.throws(() => validateGuestTrial(bad, NOW), /guest_trial_invalid/u);
  }
});

test("previous audio disposal finishes before begin; help and stop retain provenance", async () => {
  const order = [];
  const c = make(async payload => {
    order.push(payload.action);
    if (payload.action === "begin") return trial();
    if (payload.action === "help") return trial({assisted: true, generation: 2});
    return trial({assisted: true, generation: 3, status: "stopped"});
  }, async () => { order.push("disposed"); });
  await c.begin(); await c.help(); await c.stop();
  assert.deepEqual(order, ["disposed", "begin", "help", "stop"]);
  assert.equal(c.state().phase, "ended");
  assert.equal(c.state().trial.assisted, true);
  await assert.rejects(c.help(), /reconcile/u);
});

test("lost help response requires reconciliation and cannot restore an unassisted state", async () => {
  const c = make(async ({action}) => {
    if (action === "begin") return trial();
    if (action === "help") throw new Error("network");
    return trial({assisted: true, generation: 2});
  });
  await c.begin(); await assert.rejects(c.help(), /network/u);
  assert.equal(c.state().phase, "uncertain");
  await assert.rejects(c.stop(), /reconcile/u);
  await c.read(); assert.equal(c.state().trial.assisted, true);
});

test("reset invalidates late responses without disturbing the replacement request", async () => {
  let late;
  let calls = 0;
  const c = make(() => ++calls === 1 ? new Promise(resolve => {late = resolve;}) : trial({id:"b".repeat(64)}));
  const first = c.begin();
  while (!late) await Promise.resolve();
  await assert.rejects(c.begin(), /busy/u);
  c.reset();
  const failure = assert.rejects(first, /cancelled/u);
  await c.begin(); late(trial()); await failure;
  assert.equal(c.state().trial.id, "b".repeat(64));
});

test("replacement begin waits for non-cooperative old cleanup", async () => {
  let release;
  let cleanupCalls = 0;
  let sent = 0;
  const c = make(() => { sent++; return trial(); }, () => ++cleanupCalls === 1 ? new Promise(resolve => {release = resolve;}) : undefined);
  const first = c.begin();
  while (!release) await Promise.resolve();
  c.reset();
  const rejected = assert.rejects(first, /cancelled/u);
  const second = c.begin();
  await Promise.resolve(); await Promise.resolve();
  assert.equal(sent, 0);
  release(); await rejected; await second;
  assert.equal(sent, 1);
});

test("failed disposal never sends begin; non-cooperative transport times out", async () => {
  let sent = 0;
  const c = make(() => { sent++; return trial(); }, async () => {throw new Error("cleanup");});
  await assert.rejects(c.begin(), /cleanup/u); assert.equal(sent, 0);
  const stuck = createGuestTrialController({send: () => new Promise(() => {}), discardPrevious: async () => {}, now: () => NOW, timeoutMs: 5});
  await assert.rejects(stuck.begin(), /cancelled/u);
  assert.equal(stuck.state().phase, "uncertain");
});

test("read cannot relabel the question or roll back assistance", async () => {
  let reply = trial();
  const c = make(async () => reply);
  await c.begin(); reply = trial({assisted:true,generation:2}); await c.help();
  reply = trial(); await assert.rejects(c.read(), /invalid/u);
  assert.equal(c.state().trial.assisted, true);
});
