import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  advanceCandidateCapture,
  advanceVad,
  createCaptureBuffer,
  createCandidateCaptureState,
  createRetryableInitializer,
  createSessionClock,
  createStopLatch,
  createTurnGate,
  createVadState,
  initializeWithCleanup,
  isPendingDocumentExpired,
  isValidTurnMode,
  normalizeResearchDiscovery,
  shouldStopSessionForLifecycle,
  turnModeForGestureEpoch,
  VOICE_SESSION_LIMITS,
} from "../web/voice-session-policy.mjs";
import {
  createVoiceStreamParser,
  VOICE_STREAM_LIMITS,
} from "../web/voice-stream-policy.mjs";

const researchRecord = Object.freeze({
  title: "A-first responses under working-memory load",
  doi: "10.1234/kotae.2026.1",
  url: "https://doi.org/10.1234/kotae.2026.1",
  published: "2026-07-29",
  source: "Crossref",
});

test("bridge cancellation releases ownership before rejecting the recording", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("function releaseMicrophone()");
  const end = bridge.indexOf("\n}\n\nfunction hasLiveAudioTrack", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const release = bridge.slice(start, end);

  assert.doesNotMatch(release, /activeRecording\.captureBuffer/u);
  const detachAt = release.indexOf("activeRecording = undefined");
  const rejectAt = release.indexOf(
    'rejectRecording(recording, "request_cancelled")',
  );
  assert.ok(detachAt >= 0);
  assert.ok(rejectAt > detachAt);
});

test("bridge primes App Check before a fresh anonymous sign-in", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function initializeAuthenticatedUser()");
  const end = bridge.indexOf(
    "\n}\n\nconst authenticatedUser",
    start,
  );
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const initializeUser = bridge.slice(start, end);

  const appCheckAt = initializeUser.indexOf(
    "await getAppCheckToken(appCheck, false)",
  );
  const initializeAuthAt = initializeUser.indexOf("initializeAuth(app");
  const anonymousSignInAt = initializeUser.indexOf("signInAnonymously(auth)");
  assert.ok(appCheckAt >= 0);
  assert.ok(initializeAuthAt > appCheckAt);
  assert.ok(anonymousSignInAt > initializeAuthAt);
});

