import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { runInNewContext } from "node:vm";
import {
  createCaptureBuffer, createStopLatch, createTurnGate, isValidTurnMode,
  sameTurnCredentials, zeroizeCaptureFrame,
} from "../web/voice-session-policy.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, decline) => { resolve = accept; reject = decline; });
  void promise.catch(() => {});
  return { promise, resolve, reject };
}

async function fixture({ quiet = true, stopThrows = false, hasSpeech = true, credentials = true } = {}) {
  const source = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  function extract(start, end) {
    const at = source.indexOf(start), until = source.indexOf(end, at);
    assert.ok(at >= 0 && until > at);
    return source.slice(at, until);
  }
  const events = [], posts = [], tracks = [], recorders = [], timers = new Map();
  const response = deferred(), auth = deferred(), fallback = deferred();
  const pcm = {
    baseline: new Uint8Array([1, 0, 2, 0]).buffer,
    enhanced: new Uint8Array([3, 0, 4, 0]).buffer,
    weak: new Uint8Array([2, 0, 3, 0]).buffer,
  };
  fallback.resolve(quiet ? pcm : undefined);
  let nextTimer = 1;
  class Recorder {
    constructor() { this.state = "inactive"; this.mimeType = "audio/webm"; this.listeners = new Map(); recorders.push(this); }
    addEventListener(type, callback) { this.listeners.set(type, callback); }
    emit(type, value = {}) { this.listeners.get(type)?.(value); }
    start() { this.state = "recording"; }
    stop() {
      events.push("recorder-stop-requested");
      if (stopThrows) throw new Error("recorder_stop_failed");
      this.state = "inactive";
      // The browser's final dataavailable/stop tasks deliberately stay pending.
    }
  }
  const context = {
    ArrayBuffer, Uint8Array, Float32Array, Blob, Error, AbortController,
    MediaRecorder: Recorder, createCaptureBuffer, createStopLatch, createTurnGate,
    isValidTurnMode, sameTurnCredentials, zeroizeCaptureFrame,
    setTimeout: (callback) => { const id = nextTimer++; timers.set(id, callback); return id; },
    clearTimeout: (id) => timers.delete(id), clearInterval: () => {},
    performance: { now: () => 1000 },
    activeRecording: undefined, activePlayback: undefined, activeLiveSession: undefined,
    activeRequestController: undefined, pendingLiveSession: undefined, pendingDocument: undefined,
    sessionEpoch: 1, audioContext: { state: "running" },
    AUDIO_MAX_BYTES: 1_000_000, SESSION_STATE_MAX_CHARS: 16_384,
    VOICE_TURN_CLIENT_TIMEOUT_MS: 60_000, VOICE_START_SLO_BUDGETS: { stalledMs: 10_000 },
    finishGate: createTurnGate(), recorderOptions: () => ({}),
    beginSessionResponse: () => true, completeSessionResponse: () => true,
    cancelSessionResponse: () => events.push("response-cancelled"),
    stoppedSessionCode: () => "request_cancelled",
    clearPendingDocument: () => {}, takePendingLiveSession: async () => undefined,
    retirePendingLiveSession: () => {},
    setTracksEnabled: (enabled) => tracks.push({ stream: "shared", enabled }),
    setStreamTracksEnabled: (stream, enabled) => tracks.push({ stream, enabled }),
    shouldDiscardInterruptedPlaybackRecording: () => false,
    discardInterruptedPlaybackRecording: () => {},
    shouldAbortPlaybackTransportOnInterrupt: () => false,
    secureCredentials: () => { events.push("auth"); return auth.promise; },
    arrayBufferToBase64: (buffer) => Buffer.from(buffer).toString("base64"),
    VOICE_ENDPOINT: "https://fixture.invalid/voice",
    fetch: (_url, options) => { posts.push({ payload: JSON.parse(options.body), signal: options.signal }); return response.promise; },
    reportVoiceFailure: (phase, error) => events.push(`failure:${phase}:${error.message}`),
    fail: (code) => { throw new Error(code); },
    createStreamingPlayback: (_epoch, _native, transport, _coach, _ended, _strict, firstAudible) => {
      const playback = {
        transportKind: transport, interrupted: false,
        hasStreamedAudio: () => true, markLatencyCommitSent: () => {},
        markLatencyCommitAcknowledged: () => {}, armResponseInterruption: () => {},
        firstAudible,
      };
      context.activePlayback = playback;
      return playback;
    },
    haltStreamingPlayback: (playback) => {
      if (context.activePlayback === playback) context.activePlayback = undefined;
    },
    consumeVoiceStream: async (_response, playback) => {
      playback.firstAudible(1000);
      return { streamedAudio: true, finalResult: { state: "answered" } };
    },
    awaitValidatedPlaybackCompletion: async () => {},
  };
  const runtime = runInNewContext(`
${extract("function stopVad(", "function currentAudioContextFrame(")}
${extract("function startCandidateRecorder(", "function confirmLiveSpeech(")}
${extract("function createRecordingState(", "function createRecording(")}
${extract("async function finishTurn(", "function safeDocumentName(")}
({ finishTurn, createRecordingState, startCandidateRecorder, rejectRecording, resolveRecording })`, context);
  const tokens = Object.freeze({ appCheckToken: "fixture-app-check", idToken: "fixture-id-token" });
  const recording = runtime.createRecordingState("old-stream", true, false, "kms1.fixture", undefined, credentials ? tokens : undefined);
  recording.lastVoiceAt = 900;
  context.activeRecording = recording;
  runtime.startCandidateRecorder(recording, true, 0, undefined, undefined);
  if (!hasSpeech) {
    // Supply the actual negative endpoint without invoking candidate-deadline policy.
    recording.candidate.confirmed = false;
  }
  const originalCandidate = recording.candidate;
  if (hasSpeech) recorders[0].emit("dataavailable", { data: new Blob([new Uint8Array([9, 8])]) });
  context.activeLiveSession = {
    nativeAudio: true, matches: () => true,
    commit: async () => { throw new Error("voice_api_unavailable"); },
    canFallback: () => true, requiresStatefulHTTPFallback: () => false,
    takeHttpFallback: () => fallback.promise,
    cancel: () => events.push("live-cancel"),
  };
  const start = () => {
    const pending = runtime.finishTurn("opaque-state", "foreground", false);
    void pending.catch(() => {});
    return pending;
  };
  return { context, runtime, recording, originalCandidate, recorder: recorders[0], pcm, events, posts, tracks, timers, response, auth, fallback, start };
}

