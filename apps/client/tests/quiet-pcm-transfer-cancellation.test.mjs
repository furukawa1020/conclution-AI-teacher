import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import { zeroizeCaptureFrame } from "../web/voice-session-policy.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));

async function fixture(state = "ready") {
  const source = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const extract = (start, end) => {
    const at = source.indexOf(start), until = source.indexOf(end, at);
    assert.ok(at >= 0 && until > at);
    return source.slice(at, until);
  };
  const timers = new Map(), posts = [], wiped = [];
  let nextTimer = 1, disconnects = 0;
  const node = { port: { postMessage: (message) => posts.push(message) }, disconnect: () => disconnects++ };
  const runtime = runInNewContext(`
    let captureStopped = false, captureSealed = true, quietHttpFallbackEligible = true;
    let captureGeneration = 1, quietFallbackTransfer;
    let captureSealResolve, captureSealReject, captureSealTimer, adoptedCapture, captureSource;
    let guestComparisonTurnSerial = 0, guestComparisonTurnFinished = false;
    ${extract("function buildQuietPcmPayload(", "async function startVoiceLiveSession(")}
    ${extract("  function discardCaptureMessage(", "  async function sealCapture(")}
    ${extract("  async function takeQuietHttpFallback(", "  function emitLatency(")}
    const session = { ${extract('    cancel(error = new Error("request_cancelled"))', "    confirmSpeech(")} };
    ({ take: takeQuietHttpFallback, stop: stopCapture, cancel: session.cancel });
  `, {
    ArrayBuffer, Uint8Array, Error, state, captureNode: node,
    zeroizeCaptureFrame: (buffer) => { if (buffer) wiped.push(buffer); zeroizeCaptureFrame(buffer); },
    QUIET_HTTP_FALLBACK_TIMEOUT_MS: 5000, QUIET_HTTP_PCM_FRAME_BYTES: 640,
    QUIET_HTTP_PCM_MAX_FRAMES: 1500, QUIET_HTTP_PCM_MAX_BYTES: 960_000,
    setTimeout: (callback) => { const id = nextTimer++; timers.set(id, callback); return id; },
    clearTimeout: (id) => timers.delete(id),
    clearSlowReplayCandidateTimer: () => {}, clientTransport: { close() {} },
    settleReady: () => {}, settleResult: () => {}, closeSocket: () => {},
  });
  const frame = () => ({
    type: "fallback-frame", version: 1, generation: 1, sequence: 0, frameCount: 1,
    pcm: new Uint8Array(640).fill(1).buffer,
    baselinePcm: new Uint8Array(640).fill(2).buffer,
    weakPcm: new Uint8Array(640).fill(3).buffer,
  });
  const emit = (data) => node.port.onmessage({ data });
  const seal = () => emit({ type: "fallback-sealed", version: 1, generation: 1, lastSequence: 0, totalFrames: 1 });
  return { runtime, timers, posts, wiped, node, frame, emit, seal, disconnects: () => disconnects };
}

function assertWiped(frame) {
  for (const key of ["pcm", "baselinePcm", "weakPcm"]) {
    assert.ok(new Uint8Array(frame[key]).every((byte) => byte === 0), key);
  }
}

for (const state of ["ready", "failed", "cancelled", "complete"]) {
  test(`cancelling ${state} live state immediately rejects a partial quiet transfer and wipes late frames`, async () => {
    const f = await fixture(state);
    let settled;
    const pending = f.runtime.take().then(() => { settled = "resolved"; }, (error) => { settled = error.message; });
    const frame = f.frame();
    f.emit(frame);
    const staleHandler = f.node.port.onmessage;
    f.runtime.cancel(new Error("request_cancelled"));
    await flush();
    assert.equal(settled, "request_cancelled");
    assert.equal(f.timers.size, 0);
    assertWiped(frame);
    const late = f.frame();
    f.emit(late);
    assertWiped(late);
    const queued = f.frame();
    queued.sequence = 1;
    staleHandler({ data: queued });
    assertWiped(queued);
    await pending;
  });
}

test("cancellation after seal but before payload handoff rejects and wipes the assembled payload", async () => {
  const f = await fixture("failed");
  const pending = f.runtime.take();
  void pending.catch(() => {});
  const frame = f.frame();
  f.emit(frame);
  f.seal();
  f.runtime.cancel(new Error("session_expired"));
  await assert.rejects(pending, /session_expired/u);
  assert.equal(f.timers.size, 0);
  const assembled = [...new Set(f.wiped)].filter((buffer) =>
    ![frame.pcm, frame.baselinePcm, frame.weakPcm].includes(buffer));
  assert.equal(assembled.length, 3);
  for (const buffer of assembled) {
    assert.ok(new Uint8Array(buffer).every((byte) => byte === 0));
  }
});

test("a successfully handed-off payload remains intact when the terminal live owner is cancelled", async () => {
  const f = await fixture("failed");
  const pending = f.runtime.take();
  const frame = f.frame();
  f.emit(frame);
  f.seal();
  const result = await pending;
  f.runtime.cancel(new Error("request_cancelled"));
  assert.deepEqual([new Uint8Array(result.enhanced)[0], new Uint8Array(result.baseline)[0], new Uint8Array(result.weak)[0]], [1, 2, 3]);
  assertWiped(frame);
  assert.equal(f.timers.size, 0);
  assert.equal(await f.runtime.take(), undefined);
});

test("a stale transfer handler cannot retain fresh PCM after successful settlement", async () => {
  const f = await fixture();
  const pending = f.runtime.take();
  const staleHandler = f.node.port.onmessage;
  f.emit(f.frame());
  f.seal();
  await pending;
  const late = f.frame();
  late.sequence = 1;
  staleHandler({ data: late });
  assertWiped(late);
});

test("Native failure preserves eligible sealed PCM until its HTTP owner consumes it", async () => {
  const f = await fixture("failed");
  f.runtime.stop(new Error("voice_api_unavailable"), true);
  assert.equal(f.disconnects(), 0);
  const pending = f.runtime.take();
  f.emit(f.frame());
  f.seal();
  const result = await pending;
  assert.equal(new Uint8Array(result.enhanced)[0], 1);
  assert.equal(f.disconnects(), 1);
});

test("a duplicate take cannot replace the pending transfer cancellation owner", async () => {
  const f = await fixture("failed");
  const pending = f.runtime.take();
  void pending.catch(() => {});
  await assert.rejects(f.runtime.take(), /voice_api_unavailable/u);
  f.runtime.cancel(new Error("request_cancelled"));
  await assert.rejects(pending, /request_cancelled/u);
  assert.equal(f.timers.size, 0);
});

test("the bounded worklet transfer timeout wipes partial views and keeps its original error", async () => {
  const f = await fixture("failed");
  const pending = f.runtime.take();
  void pending.catch(() => {});
  const frame = f.frame();
  f.emit(frame);
  [...f.timers.values()][0]();
  await assert.rejects(pending, /voice_api_unavailable/u);
  assertWiped(frame);
  assert.equal(f.timers.size, 0);
});
