import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";

import {
  createTurnGate,
  initializeWithCleanup,
  isValidTurnMode,
} from "../../web/voice-session-policy.mjs";

export function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, reject, resolve };
}

export function flushStartupTasks() {
  return new Promise((resolve) => setImmediate(resolve));
}

// Execute the production orchestration, replacing only its browser/provider
// dependencies. Each external boundary stays pending until the test releases it.
export async function createVoiceStartupHarness({ guest = false } = {}) {
  const bridge = await readFile(
    new URL("../../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function beginTurn(");
  const end = bridge.indexOf("async function waitForTurnEnd(", start);
  assert.ok(start >= 0 && end > start);
  const credentials = deferred();
  const microphone = deferred();
  const audioGraph = deferred();
  const providerReady = deferred();
  const memory = deferred();
  const events = [];
  const stream = { enabled: true };
  const credentialValue = Object.freeze({
    appCheckToken: "fixture-app-check",
    idToken: "fixture-id-token",
  });
  const liveSession = {
    isLiveReady: () => true,
    cancel: () => { events.push("live-cancel"); },
  };
  const scope = {
    document: { hidden: false },
    nativePreflightInvalidation: { hasActive: () => false },
    SESSION_STATE_MAX_CHARS: 100_000,
    isValidTurnMode,
    activeRecording: undefined,
    activeLiveSession: undefined,
    preparingLiveSession: undefined,
    pendingLiveSession: undefined,
    pendingDocument: undefined,
    beginGate: createTurnGate(),
    finishGate: createTurnGate(),
    passkeyGate: createTurnGate(),
    performance: { now: () => 100 },
    dispatchVoicePrepareSloClear: () => {},
    beginCurrentVoicePrepareSlo: () => 1,
    completeCurrentVoicePrepareSlo: () => {},
    cancelCurrentVoicePrepareSlo: () => {},
    VOICE_PREPARE_SLO_ROUTES: { NATIVE_READY: "native-ready", HTTP_FALLBACK: "http-fallback" },
    VOICE_PREPARE_SLO_RESULTS: { READY: "ready", FALLBACK: "fallback" },
    sessionClock: { begin: () => ({ ok: true }), reset: () => {} },
    warmVoiceService: () => { events.push("warmup"); },
    dispatchVoiceStartLatency: () => {},
    sessionEpoch: 1,
    guestModeActive: guest,
    guestVoiceComparisonRequested: false,
    sessionExpiryWatchdog: { arm: () => true, disarm: () => {} },
    ensureActiveSession: () => true,
    stoppedSessionCode: () => "request_cancelled",
    initializeWithCleanup,
    secureCredentials: async (interactive) => {
      assert.equal(interactive, true);
      events.push("credentials-start");
      await credentials.promise;
      events.push("credentials-ready");
      return credentialValue;
    },
    ensureMediaStream: async (epoch, allowAcquisition) => {
      assert.equal(epoch, 1);
      assert.equal(allowAcquisition, true);
      events.push("microphone-start");
      await microphone.promise;
      events.push("microphone-ready");
      return stream;
    },
    setStreamTracksEnabled: (candidate, enabled) => {
      assert.equal(candidate, stream);
      stream.enabled = enabled;
      events.push(enabled ? "unmute" : "mute");
    },
    ensureAudioGraph: async (candidate, epoch) => {
      assert.equal(candidate, stream);
      assert.equal(epoch, 1);
      assert.equal(stream.enabled, false);
      events.push("graph-start");
      await audioGraph.promise;
      events.push("graph-ready");
    },
    longMemorySession: {
      start: (input) => {
        assert.equal(input.idToken, credentialValue.idToken);
        assert.equal(input.appCheckToken, credentialValue.appCheckToken);
        assert.equal(input.guest, guest);
        assert.equal(input.voiceGeneration, 1);
        assert.equal(input.isStillCurrent(), true);
        events.push("memory-start");
        return memory.promise;
      },
      voiceBinding: () => undefined,
    },
    LONG_MEMORY_CONTEXT_BEGIN_ENDPOINT: "https://fixture.invalid/begin",
    LONG_MEMORY_CONTEXT_CONSUME_ENDPOINT: "https://fixture.invalid/consume",
    startVoiceLiveSession: async (input) => {
      assert.equal(input.idToken, credentialValue.idToken);
      assert.equal(input.appCheckToken, credentialValue.appCheckToken);
      assert.equal(input.stream, stream);
      assert.equal(input.expectedEpoch, 1);
      assert.equal(stream.enabled, false);
      assert.ok(events.includes("credentials-ready"));
      assert.ok(events.includes("graph-ready"));
      events.push("live-start");
      await providerReady.promise;
      events.push("live-ready");
      return liveSession;
    },
    createRecording: (candidate) => {
      assert.equal(candidate, stream);
      assert.equal(stream.enabled, true);
      assert.ok(events.includes("live-ready"));
      events.push("recording");
      return {};
    },
    releaseMicrophone: () => {
      stream.enabled = false;
      events.push("microphone-release");
    },
    reportVoiceFailure: () => { events.push("failure"); },
    fail: (code) => { throw new Error(code); },
    stopSession: () => { throw new Error("unexpected_stop"); },
  };
  const beginTurn = runInNewContext(`${bridge.slice(start, end)}\nbeginTurn`, scope);
  return {
    audioGraph, credentials, events, memory, microphone, providerReady, stream,
    start: () => beginTurn("", "intentional", false, false),
  };
}