function assertCleared(pcm) {
  for (const value of Object.values(pcm)) {
    if (value instanceof ArrayBuffer) assert.ok(new Uint8Array(value).every((byte) => byte === 0));
  }
}

test("verified quiet PCM sends and completes while the browser's final Recorder event is withheld", async () => {
  const f = await fixture();
  const pending = f.start();
  await flush();
  assert.equal(f.posts.length, 1, "complete PCM must not wait for the unused Blob");
  assert.equal(f.recording.settled, true);
  assert.equal(f.recording.candidate, undefined);
  assert.equal(f.originalCandidate.discarded, true);
  assert.equal(f.originalCandidate.captureBuffer.take().totalBytes, 0);
  assert.equal(f.posts[0].payload.mimeType, "audio/l16");
  assert.equal(f.posts[0].payload.sessionContext, "kms1.fixture");
  assert.equal(f.posts[0].payload.sessionState, "opaque-state");
  assert.deepEqual([
    f.posts[0].payload.baselineAudioBase64, f.posts[0].payload.audioBase64, f.posts[0].payload.weakAudioBase64,
  ], ["AQACAA==", "AwAEAA==", "AgADAA=="]);
  assertCleared(f.pcm);
  const trackCount = f.tracks.length;
  f.recorder.emit("error");
  f.recorder.emit("dataavailable", { data: new Blob([new Uint8Array([7, 7])]) });
  f.recorder.emit("stop");
  assert.equal(f.posts[0].signal.aborted, false);
  assert.equal(f.tracks.length, trackCount);
  assert.equal(f.recording.sessionContext, "kms1.fixture");
  f.response.resolve({ ok: true });
  assert.equal((await pending).state, "answered");
  f.context.activeRecording = { stream: "new-stream" };
  f.context.activePlayback = undefined;
  f.recorder.emit("error");
  f.recorder.emit("stop");
  assert.equal(f.tracks.length, trackCount);
  assert.equal(f.timers.size, 0);
});

test("ordinary Blob fallback still waits for the complete recorder and retains its validation", async () => {
  const f = await fixture({ quiet: false });
  const pending = f.start();
  await flush();
  assert.equal(f.posts.length, 0);
  f.recorder.emit("stop");
  await flush();
  assert.equal(f.posts.length, 1);
  assert.equal(f.posts[0].payload.mimeType, "audio/webm");
  assert.equal(Object.hasOwn(f.posts[0].payload, "baselineAudioBase64"), false);
  f.response.resolve({ ok: true });
  assert.equal((await pending).state, "answered");
});