test("empty capture rollover preserves the current conversation intent", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const marker = client.indexOf(
    "A finite recorder window with no speech is not a new user",
  );
  assert.notEqual(marker, -1);
  const rollover = client.slice(marker, marker + 600);

  assert.match(
    rollover,
    /arm_listening\(\s*operation,\s*false,\s*intentional,/u,
  );
  assert.doesNotMatch(
    rollover,
    /arm_listening\(\s*operation,\s*false,\s*false,/u,
  );
});

test("a replacement microphone stream rebuilds its analyser graph", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function ensureAudioGraph(");
  const end = bridge.indexOf("\n}\n\nfunction recorderOptions", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const ensureGraph = bridge.slice(start, end);

  assert.match(ensureGraph, /analyserStream !== stream/u);
  assert.match(ensureGraph, /analyserStream = stream/u);
});

test("stopping a session settles playback before stopping every source", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("function stopSession()");
  const end = bridge.indexOf("\n}\n\nfunction hasActiveVoiceSession", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const stop = bridge.slice(start, end);

  const detachAt = stop.indexOf("activePlayback = undefined");
  const rejectAt = stop.indexOf(
    'playback.reject(new Error("request_cancelled"))',
  );
  const sourcesAt = stop.indexOf("for (const source of playback.sources)");
  const stopAt = stop.indexOf("source.stop()");
  assert.ok(detachAt >= 0);
  assert.ok(rejectAt > detachAt);
  assert.ok(sourcesAt > rejectAt);
  assert.ok(stopAt > sourcesAt);
});

function finalVoiceResult() {
  return {
    audioBase64: "",
    audioMimeType: "audio/L16",
    sessionState: "opaque",
  };
}

function streamLine(value) {
  return `${JSON.stringify(value)}\n`;
}

test("voice stream accepts split UTF-8 input with strict ordered PCM events", () => {
  const parser = createVoiceStreamParser((result) =>
    Object.freeze({ ...result }),
  );
  const ready = streamLine({ type: "ready", version: 1 });
  const audio = streamLine({
    type: "audio",
    sequence: 0,
    audioBase64: "AQIDBA==",
    sampleRateHz: 24_000,
  });
  const final = JSON.stringify({
    type: "final",
    version: 1,
    result: finalVoiceResult(),
  });

  assert.deepEqual(parser.push(ready.slice(0, 8)), []);
  assert.deepEqual(parser.push(ready.slice(8)), [
    { type: "ready", version: 1 },
  ]);
  assert.deepEqual(parser.push(audio), [
    {
      type: "audio",
      sequence: 0,
      audioBase64: "AQIDBA==",
      decodedBytes: 4,
      sampleRateHz: 24_000,
    },
  ]);
  assert.deepEqual(parser.push(final), []);
  const completed = parser.finish();
  assert.equal(completed.audioEventCount, 1);
  assert.equal(completed.totalAudioBytes, 4);
  assert.deepEqual(completed.finalResult, finalVoiceResult());
  assert.equal(completed.events[0].type, "final");
});

test("voice stream fails closed on missing ready, sequence gaps, and truncation", () => {
  const audio = streamLine({
    type: "audio",
    sequence: 0,
    audioBase64: "AQIDBA==",
    sampleRateHz: 24_000,
  });
  const withoutReady = createVoiceStreamParser((result) => result);
  assert.throws(() => withoutReady.push(audio), /voice_response_invalid/);

  const sequenceGap = createVoiceStreamParser((result) => result);
  sequenceGap.push(streamLine({ type: "ready", version: 1 }));
  assert.throws(
    () =>
      sequenceGap.push(
        streamLine({
          type: "audio",
          sequence: 1,
          audioBase64: "AQIDBA==",
          sampleRateHz: 24_000,
        }),
      ),
    /voice_response_invalid/,
  );

  const truncated = createVoiceStreamParser((result) => result);
  truncated.push(streamLine({ type: "ready", version: 1 }));
  truncated.push(audio);
  assert.throws(() => truncated.finish(), /voice_response_invalid/);
});

test("voice stream rejects odd PCM, extra fields, and anything after final", () => {
  const oddPcm = createVoiceStreamParser((result) => result);
  oddPcm.push(streamLine({ type: "ready", version: 1 }));
  assert.throws(
    () =>
      oddPcm.push(
        streamLine({
          type: "audio",
          sequence: 0,
          audioBase64: "AQID",
          sampleRateHz: 24_000,
        }),
      ),
    /voice_response_invalid/,
  );

  const extraField = createVoiceStreamParser((result) => result);
  assert.throws(
    () =>
      extraField.push(
        streamLine({ type: "ready", version: 1, ignored: true }),
      ),
    /voice_response_invalid/,
  );

  const afterFinal = createVoiceStreamParser((result) => result);
  afterFinal.push(streamLine({ type: "ready", version: 1 }));
  afterFinal.push(
    streamLine({
      type: "final",
      version: 1,
      result: finalVoiceResult(),
    }),
  );
  assert.throws(
    () => afterFinal.push(streamLine({ type: "ready", version: 1 })),
    /voice_response_invalid/,
  );
});

test("stream bridge uses direct authenticated CORS with bounded PCM playback", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  assert.match(
    bridge,
    /https:\/\/kotae-api-r6kgkvtrmq-an\.a\.run\.app\/api\/v1\/voice\/turns:stream/u,
  );
  assert.doesNotMatch(
    bridge,
    /const VOICE_ENDPOINT = "\/api\/v1\/voice\/turns/u,
  );
  const fetchAt = bridge.indexOf("const response = await fetch(VOICE_ENDPOINT");
  const fetchBlock = bridge.slice(fetchAt, fetchAt + 700);
  assert.match(fetchBlock, /credentials: "omit"/u);
  assert.match(fetchBlock, /mode: "cors"/u);
  assert.match(fetchBlock, /Authorization: `Bearer \$\{idToken\}`/u);
  assert.match(fetchBlock, /"X-Firebase-AppCheck": appCheckToken/u);
  assert.match(bridge, /maximumTransportChunkBytes/u);
  assert.match(bridge, /maximumResponseBytes/u);
  assert.match(bridge, /new CustomEvent\("kotae:first-audio"/u);
  assert.equal(VOICE_STREAM_LIMITS.maximumAudioChunkBytes, 1024 * 1024);
  assert.equal(
    VOICE_STREAM_LIMITS.maximumAudioTotalBytes,
    16 * 1024 * 1024,
  );
});

test("research discovery stays bounded, immutable, and explicitly unverified", () => {
  const discovery = normalizeResearchDiscovery("needs_primary_evidence", [
    researchRecord,
    {
      title: "Partial publication dates remain partial",
      doi: "10.5678/kotae.2025.2",
      url: "https://doi.org/10.5678/kotae.2025.2",
      published: "2025",
      source: "Crossref",
    },
  ]);

  assert.equal(discovery.status, "needs_primary_evidence");
  assert.equal(discovery.records.length, 2);
  assert.equal(discovery.records[0].doi, researchRecord.doi);
  assert.equal(Object.isFrozen(discovery), true);
  assert.equal(Object.isFrozen(discovery.records), true);
  assert.equal(Object.isFrozen(discovery.records[0]), true);
  assert.equal(
    normalizeResearchDiscovery("needs_primary_evidence", [
      { ...researchRecord, title: "" },
    ]).records[0].title,
    "",
  );
  assert.deepEqual(normalizeResearchDiscovery("none", []), {
    status: "none",
    records: [],
  });
  assert.deepEqual(normalizeResearchDiscovery("unavailable", []), {
    status: "unavailable",
    records: [],
  });
});

test("research discovery rejects verified claims, unsafe links, and surplus data", () => {
  assert.throws(
    () => normalizeResearchDiscovery("verified", []),
    /research_discovery_invalid/,
  );
  assert.throws(
    () => normalizeResearchDiscovery("none", [researchRecord]),
    /research_discovery_invalid/,
  );
  assert.throws(
    () =>
      normalizeResearchDiscovery("needs_primary_evidence", [
        { ...researchRecord, url: "https://example.com/paper" },
      ]),
    /research_discovery_invalid/,
  );
  assert.throws(
    () =>
      normalizeResearchDiscovery("needs_primary_evidence", [
        {
          ...researchRecord,
          url: "https://doi.org/10.1234/a-different-record",
        },
      ]),
    /research_discovery_invalid/,
  );
  assert.throws(
    () =>
      normalizeResearchDiscovery("needs_primary_evidence", [
        { ...researchRecord, source: "Unreviewed crawler" },
      ]),
    /research_discovery_invalid/,
  );
  assert.throws(
    () =>
      normalizeResearchDiscovery("needs_primary_evidence", [
        { ...researchRecord, abstract: "must not cross this UI boundary" },
      ]),
    /research_discovery_invalid/,
  );
  assert.throws(
    () =>
      normalizeResearchDiscovery(
        "needs_primary_evidence",
        Array.from({ length: 6 }, () => researchRecord),
      ),
    /research_discovery_invalid/,
  );
});

test("permission or initialization failure releases every acquired resource once", async () => {
  const permissionError = Object.assign(new Error("permission denied"), {
    name: "NotAllowedError",
  });
  const tracks = [{ stopped: 0 }, { stopped: 0 }];
  let cleanupCalls = 0;

  await assert.rejects(
    initializeWithCleanup(
      async () => {
        throw permissionError;
      },
      () => {
        cleanupCalls += 1;
        for (const track of tracks) track.stopped += 1;
      },
    ),
    (error) => error === permissionError,
  );
  assert.equal(cleanupCalls, 1);
  assert.deepEqual(
    tracks.map((track) => track.stopped),
    [1, 1],
  );

  const result = await initializeWithCleanup(
    async () => "ready",
    () => {
      cleanupCalls += 1;
    },
  );
  assert.equal(result, "ready");
  assert.equal(cleanupCalls, 1);
});

test("hidden documents and pagehide stop only active voice sessions", () => {
  assert.equal(
    shouldStopSessionForLifecycle("visibilitychange", true, true),
    true,
  );
  assert.equal(
    shouldStopSessionForLifecycle("visibilitychange", false, true),
    false,
  );
  assert.equal(
    shouldStopSessionForLifecycle("visibilitychange", true, false),
    false,
  );
  assert.equal(shouldStopSessionForLifecycle("pagehide", false, true), true);
  assert.equal(shouldStopSessionForLifecycle("pagehide", true, false), false);

  const pendingPdfMakesSessionActive = Boolean({ mimeType: "application/pdf" });
  assert.equal(
    shouldStopSessionForLifecycle(
      "visibilitychange",
      true,
      pendingPdfMakesSessionActive,
    ),
    true,
  );
});

test("an unsent PDF expires at five minutes, not before", () => {
  const attachedAt = 50_000;
  assert.equal(
    isPendingDocumentExpired(
      attachedAt,
      attachedAt + VOICE_SESSION_LIMITS.pendingDocumentLimitMs - 1,
    ),
    false,
  );
  assert.equal(
    isPendingDocumentExpired(
      attachedAt,
      attachedAt + VOICE_SESSION_LIMITS.pendingDocumentLimitMs,
    ),
    true,
  );
});

test("idle and absolute session expiries are checked at their boundaries", () => {
  let now = 10_000;
  const clock = createSessionClock({ now: () => now });
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });

  now += VOICE_SESSION_LIMITS.idleSessionLimitMs - 1;
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  now += 1;
  assert.deepEqual(clock.begin(), { expiry: "idle", ok: false });

  clock.reset();
  now = 1_000_000;
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  now += VOICE_SESSION_LIMITS.maximumSessionMs - 1;
  clock.markSpeech();
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  now += 1;
  assert.deepEqual(clock.begin(), { expiry: "maximum", ok: false });
});

