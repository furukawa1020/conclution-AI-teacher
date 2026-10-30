import assert from "node:assert/strict";
import test from "node:test";
import {openGuestTrialCapture} from "../web/guest-trial-controller.mjs";

function environment(overrides = {}) {
  let stopped = 0, closed = 0, disconnected = 0;
  const controller = new AbortController();
  const track = {stop: () => stopped++};
  const stream = {getTracks: () => [track]};
  const port = {postMessage: () => {}};
  const context = {sampleRate: 48000, resume: async () => {}, close: async () => {closed++;},
    createMediaStreamSource: () => ({connect: () => {}, disconnect: () => disconnected++})};
  const options = {getUserMedia: async () => stream, createContext: () => context,
    loadWorklet: async () => ({}), createNode: () => ({port, disconnect: () => disconnected++}),
    signal: controller.signal, generation: 1, ...overrides};
  return {options, stream, port, controller, counts: () => ({stopped, closed, disconnected})};
}

test("completed explicit capture stops tracks and closes its private audio context", async () => {
  const env = environment(); const capture = await openGuestTrialCapture(env.options);
  const pcm = new Uint8Array(640).fill(1);
  env.port.onmessage({data: {type: "frame", version: 1, generation: 1, sequence: 0, contextFrame: 0, pcm: pcm.buffer}});
  const completed = capture.finish();
  env.port.onmessage({data: {type: "sealed", version: 1, generation: 1, lastSequence: 0}});
  const audio = await completed;
  assert.equal(audio[0], 1); assert.ok(pcm.every(v => v === 0));
  assert.deepEqual(env.counts(), {stopped: 1, closed: 1, disconnected: 2}); audio.fill(0);
});

test("permission resolving after cancellation cannot leave a microphone open", async () => {
  let grant; const env = environment({getUserMedia: () => new Promise(r => {grant = r;})});
  const pending = openGuestTrialCapture(env.options);
  await Promise.resolve(); env.controller.abort();
  await assert.rejects(pending, /capture_unavailable/u);
  grant(env.stream); await new Promise(r => setImmediate(r));
  assert.equal(env.counts().stopped, 1); assert.equal(env.counts().closed, 1);
});

test("worklet failure closes an already granted microphone", async () => {
  const env = environment({loadWorklet: async () => {throw new Error("worklet unavailable");}});
  await assert.rejects(openGuestTrialCapture(env.options));
  await new Promise(r => setImmediate(r));
  assert.equal(env.counts().stopped, 1); assert.equal(env.counts().closed, 1);
});

test("setup deadline is finite even if permission never resolves", async () => {
  const env = environment({getUserMedia: () => new Promise(() => {}), setupTimeoutMs: 5});
  await assert.rejects(openGuestTrialCapture(env.options), /capture_unavailable/u);
  assert.equal(env.counts().closed, 1);
});

test("invalid PCM immediately releases the microphone and rejects later submission", async () => {
  let interrupted = 0;
  const env = environment({onInterrupted: () => interrupted++});
  const capture = await openGuestTrialCapture(env.options);
  const pcm = new Uint8Array(640).fill(9);
  env.port.onmessage({data: {type: "frame", version: 1, generation: 1,
    sequence: 2, contextFrame: 0, pcm: pcm.buffer}});
  assert.ok(pcm.every(v => v === 0));
  assert.deepEqual(env.counts(), {stopped: 1, closed: 1, disconnected: 2});
  await assert.rejects(capture.finish(), /capture_unavailable/u);
  capture.cancel(); env.controller.abort();
  assert.equal(env.counts().stopped, 1);
  assert.equal(interrupted, 1);
});

test("explicit cancellation does not report an unexpected interruption", async () => {
  let interrupted = 0;
  const env = environment({onInterrupted: () => interrupted++});
  const capture = await openGuestTrialCapture(env.options);
  capture.cancel(); env.controller.abort();
  assert.equal(interrupted, 0);
});