test("malformed quiet PCM fails before Blob wait and clears every owned view", async () => {
  const f = await fixture();
  f.pcm.weak = new Uint8Array([5, 0]).buffer;
  const pending = f.start();
  await flush();
  assert.ok(f.events.some((event) => event.includes("voice_api_unavailable")));
  await assert.rejects(pending, /voice_api_unavailable/u);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("authentication failure clears ready PCM without reviving the retired Recorder", async () => {
  const f = await fixture({ credentials: false });
  const pending = f.start();
  await flush();
  assert.ok(f.events.includes("auth"));
  f.auth.reject(new Error("authentication_failed"));
  await assert.rejects(pending, /authentication_failed/u);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("a Recorder stop failure cannot resurrect its positive endpoint through quiet PCM", async () => {
  const f = await fixture({ stopThrows: true });
  const pending = f.start();
  await flush();
  assert.equal(f.recording.discard, true);
  assert.equal(f.posts.length, 0);
  await assert.rejects(pending, /voice_turn_invalid/u);
  assertCleared(f.pcm);
});

test("an epoch cancelled while taking PCM cannot upload or mute the new microphone owner", async () => {
  const f = await fixture();
  const gate = deferred();
  f.context.activeLiveSession.takeHttpFallback = () => gate.promise;
  const pending = f.start();
  await flush();
  f.context.sessionEpoch += 1;
  f.context.activeRecording = { stream: "new-stream" };
  const trackCount = f.tracks.length;
  gate.resolve(f.pcm);
  await assert.rejects(pending, /request_cancelled/u);
  f.recorder.emit("stop");
  assert.equal(f.tracks.length, trackCount);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("the existing speech-end deadline still cancels pending auth and clears PCM", async () => {
  const f = await fixture({ credentials: false });
  const pending = f.start();
  await flush();
  assert.ok(f.events.includes("auth"));
  const callbacks = [...f.timers.values()];
  assert.ok(callbacks.length > 0);
  // The master speech-end timer is registered before any network wait timer.
  callbacks[0]();
  await assert.rejects(pending, /voice_turn_timeout/u);
  assertCleared(f.pcm);
  f.auth.resolve(Object.freeze({ appCheckToken: "late-app", idToken: "late-id" }));
  await flush();
  assert.equal(f.posts.length, 0);
});

for (const arrival of ["after-timeout", "same-turn-as-timeout", "owned-before-timeout"]) {
  test(`the speech-end deadline cancels a pending quiet transfer and wipes PCM arriving ${arrival}`, async () => {
    const f = await fixture();
    const gate = deferred();
    f.context.activeLiveSession.takeHttpFallback = () => gate.promise;
    let failure;
    const pending = f.start().catch((error) => { failure = error.message; });
    await flush();
    if (arrival !== "after-timeout") gate.resolve(f.pcm);
    if (arrival === "owned-before-timeout") await Promise.resolve();
    const [timer, callback] = [...f.timers.entries()][0];
    f.timers.delete(timer);
    callback();
    await flush();
    assert.equal(failure, "voice_turn_timeout");
    assert.ok(f.events.includes("live-cancel"));
    if (arrival === "after-timeout") gate.resolve(f.pcm);
    await flush();
    await pending;
    assertCleared(f.pcm);
    assert.equal(f.posts.length, 0);
    assert.equal(f.timers.size, 0);
  });
}

test("credentials arriving after an epoch change cannot upload the old PCM or revoke the new owner", async () => {
  const f = await fixture({ credentials: false });
  const pending = f.start();
  await flush();
  assert.ok(f.events.includes("auth"));
  const newOwner = { stream: "next-turn-stream" };
  f.context.sessionEpoch += 1;
  f.context.activeRecording = newOwner;
  const trackCount = f.tracks.length;
  f.auth.resolve(Object.freeze({ appCheckToken: "late-app", idToken: "late-id" }));
  await assert.rejects(pending, /request_cancelled/u);
  f.recorder.emit("error");
  f.recorder.emit("stop");
  assert.equal(f.context.activeRecording, newOwner);
  assert.equal(f.tracks.length, trackCount);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("failure to stop the retired Recorder is not swallowed", async () => {
  const f = await fixture();
  f.context.activeLiveSession.takeHttpFallback = async () => {
    f.recorder.state = "recording";
    f.recorder.stop = () => { throw new Error("secondary_stop_failed"); };
    return f.pcm;
  };
  await assert.rejects(f.start(), /voice_turn_invalid/u);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("encoding failure clears all three PCM views, including those not encoded yet", async () => {
  const f = await fixture();
  let encoded = 0;
  f.context.arrayBufferToBase64 = () => {
    encoded++;
    if (encoded === 2) throw new Error("encoding_failed");
    return "valid-first-view";
  };
  await assert.rejects(f.start(), /encoding_failed/u);
  assert.equal(encoded, 2);
  assert.equal(f.posts.length, 0);
  assertCleared(f.pcm);
});

test("PCM availability never bypasses the positive endpoint requirement", async () => {
  const f = await fixture({ hasSpeech: false });
  let takeCalls = 0;
  f.context.activeLiveSession.takeHttpFallback = async () => { takeCalls++; return f.pcm; };
  await assert.rejects(f.start(), /no_speech/u);
  assert.equal(takeCalls, 0);
  assert.equal(f.posts.length, 0);
});

test("ordinary Blob fallback still rejects incomplete, oversized or empty payloads", async (t) => {
  for (const mode of ["incomplete", "oversized", "empty"]) {
    await t.test(mode, async () => {
      const f = await fixture({ quiet: false });
      const pending = f.start();
      await flush();
      if (mode === "incomplete") f.recording.fallbackAudioComplete = false;
      if (mode === "empty") f.originalCandidate.captureBuffer.clear();
      if (mode === "oversized") {
        f.originalCandidate.captureBuffer = {
          take: () => ({ chunks: [new Blob([new Uint8Array(1_000_002)])], totalBytes: 1_000_002 }),
        };
      }
      f.recorder.emit("stop");
      await assert.rejects(pending, mode === "empty" ? /no_speech/u : /voice_turn_too_large/u);
      assert.equal(f.posts.length, 0);
    });
  }
});
