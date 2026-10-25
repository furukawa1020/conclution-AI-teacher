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