test("a failing device stop does not prevent other devices and context from closing", async () => {
  let removed = 0, stopped = 0;
  const tracks = [
    {stop() { throw new Error("device detached"); }, removeEventListener() { removed++; }},
    {stop() { stopped++; }, removeEventListener() { removed++; }},
  ];
  const env = environment({getUserMedia: async () => ({getTracks: () => tracks})});
  const capture = await openGuestTrialCapture(env.options);
  capture.cancel();
  assert.equal(stopped, 1); assert.equal(removed, 6);
  assert.equal(env.counts().closed, 1);
  await assert.rejects(capture.finish(), /capture_unavailable/u);
});

test("late permission cleanup isolates stop errors too", async () => {
  let grant, stopped = 0;
  const env = environment({getUserMedia: () => new Promise(resolve => { grant = resolve; })});
  const pending = openGuestTrialCapture(env.options);
  await Promise.resolve(); env.controller.abort();
  await assert.rejects(pending, /capture_unavailable/u);
  grant({getTracks: () => [{stop() {throw new Error("detached");}}, {stop() {stopped++;}}]});
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(stopped, 1);
});

test("an ended input cannot enter recording", async () => {
  let stopped = 0;
  const env = environment({getUserMedia: async () => ({getTracks: () => [
    {readyState: "ended", stop() {stopped++;}},
  ]})});
  await assert.rejects(openGuestTrialCapture(env.options), /capture_unavailable/u);
  assert.equal(stopped, 1); assert.equal(env.counts().closed, 1);
});

test("a suspended audio context cannot enter recording even if resume resolves", async () => {
  let closed = 0;
  const env = environment({createContext: () => ({state: "suspended",
    resume: async () => {}, close: async () => {closed++;}})});
  await assert.rejects(openGuestTrialCapture(env.options), /capture_unavailable/u);
  assert.equal(closed, 1); assert.equal(env.counts().stopped, 1);
});

test("OS context interruption releases capture and removes the observer", async () => {
  for (const state of ["suspended", "interrupted", "closed"]) {
    let listener, closed = 0, interrupted = 0;
    const context = {state: "running", sampleRate: 48000, resume: async () => {},
      close: async () => {closed++;},
      createMediaStreamSource: () => ({connect() {}, disconnect() {}}),
      addEventListener: (type, fn) => {assert.equal(type, "statechange"); listener = fn;},
      removeEventListener: (type, fn) => {assert.equal(listener, fn); listener = undefined;},
    };
    const env = environment({createContext: () => context, onInterrupted: () => interrupted++});
    const capture = await openGuestTrialCapture(env.options);
    context.state = state; listener();
    assert.equal(listener, undefined); assert.equal(closed, 1); assert.equal(interrupted, 1);
    await assert.rejects(capture.finish(), /capture_unavailable/u);
    assert.equal(env.counts().stopped, 1);
  }
});

test("device mute has a grace period and brief unmute does not stop quiet capture", async t => {
  t.mock.timers.enable({apis:["setTimeout"]});
  let stopped=0, interrupted=0;
  const listeners=new Map();
  const track={muted:false,stop(){stopped++;},
    addEventListener:(name,fn)=>listeners.set(name,fn), removeEventListener:name=>listeners.delete(name)};
  const env=environment({getUserMedia:async()=>({getTracks:()=>[track]}),onInterrupted:()=>interrupted++});
  const capture=await openGuestTrialCapture(env.options);
  track.muted=true; listeners.get("mute")(); t.mock.timers.tick(500);
  track.muted=false; listeners.get("unmute")(); t.mock.timers.tick(500);
  assert.equal(stopped,0);
  track.muted=true; listeners.get("mute")(); t.mock.timers.tick(999);
  // Keep the transport alive independently of audio amplitude.
  env.port.onmessage({data:{type:"frame",version:1,generation:1,sequence:0,contextFrame:0,pcm:new ArrayBuffer(640)}});
  t.mock.timers.tick(1);
  assert.equal(stopped,1); assert.equal(interrupted,1); assert.equal(listeners.size,0);
  await assert.rejects(capture.finish(),/capture_unavailable/u);
});