test("VAD confirms 120 ms of voice then ends after 1.1 s of trailing silence", () => {
  const startedAt = 1_000;
  let state = createVadState(startedAt);
  const confirmationFrames =
    VOICE_SESSION_LIMITS.minimumVoiceMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  assert.equal(confirmationFrames, 3);

  for (let sample = 1; sample < confirmationFrames; sample += 1) {
    state = advanceVad(state, {
      now: startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs,
      peak: 0.08,
      rms: 0.03,
    });
    assert.equal(state.hasSpeech, false);
  }
  state = advanceVad(state, {
    now: startedAt + VOICE_SESSION_LIMITS.minimumVoiceMs,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.hasSpeech, true);
  assert.equal(state.action, null);

  state = advanceVad(state, {
    now:
      startedAt +
      VOICE_SESSION_LIMITS.minimumVoiceMs +
      VOICE_SESSION_LIMITS.endOfTurnSilenceMs -
      1,
    peak: 0,
    rms: 0.003,
  });
  assert.equal(state.action, null);

  state = advanceVad(state, {
    now:
      startedAt +
      VOICE_SESSION_LIMITS.minimumVoiceMs +
      VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
    peak: 0,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("VAD gives a reflective utterance 1.7 seconds to continue", () => {
  const startedAt = 5_000;
  let state = createVadState(startedAt);
  const reflectiveFrames =
    VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  assert.equal(Number.isInteger(reflectiveFrames), true);

  for (let sample = 1; sample <= reflectiveFrames; sample += 1) {
    state = advanceVad(state, {
      now: startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs,
      peak: 0.08,
      rms: 0.03,
    });
  }
  const lastVoiceAt = state.lastVoiceAt;
  assert.notEqual(lastVoiceAt, null);

  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
    peak: 0,
    rms: 0.003,
  });
  assert.equal(state.action, null);

  state = advanceVad(state, {
    now:
      lastVoiceAt +
      VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs -
      1,
    peak: 0,
    rms: 0.003,
  });
  assert.equal(state.action, null);

  state = advanceVad(state, {
    now:
      lastVoiceAt + VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
    peak: 0,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("VAD keeps a weak word ending after speech is confirmed", () => {
  const startedAt = 10_000;
  let state = createVadState(startedAt);
  const confirmationFrames =
    VOICE_SESSION_LIMITS.minimumVoiceMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let sample = 1; sample <= confirmationFrames; sample += 1) {
    state = advanceVad(state, {
      now: startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs,
      peak: 0.08,
      rms: 0.03,
    });
  }
  assert.equal(state.hasSpeech, true);

  const weakTailAt =
    startedAt +
    VOICE_SESSION_LIMITS.minimumVoiceMs +
    VOICE_SESSION_LIMITS.endOfTurnSilenceMs -
    VOICE_SESSION_LIMITS.vadIntervalMs;
  state = advanceVad(state, {
    now: weakTailAt,
    peak: 0.017,
    rms: 0.011,
  });
  assert.equal(state.lastVoiceAt, weakTailAt);
  assert.equal(state.action, null);
});

test("VAD caps a spoken capture at 55 seconds", () => {
  const startedAt = 20_000;
  let state = createVadState(startedAt);
  const confirmationFrames =
    VOICE_SESSION_LIMITS.minimumVoiceMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let sample = 1; sample <= confirmationFrames; sample += 1) {
    state = advanceVad(state, {
      now: startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs,
      peak: 0.08,
      rms: 0.03,
    });
  }
  assert.equal(state.hasSpeech, true);

  state = advanceVad(state, {
    now: startedAt + VOICE_SESSION_LIMITS.spokenCaptureLimitMs - 1,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now: startedAt + VOICE_SESSION_LIMITS.spokenCaptureLimitMs,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.action, "duration-limit");
});

test("VAD refreshes the trailing-silence clock as soon as confirmed speech resumes", () => {
  const startedAt = 30_000;
  let state = createVadState(startedAt);
  const confirmationFrames =
    VOICE_SESSION_LIMITS.minimumVoiceMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let sample = 1; sample <= confirmationFrames; sample += 1) {
    state = advanceVad(state, {
      now: startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs,
      peak: 0.08,
      rms: 0.03,
    });
  }
  assert.equal(state.hasSpeech, true);

  const resumesAt =
    startedAt +
    VOICE_SESSION_LIMITS.minimumVoiceMs +
    VOICE_SESSION_LIMITS.endOfTurnSilenceMs;
  for (
    let now =
      startedAt +
      VOICE_SESSION_LIMITS.minimumVoiceMs +
      VOICE_SESSION_LIMITS.vadIntervalMs;
    now < resumesAt;
    now += VOICE_SESSION_LIMITS.vadIntervalMs
  ) {
    state = advanceVad(state, {
      now,
      peak: 0.003,
      rms: 0.003,
    });
  }
  assert.equal(state.action, null);
  assert.equal(state.voiceRunMs, 0);

  state = advanceVad(state, {
    now: resumesAt,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.voiceRunMs, VOICE_SESSION_LIMITS.vadIntervalMs);
  assert.equal(state.lastVoiceAt, resumesAt);
  assert.equal(state.action, null);

  state = advanceVad(state, {
    now: resumesAt + VOICE_SESSION_LIMITS.vadIntervalMs,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(
    state.lastVoiceAt,
    resumesAt + VOICE_SESSION_LIMITS.vadIntervalMs,
  );
  assert.equal(state.action, null);
});

test("turn gate rejects double finish and ignores stale releases", () => {
  const gate = createTurnGate();
  const first = gate.acquire();
  assert.notEqual(first, null);
  assert.equal(gate.isBusy(), true);
  assert.equal(gate.acquire(), null);
  assert.equal(gate.release(Object.freeze({ sequence: first.sequence })), false);
  assert.equal(gate.release(first), true);
  assert.equal(gate.release(first), false);

  const second = gate.acquire();
  assert.notEqual(second, null);
  gate.reset();
  assert.equal(gate.release(second), false);
  assert.equal(gate.isBusy(), false);
});

test("an in-flight initialization gate remains held until cleanup finishes", async () => {
  const gate = createTurnGate();
  const token = gate.acquire();
  let rejectInitialization;
  let cleanupCalls = 0;
  const initialization = (async () => {
    try {
      return await initializeWithCleanup(
        () =>
          new Promise((_, reject) => {
            rejectInitialization = reject;
          }),
        () => {
          cleanupCalls += 1;
        },
      );
    } finally {
      gate.release(token);
    }
  })();

  await Promise.resolve();
  assert.equal(gate.acquire(), null);
  rejectInitialization(new Error("request_cancelled"));
  await assert.rejects(initialization, /request_cancelled/);
  assert.equal(cleanupCalls, 1);
  assert.equal(gate.isBusy(), false);
  assert.notEqual(gate.acquire(), null);
});

test("capture buffer retains complete recorder chunks in order", () => {
  const capture = createCaptureBuffer({ maximumBytes: 1_000 });
  capture.append({ id: 1, size: 10 });
  capture.append({ id: 2, size: 20 });
  capture.append({ id: 3, size: 30 });

  assert.deepEqual(capture.snapshot(), {
    retainedBytes: 60,
    retainedChunks: 3,
    tooLarge: false,
    totalBytes: 60,
  });
  assert.deepEqual(
    capture.take().chunks.map(({ id }) => id),
    [1, 2, 3],
  );
  assert.equal(capture.snapshot().totalBytes, 0);
});

test("capture buffer clears the complete payload at its byte ceiling", () => {
  const capture = createCaptureBuffer({ maximumBytes: 100 });
  capture.append({ id: 1, size: 40 });
  capture.append({ id: 2, size: 40 });
  const overflow = capture.append({ id: 3, size: 21 });

  assert.equal(overflow.tooLarge, true);
  assert.equal(overflow.totalBytes, 0);
  assert.deepEqual(capture.take(), { chunks: [], totalBytes: 0 });
  assert.equal(capture.snapshot().totalBytes, 0);
});

test("an 80 ms noise candidate is discarded before a fresh capture starts", () => {
  const startedAt = 40_000;
  let vadState = createVadState(startedAt);
  let candidateState = createCandidateCaptureState();
  const firstCapture = createCaptureBuffer({ maximumBytes: 1_000 });

  let now = startedAt + VOICE_SESSION_LIMITS.vadIntervalMs;
  vadState = advanceVad(vadState, {
    now,
    peak: 0.08,
    rms: 0.03,
  });
  candidateState = advanceCandidateCapture(candidateState, vadState, now);
  assert.deepEqual(candidateState, {
    action: "start",
    candidateStartedAt: now,
    phase: "candidate",
  });
  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  vadState = advanceVad(vadState, {
    now,
    peak: 0.08,
    rms: 0.03,
  });
  candidateState = advanceCandidateCapture(candidateState, vadState, now);
  assert.equal(vadState.voiceRunMs, 80);
  assert.equal(vadState.hasSpeech, false);
  assert.equal(candidateState.phase, "candidate");
  firstCapture.append({ id: "first-webm-header", size: 20 });

  for (let sample = 0; sample < 4; sample += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, {
      now,
      peak: 0.003,
      rms: 0.003,
    });
    candidateState = advanceCandidateCapture(candidateState, vadState, now);
  }
  assert.equal(vadState.voiceRunMs, 0);
  assert.deepEqual(candidateState, {
    action: "discard",
    candidateStartedAt: null,
    phase: "armed",
  });
  assert.equal(vadState.firstVoiceAt, null);
  firstCapture.clear();
  assert.deepEqual(firstCapture.take(), { chunks: [], totalBytes: 0 });

  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  vadState = advanceVad(vadState, {
    now,
    peak: 0.08,
    rms: 0.03,
  });
  const nextCandidate = advanceCandidateCapture(candidateState, vadState, now);
  assert.equal(vadState.firstVoiceAt, now);
  assert.deepEqual(nextCandidate, {
    action: "start",
    candidateStartedAt: now,
    phase: "candidate",
  });

  // A rearmed candidate owns a new buffer just as it owns a new
  // MediaRecorder. The rejected container header can therefore never become
  // the header of a later, confirmed utterance.
  const secondCapture = createCaptureBuffer({ maximumBytes: 1_000 });
  secondCapture.append({ id: "second-webm-header", size: 24 });
  secondCapture.append({ id: "second-speech", size: 36 });
  assert.equal(firstCapture.snapshot().totalBytes, 0);
  assert.deepEqual(
    secondCapture.take().chunks.map(({ id }) => id),
    ["second-webm-header", "second-speech"],
  );
});

test("capture starts on the first voiced frame but confirms after 120 ms", () => {
  const startedAt = 50_000;
  let vadState = createVadState(startedAt);
  let candidateState = createCandidateCaptureState();

  const confirmationFrames =
    VOICE_SESSION_LIMITS.minimumVoiceMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  assert.equal(confirmationFrames, 3);
  for (let sample = 1; sample <= confirmationFrames; sample += 1) {
    const now = startedAt + sample * VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, {
      now,
      peak: 0.08,
      rms: 0.03,
    });
    candidateState = advanceCandidateCapture(candidateState, vadState, now);

    if (sample === 1) {
      assert.deepEqual(candidateState, {
        action: "start",
        candidateStartedAt: now,
        phase: "candidate",
      });
    } else if (sample < confirmationFrames) {
      assert.deepEqual(candidateState, {
        action: null,
        candidateStartedAt:
          startedAt + VOICE_SESSION_LIMITS.vadIntervalMs,
        phase: "candidate",
      });
    } else {
      assert.equal(vadState.hasSpeech, true);
      assert.deepEqual(candidateState, {
        action: "confirm",
        candidateStartedAt:
          startedAt + VOICE_SESSION_LIMITS.vadIntervalMs,
        phase: "confirmed",
      });
    }
  }
});

test("candidate capture phase is finite and cannot regress after confirmation", () => {
  const vadState = createVadState(60_000);

  assert.throws(
    () =>
      advanceCandidateCapture(
        {
          action: null,
          candidateStartedAt: null,
          phase: "recording",
        },
        vadState,
        60_000,
      ),
    /candidate_capture_state_invalid/,
  );
  assert.throws(
    () =>
      advanceCandidateCapture(
        {
          action: null,
          candidateStartedAt: 60_000,
          phase: "confirmed",
        },
        vadState,
        60_000,
      ),
    /candidate_capture_transition_invalid/,
  );
});

test("candidate capture has a finite privacy deadline", () => {
  const startedAt = 70_000;
  let state = advanceCandidateCapture(
    createCandidateCaptureState(),
    {
      hasSpeech: false,
      sampleVoiced: true,
      voiceRunMs: VOICE_SESSION_LIMITS.vadIntervalMs,
    },
    startedAt,
  );
  assert.equal(state.action, "start");

  state = advanceCandidateCapture(
    state,
    {
      hasSpeech: false,
      sampleVoiced: true,
      voiceRunMs: VOICE_SESSION_LIMITS.vadIntervalMs,
    },
    startedAt + VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
  );
  assert.deepEqual(state, {
    action: "discard",
    candidateStartedAt: null,
    phase: "armed",
  });
});

test("manual stop latch accepts one reason and rejects duplicate POST paths", () => {
  const latch = createStopLatch();
  assert.equal(latch.request("manual"), true);
  assert.equal(latch.isRequested(), true);
  assert.equal(latch.reason(), "manual");
  assert.equal(latch.request("end-of-turn"), false);
  assert.equal(latch.reason(), "manual");
});

test("retryable initializer shares an attempt, retries a failure, and caches success", async () => {
  let calls = 0;
  const initialize = createRetryableInitializer(async () => {
    calls += 1;
    if (calls === 1) throw new Error("temporary_failure");
    return Object.freeze({ ready: true });
  });

  const first = initialize();
  assert.equal(initialize(), first);
  await assert.rejects(first, /temporary_failure/);

  const second = initialize();
  assert.notEqual(second, first);
  assert.deepEqual(await second, { ready: true });
  assert.equal(initialize(), second);
  assert.equal(calls, 2);
});

test("gesture epoch, not session state, selects explicit versus ambient mode", () => {
  const resumedTurn = {
    sessionState: "opaque-encrypted-state",
    turnMode: turnModeForGestureEpoch(true),
  };
  assert.equal(resumedTurn.turnMode, "intentional");
  assert.equal(isValidTurnMode(resumedTurn.turnMode), true);

  const automaticFollowUp = {
    sessionState: resumedTurn.sessionState,
    turnMode: turnModeForGestureEpoch(false),
  };
  assert.equal(automaticFollowUp.turnMode, "ambient");
  assert.equal(isValidTurnMode(automaticFollowUp.turnMode), true);
  assert.equal(isValidTurnMode("derived-from-session-state"), false);
  assert.throws(() => turnModeForGestureEpoch("first"), /gesture_epoch_invalid/);
});
