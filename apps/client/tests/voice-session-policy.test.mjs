import assert from "node:assert/strict";
import { readFile as readRawFile } from "node:fs/promises";
import test from "node:test";

async function readFile(path, encoding) {
  const source = await readRawFile(path, encoding);
  return typeof source === "string"
    ? source.replaceAll("\r\n", "\n")
    : source;
}

import {
  advanceCandidateCapture,
  advanceVad,
  classifyVoiceSessionStopReason,
  createCaptureBuffer,
  createCandidateCaptureState,
  createRetryableInitializer,
  createSessionClock,
  createSessionExpiryWatchdog,
  createStopLatch,
  createTurnGate,
  createVadState,
  initializeWithCleanup,
  isPendingDocumentExpired,
  isValidTurnMode,
  normalizeResearchDiscovery,
  shouldCommitHybridEndpoint,
  shouldShowVoiceReceipt,
  shouldStopSessionForLifecycle,
  turnModeForGestureEpoch,
  VOICE_RECEIPT_LIMITS,
  VOICE_SESSION_LIMITS,
} from "../web/voice-session-policy.mjs";
import {
  ambientHandoffAssignmentAllowed,
  advanceInterruptVad,
  BARGE_PCM_LIMITS,
  claimAmbientLiveHandoff,
  CONFIRMED_SPEECH_PCM_LIMITS,
  createBargePcmRing,
  createConfirmedSpeechPcmGate,
  createInterruptVadState,
  createLivePcmQueue,
  createVoiceLiveClientTransport,
  createVoiceLiveServerProtocol,
  createVoiceStreamParser,
  estimateAudiblePerformanceTime,
  INTERRUPT_VAD_LIMITS,
  isCleanVoiceLiveTerminalClose,
  shouldAbortVoiceTransportOnInterrupt,
  shouldStartAmbientLiveHandoff,
  safeLiveCaptureFrame,
  safeLiveCaptureSignal,
  shouldReplayCommittedNativeTurn,
  validatedPlaybackDrainTimeoutMs,
  VOICE_LIVE_LIMITS,
  VOICE_PLAYBACK_LIMITS,
  VOICE_STREAM_LIMITS,
} from "../web/voice-stream-policy.mjs";

test("the content-free receipt stays inside a sub-three-second budget", () => {
  assert.equal(VOICE_RECEIPT_LIMITS.visibleAfterSilenceMs, 700);
  assert.equal(
    shouldShowVoiceReceipt({
      hasSpeech: true,
      lastVoiceAt: 1_000,
      now: 1_699,
    }),
    false,
  );
  assert.equal(
    shouldShowVoiceReceipt({
      hasSpeech: true,
      lastVoiceAt: 1_000,
      now: 1_700,
    }),
    true,
  );
  assert.equal(
    shouldShowVoiceReceipt({
      hasSpeech: true,
      lastVoiceAt: 1_680,
      now: 1_700,
    }),
    false,
    "resumed speech clears the receipt",
  );
  assert.equal(
    shouldShowVoiceReceipt({
      hasSpeech: false,
      lastVoiceAt: null,
      now: 5_000,
    }),
    false,
  );
  assert.throws(
    () =>
      shouldShowVoiceReceipt(
        { hasSpeech: true, lastVoiceAt: 0, now: 3_000 },
        { visibleAfterSilenceMs: 3_000 },
      ),
    /voice_receipt_state_invalid/u,
  );
});

function executableBridgeFunction(source, signature, nextSignature) {
  const start = source.indexOf(signature);
  const end = source.indexOf(`\n\n${nextSignature}`, start);
  assert.notEqual(start, -1, `${signature} is missing`);
  assert.notEqual(end, -1, `${nextSignature} is missing`);
  return source.slice(start, end);
}

class FakePlaybackTimers {
  constructor() {
    this.nextId = 1;
    this.pending = new Map();
  }

  clearTimeout(id) {
    this.pending.delete(id);
  }

  fireNext() {
    const entry = this.pending.entries().next().value;
    assert.ok(entry, "a playback deadline must be armed");
    const [id, timer] = entry;
    this.pending.delete(id);
    timer.callback();
    return timer.delay;
  }

  setTimeout(callback, delay) {
    const id = this.nextId;
    this.nextId += 1;
    this.pending.set(id, { callback, delay });
    return id;
  }
}

class FakePlaybackSource {
  constructor() {
    this.buffer = undefined;
    this.disconnected = false;
    this.ended = false;
    this.listeners = new Map();
    this.startedAt = undefined;
    this.stopped = false;
  }

  addEventListener(type, listener) {
    this.listeners.set(type, listener);
  }

  connect() {}

  disconnect() {
    this.disconnected = true;
  }

  end() {
    if (this.ended) return;
    this.ended = true;
    const listener = this.listeners.get("ended");
    this.listeners.delete("ended");
    listener?.();
  }

  start(at) {
    this.startedAt = at;
  }

  stop() {
    this.stopped = true;
    // Some browser engines dispatch ended synchronously from stop().
    this.end();
  }
}

class FakePlaybackAudioContext {
  constructor() {
    this.baseLatency = 0;
    this.currentTime = 0;
    this.destination = Object.freeze({});
    this.outputLatency = 0;
    this.sources = [];
    this.state = "running";
  }

  createBufferSource() {
    const source = new FakePlaybackSource();
    this.sources.push(source);
    return source;
  }

  createGain() {
    return {
      connected: false,
      disconnected: false,
      connect() {
        this.connected = true;
      },
      disconnect() {
        this.disconnected = true;
      },
      gain: {
        setValueAtTime() {},
      },
    };
  }
}

class ControlledVoiceReader {
  constructor() {
    this.cancelledWith = undefined;
    this.closed = false;
    this.failedWith = undefined;
    this.pendingRead = undefined;
    this.queue = [];
    this.released = false;
  }

  cancel(error) {
    this.cancelledWith = error;
    this.closed = true;
    if (this.pendingRead) {
      const { resolve } = this.pendingRead;
      this.pendingRead = undefined;
      resolve({ done: true, value: undefined });
    }
    return Promise.resolve();
  }

  close() {
    this.closed = true;
    if (this.pendingRead) {
      const { resolve } = this.pendingRead;
      this.pendingRead = undefined;
      resolve({ done: true, value: undefined });
    }
  }

  fail(error) {
    this.failedWith = error;
    if (this.pendingRead) {
      const { reject } = this.pendingRead;
      this.pendingRead = undefined;
      reject(error);
    }
  }

  pushText(value) {
    const chunk = new TextEncoder().encode(value);
    if (this.pendingRead) {
      const { resolve } = this.pendingRead;
      this.pendingRead = undefined;
      resolve({ done: false, value: chunk });
      return;
    }
    this.queue.push(chunk);
  }

  read() {
    if (this.queue.length > 0) {
      return Promise.resolve({ done: false, value: this.queue.shift() });
    }
    if (this.failedWith) return Promise.reject(this.failedWith);
    if (this.closed) {
      return Promise.resolve({ done: true, value: undefined });
    }
    assert.equal(this.pendingRead, undefined, "only one read may be pending");
    return new Promise((resolve, reject) => {
      this.pendingRead = { reject, resolve };
    });
  }

  releaseLock() {
    this.released = true;
  }
}

function fakeVoiceStreamResponse(reader) {
  return {
    body: {
      getReader: () => reader,
    },
    headers: {
      get(name) {
        return name.toLowerCase() === "content-type"
          ? "application/x-ndjson; charset=utf-8"
          : null;
      },
    },
  };
}

function fakePcmEvent(sequence, byteLength = 4_800) {
  return {
    pcm: new ArrayBuffer(byteLength),
    sampleRateHz: 24_000,
    sequence,
  };
}

async function flushPlaybackMicrotasks() {
  await Promise.resolve();
  await Promise.resolve();
}

async function waitForPlaybackState(predicate) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (predicate()) return;
    await flushPlaybackMicrotasks();
  }
  assert.fail("playback state did not settle");
}

async function createExecutablePlaybackHarness({ pcmSamples = [] } = {}) {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const executable = [
    executableBridgeFunction(
      bridge,
      "function stopBargeInMonitoring(",
      "function rampPlaybackGain(",
    ),
    executableBridgeFunction(
      bridge,
      "function shouldAbortPlaybackTransportOnInterrupt(",
      "function confirmBargeIn(",
    ),
    executableBridgeFunction(
      bridge,
      "function confirmBargeIn(",
      "function startBargeInMonitoring(",
    ),
    executableBridgeFunction(
      bridge,
      "function createStreamingPlayback(",
      "function haltStreamingPlayback(",
    ),
    executableBridgeFunction(
      bridge,
      "function haltStreamingPlayback(",
      "async function awaitValidatedPlaybackCompletion(",
    ),
    executableBridgeFunction(
      bridge,
      "async function awaitValidatedPlaybackCompletion(",
      "async function consumeVoiceStream(",
    ),
    executableBridgeFunction(
      bridge,
      "function isNdjsonContentType(",
      "function pcm16BytesAudioBuffer(",
    ),
    executableBridgeFunction(
      bridge,
      "async function consumeVoiceStream(",
      "async function finishTurn(",
    ),
    executableBridgeFunction(
      bridge,
      "function stopSession(",
      "function hasActiveVoiceSession(",
    ),
  ].join("\n\n");
  const context = new FakePlaybackAudioContext();
  const timers = new FakePlaybackTimers();
  const remainingPcmSamples = [...pcmSamples];
  const state = {
    abandonedInterrupts: 0,
    bargeStartedAt: [],
    bargeResetAt: [],
    clearedCandidateDeadlines: 0,
    clearedDocuments: [],
    latencyEvents: [],
    micEnabled: false,
    pauseEvents: [],
    releasedCodes: [],
    resetCount: 0,
    restoredGain: 0,
  };

  function pcmBuffer(decodedBytes, sampleRateHz) {
    const samples =
      remainingPcmSamples.shift() ?? Float32Array.of(0.25);
    return {
      duration: decodedBytes / (sampleRateHz * 2),
      getChannelData: () => samples,
      numberOfChannels: 1,
      sampleRate: sampleRateHz,
    };
  }

  const factory = new Function(
    "dependencies",
    `"use strict";
let activeLiveSession;
let activePasskeyController;
let activePlayback;
let activeRecording;
let activeRequestController;
let audioContext = dependencies.audioContext;
let documentEpoch = 0;
let mediaStream = dependencies.mediaStream;
let sessionEpoch = 1;
const stoppedSessionCodes = new Map();
const CustomEvent = dependencies.CustomEvent;
const BARGE_PCM_LIMITS = dependencies.BARGE_PCM_LIMITS;
const TextDecoder = dependencies.TextDecoder;
const VOICE_STREAM_LIMITS = dependencies.VOICE_STREAM_LIMITS;
const abandonInterruptRecording = dependencies.abandonInterruptRecording;
const classifyVoiceSessionStopReason = dependencies.classifyVoiceSessionStopReason;
const candidateEventIsCurrent = dependencies.candidateEventIsCurrent;
const clearCandidateDeadline = dependencies.clearCandidateDeadline;
const clearPendingDocument = dependencies.clearPendingDocument;
const clearTimeout = dependencies.clearTimeout;
const createVoiceStreamParser = dependencies.createVoiceStreamParser;
const dispatchVoiceLatency = dependencies.dispatchVoiceLatency;
const estimateAudiblePerformanceTime = dependencies.estimateAudiblePerformanceTime;
const fail = dependencies.fail;
const finishGate = dependencies.finishGate;
const globalThis = dependencies.eventTarget;
const pcm16AudioBuffer = dependencies.pcm16AudioBuffer;
const pcm16BytesAudioBuffer = dependencies.pcm16BytesAudioBuffer;
const performance = dependencies.performance;
const markSessionSpeech = dependencies.markSessionSpeech;
const releaseMicrophone = dependencies.releaseMicrophone;
const restorePlaybackGain = dependencies.restorePlaybackGain;
const retirePendingLiveSession = dependencies.retirePendingLiveSession;
const safeVoiceResponse = dependencies.safeVoiceResponse;
const sessionClock = dependencies.sessionClock;
const sessionExpiryWatchdog = dependencies.sessionExpiryWatchdog;
const setTimeout = dependencies.setTimeout;
const setTracksEnabled = dependencies.setTracksEnabled;
const shouldAbortVoiceTransportOnInterrupt = dependencies.shouldAbortVoiceTransportOnInterrupt;
const startBargeInMonitoring = dependencies.startBargeInMonitoring;
const validatedPlaybackDrainTimeoutMs = dependencies.validatedPlaybackDrainTimeoutMs;

function rememberStoppedSession(expectedEpoch, code) {
  stoppedSessionCodes.set(expectedEpoch, code);
}

function stoppedSessionCode(expectedEpoch) {
  return stoppedSessionCodes.get(expectedEpoch) ?? "request_cancelled";
}

${executable}

return Object.freeze({
  awaitValidatedPlaybackCompletion,
  confirmBargeIn,
  consumeVoiceStream,
  createStreamingPlayback,
  discardInterruptedPlaybackRecording,
  getActivePlayback: () => activePlayback,
  getActiveRecording: () => activeRecording,
  getSessionEpoch: () => sessionEpoch,
  haltStreamingPlayback,
  maybeAbortPlaybackTransportOnInterrupt,
  setActiveRequestController: (controller) => { activeRequestController = controller; },
  setActiveRecording: (recording) => { activeRecording = recording; },
  shouldAbortPlaybackTransportOnInterrupt,
  shouldDiscardInterruptedPlaybackRecording,
  stopSession,
});`,
  );
  const runtime = factory({
    BARGE_PCM_LIMITS,
    CustomEvent: class {
      constructor(type, options) {
        this.detail = options?.detail;
        this.type = type;
      }
    },
    TextDecoder,
    VOICE_STREAM_LIMITS,
    abandonInterruptRecording(recording) {
      recording.settled = true;
      state.abandonedInterrupts += 1;
    },
    audioContext: context,
    candidateEventIsCurrent(recording, candidate) {
      return recording.candidate === candidate;
    },
    classifyVoiceSessionStopReason,
    clearCandidateDeadline() {
      state.clearedCandidateDeadlines += 1;
    },
    clearPendingDocument(reason) {
      state.clearedDocuments.push(reason);
    },
    clearTimeout: (id) => timers.clearTimeout(id),
    createVoiceStreamParser,
    dispatchVoiceLatency(value) {
      state.latencyEvents.push(value);
    },
    estimateAudiblePerformanceTime,
    eventTarget: {
      dispatchEvent(event) {
        state.pauseEvents.push(event);
        return true;
      },
    },
    fail(code) {
      throw new Error(code);
    },
    finishGate: {
      reset() {
        state.resetCount += 1;
      },
    },
    markSessionSpeech() {
      return true;
    },
    mediaStream: Object.freeze({}),
    pcm16AudioBuffer(_audioBase64, decodedBytes, sampleRateHz) {
      return pcmBuffer(decodedBytes, sampleRateHz);
    },
    pcm16BytesAudioBuffer(_pcm, decodedBytes, sampleRateHz) {
      return pcmBuffer(decodedBytes, sampleRateHz);
    },
    performance: { now: () => 0 },
    releaseMicrophone(code) {
      state.micEnabled = false;
      state.releasedCodes.push(code);
    },
    restorePlaybackGain() {
      state.restoredGain += 1;
    },
    retirePendingLiveSession() {},
    safeVoiceResponse: (result) => Object.freeze({ ...result }),
    sessionClock: {
      reset() {
        state.resetCount += 1;
      },
    },
    sessionExpiryWatchdog: {
      disarm() {
        state.resetCount += 1;
      },
    },
    setTimeout: (callback, delay) => timers.setTimeout(callback, delay),
    setTracksEnabled(enabled) {
      state.micEnabled = enabled;
    },
    shouldAbortVoiceTransportOnInterrupt,
    startBargeInMonitoring(playback, _expectedEpoch, audibleAt) {
      state.micEnabled = true;
      state.bargeStartedAt.push(audibleAt);
      playback.interruptRecording = {
        coachActive: playback.coachActive,
        settled: false,
      };
      playback.resetInterruptGuard = (nextAudibleAt) => {
        state.bargeResetAt.push(nextAudibleAt);
      };
    },
    validatedPlaybackDrainTimeoutMs,
  });
  return { context, runtime, state, timers };
}

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
  const start = bridge.indexOf("function releaseMicrophone(");
  const end = bridge.indexOf("\n}\n\nfunction hasLiveAudioTrack", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const release = bridge.slice(start, end);

  assert.doesNotMatch(release, /activeRecording\.captureBuffer/u);
  const detachAt = release.indexOf("activeRecording = undefined");
  const rejectAt = release.indexOf("rejectRecording(recording, code)");
  assert.ok(detachAt >= 0);
  assert.ok(rejectAt > detachAt);
  const stopVadAt = bridge.indexOf("function stopVad(");
  const stopVad = bridge.slice(stopVadAt, stopVadAt + 500);
  assert.match(
    stopVad,
    /recording\.vadPcm\.fill\(0\);[\s\S]*recording\.vadPcm = undefined/u,
  );
});

test("bridge requires a fresh passkey and never creates anonymous or popup identity", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function initializeFirebaseAuth()");
  const end = bridge.indexOf(
    "\n}\n\nconst firebaseAuth",
    start,
  );
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const initializeUser = bridge.slice(start, end);

  const appCheckAt = initializeUser.indexOf(
    "await getAppCheckToken(appCheck, false)",
  );
  const initializeAuthAt = initializeUser.indexOf("initializeAuth(app");
  assert.ok(appCheckAt >= 0);
  assert.ok(initializeAuthAt > appCheckAt);
  assert.doesNotMatch(bridge, /signInAnonymously/u);
  assert.doesNotMatch(bridge, /signInWithPopup|GoogleAuthProvider/u);
  assert.match(bridge, /!user\.isAnonymous/u);
  assert.match(
    bridge,
    /if \(!user\) \{\s*if \(!interactive\) fail\("passkey_required"\);\s*return authenticatePasskey\(auth, appCheckToken\)/u,
  );
  const freshStart = bridge.indexOf("async function freshPasskeyUser(");
  const freshEnd = bridge.indexOf(
    "async function registerPasskeyAccount(",
    freshStart,
  );
  const freshPasskeyUser = bridge.slice(freshStart, freshEnd);
  assert.doesNotMatch(freshPasskeyUser, /return registerPasskey\(/u);
  assert.match(freshPasskeyUser, /fail\("passkey_required"\)/u);
  assert.match(
    bridge.slice(bridge.indexOf("const publicBridge")),
    /registerPasskeyAccount/u,
  );
  assert.match(bridge, /await signInWithCustomToken\(auth, finish\.customToken\)/u);
  assert.match(bridge, /secureCredentials\(true\)/u);
  assert.match(bridge, /state: "passkey-required"/u);
});

test("runtime PDF is rejected before browser file access", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
	const ui = await readFile(
		new URL("../src/main.rs", import.meta.url),
		"utf8",
	);
  const attachStart = bridge.indexOf("async function attachDocument(");
  const attachEnd = bridge.indexOf("\n}\n\nfunction stopSession", attachStart);
  assert.notEqual(attachStart, -1);
  assert.notEqual(attachEnd, -1);
  const attach = bridge.slice(attachStart, attachEnd);

  assert.match(attach, /fail\("document_unavailable"\)/u);
  assert.doesNotMatch(attach, /getElementById|\.files|arrayBuffer|pendingDocument/u);
	assert.doesNotMatch(ui, /id: "paper-input"|r#type: "file"/u);
	assert.match(ui, /PDF入力[\s\S]*公開版では未提供/u);
});

test("explicit voice start warms only the fixed transport without private data", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const warmStart = bridge.indexOf("function primeVoiceTransportConnection()");
  const warmEnd = bridge.indexOf("\n}\n\nasync function getStatus", warmStart);
  assert.notEqual(warmStart, -1);
  assert.notEqual(warmEnd, -1);
  const warm = bridge.slice(warmStart, warmEnd);

  assert.match(bridge, /VOICE_WARMUP_ENDPOINT = `\$\{VOICE_ORIGIN\}\/health`/u);
  assert.doesNotMatch(bridge, /\/healthz/u);
  assert.match(warm, /fetch\(VOICE_WARMUP_ENDPOINT/u);
  assert.match(warm, /credentials:\s*"omit"/u);
  assert.match(warm, /mode:\s*"no-cors"/u);
  assert.doesNotMatch(
    warm,
    /Authorization|audioBase64|sessionState|X-Firebase-AppCheck/u,
  );

  const beginStart = bridge.indexOf("async function beginTurn(");
  const beginEnd = bridge.indexOf("\n}\n\nasync function waitForTurnEnd", beginStart);
  const begin = bridge.slice(beginStart, beginEnd);
  const sessionAt = begin.indexOf("sessionClock.begin()");
  const warmAt = begin.indexOf("primeVoiceTransportConnection()");
  const microphoneAt = begin.indexOf(
    "const stream = await ensureMediaStream(",
  );
  assert.ok(sessionAt >= 0);
  assert.ok(warmAt > sessionAt);
  assert.ok(microphoneAt > warmAt);
});

test("unfinished respondent coaching keeps Native input when privacy permits", async () => {
  const [bridge, client] = await Promise.all([
    readFile(
      new URL("../web/firebase-bridge.js", import.meta.url),
      "utf8",
    ),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);
  const beginStart = bridge.indexOf("async function beginTurn(");
  const beginEnd = bridge.indexOf(
    "\n}\n\nasync function waitForTurnEnd",
    beginStart,
  );
  assert.notEqual(beginStart, -1);
  assert.notEqual(beginEnd, -1);
  const begin = bridge.slice(beginStart, beginEnd);

  assert.match(
    begin,
    /strictCloudMinimization,\s*coachActive = false,\s*\)/u,
  );
  assert.match(begin, /typeof coachActive !== "boolean"/u);
  assert.match(
    begin,
    /const nativeAudio =\s*!strictCloudMinimization && !pendingDocument;/u,
  );
  assert.match(
    begin,
    /coachActive,\s*expectedEpoch,[\s\S]*nativeAudio,\s*sessionState: serializedSessionState/u,
  );
  assert.match(
    bridge,
    /nativeAudio \? \{ nativeCoachControl: true \} : \{\}/u,
  );
  assert.match(
    begin,
    /createRecording\(\s*stream,\s*nativeAudio,\s*coachActive,\s*\)/u,
  );

  const routeStart = client.indexOf("const fn requires_staged_route(self)");
  const routeEnd = client.indexOf("\n    const fn status(self)", routeStart);
  assert.notEqual(routeStart, -1);
  assert.notEqual(routeEnd, -1);
  const route = client.slice(routeStart, routeEnd);
  for (const pair of [
    "(CoachPhase::AwaitingAnswer, CoachAction::Elicit)",
    "(CoachPhase::AwaitingRestatement, CoachAction::Restate)",
    "(CoachPhase::Expanding, CoachAction::Expand)",
    "(CoachPhase::Blocked, CoachAction::Retry)",
  ]) {
    assert.ok(route.includes(pair), pair);
  }
  assert.doesNotMatch(route, /CoachAction::Complete|CoachAction::Release/u);

  const armStart = client.indexOf("fn arm_listening(");
  const armEnd = client.indexOf("\nfn resume_foreground_interruption(", armStart);
  const arm = client.slice(armStart, armEnd);
  assert.match(
    arm,
    /let coach_active_snapshot = coach_state\.peek\(\)\.requires_staged_route\(\);/u,
  );
  assert.match(
    arm,
    /cloud::begin_turn\(\s*&state_snapshot,\s*turn_mode,\s*strict_snapshot,\s*coach_active_snapshot,\s*\)/u,
  );
});

test("the staged coach lane stays coherent across interruption, fallback, and reconnect", async () => {
  const [bridge, client] = await Promise.all([
    readFile(
      new URL("../web/firebase-bridge.js", import.meta.url),
      "utf8",
    ),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);
  const liveStart = bridge.indexOf("async function startVoiceLiveSession(");
  const liveEnd = bridge.indexOf("\n}\n\nfunction isNdjsonContentType", liveStart);
  const live = bridge.slice(liveStart, liveEnd);
  assert.match(
    live,
    /handoffAmbient\([\s\S]*startVoiceLiveSession\(\{[\s\S]*nativeAudio,[\s\S]*turnMode: "foreground"/u,
  );
  assert.match(
    live,
    /matches\([\s\S]*expectedSessionState === sessionState[\s\S]*expectedStrictCloudMinimization === strictCloudMinimization/u,
  );

  const finishStart = bridge.indexOf("async function finishTurn(");
  const finishEnd = bridge.indexOf(
    "\n}\n\nfunction safeDocumentName",
    finishStart,
  );
  const finish = bridge.slice(finishStart, finishEnd);
  assert.match(
    finish,
    /createStreamingPlayback\(\s*expectedEpoch,\s*liveSession\.nativeAudio === true,\s*"live",\s*coachActive,\s*\)/u,
  );
  assert.match(
    finish,
    /createStreamingPlayback\(\s*expectedEpoch,\s*false,\s*"http",\s*coachActive,\s*\)/u,
  );
  assert.match(
    finish,
    /liveSession\.requiresStatefulHTTPFallback\(\)/u,
  );
  assert.match(finish, /sessionState: serializedSessionState/u);
  const bargeStart = bridge.indexOf("function startBargeInMonitoring(");
  const bargeEnd = bridge.indexOf(
    "\n}\n\nfunction createStreamingPlayback",
    bargeStart,
  );
  const barge = bridge.slice(bargeStart, bargeEnd);
  assert.match(
    barge,
    /createRecordingState\(\s*mediaStream,\s*playback\.nativeAudio === true,\s*playback\.coachActive === true,\s*\)/u,
  );

  const resumeStart = client.indexOf("fn start_or_resume(");
  const resumeEnd = client.indexOf("\nfn human_file_size(", resumeStart);
  const resume = client.slice(resumeStart, resumeEnd);
  assert.match(resume, /arm_listening\(/u);
  assert.match(resume, /coach_state,/u);

  assert.match(
    client,
    /const ANSWER_SUPPORT_COPY: &str =\s*"「一問だけ手伝って」で、AIが答えず、今回のA先頭だけ確認する";/u,
  );
  assert.match(
    client,
    /\(CoachPhase::Complete, _\) => "あなた自身の言葉が出ました"/u,
  );
  assert.doesNotMatch(client, /聞かれたことに届いています/u);
});

test("live PCM capture is attached before VAD can confirm immediate speech", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const beginStart = bridge.indexOf("async function beginTurn(");
  const beginEnd = bridge.indexOf(
    "\n}\n\nasync function waitForTurnEnd",
    beginStart,
  );
  assert.notEqual(beginStart, -1);
  assert.notEqual(beginEnd, -1);
  const begin = bridge.slice(beginStart, beginEnd);
  const liveAt = begin.indexOf(
    "const liveSession = await startVoiceLiveSession(",
  );
  const recordingAt = begin.indexOf(
    "const recording = createRecording(",
  );
  const assignmentAt = begin.indexOf("activeLiveSession = liveSession");
  assert.ok(liveAt >= 0);
  assert.ok(recordingAt > liveAt);
  assert.ok(assignmentAt > liveAt);
  assert.ok(assignmentAt < recordingAt);
  assert.match(
    begin.slice(recordingAt, recordingAt + 180),
    /createRecording\(\s*stream,\s*nativeAudio,\s*coachActive,\s*\)/u,
  );
  assert.doesNotMatch(begin, /voice_live_capture_late/u);
});

test("voice upload conversion overlaps refreshed credentials", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function finishTurn(");
  const end = bridge.indexOf("\n}\n\nfunction safeDocumentName", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const finish = bridge.slice(start, end);

  assert.match(
    finish,
    /Promise\.all\(\[\s*capture\.blob\.arrayBuffer\(\),\s*secureCredentials\(\),\s*\]\)/u,
  );
  const joinedAt = finish.indexOf("Promise.all([");
  const encodeAt = finish.indexOf("arrayBufferToBase64(audioBuffer)");
  assert.ok(joinedAt >= 0);
  assert.ok(encodeAt > joinedAt);
});

test("empty capture drops authority and keeps the bounded session alive", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const marker = client.indexOf("A silent bounded window carries no speech");
  assert.notEqual(marker, -1);
  const rollover = client.slice(marker, marker + 1_300);

  assert.match(
    rollover,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.doesNotMatch(
    rollover,
    /cloud::stop_session\(\)|VoiceState::Ready|VoiceTurnMode::Intentional/u,
  );
});

test("terminal barge-in commits final state before foreground continuation", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const start = client.indexOf("fn submit_turn(");
  const end = client.indexOf("\n}\n\n#[allow(clippy::too_many_arguments)]", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const submit = client.slice(start, end);
  const stateCommitAt = submit.indexOf(
    "session_state.set(result.session_state.clone())",
  );
  const interruptedAt = submit.indexOf("if result.interrupted");
  const foregroundResumeAt = submit.indexOf(
    "resume_foreground_interruption(",
    interruptedAt,
  );
  assert.ok(stateCommitAt >= 0);
  assert.ok(interruptedAt > stateCommitAt);
  assert.ok(foregroundResumeAt > interruptedAt);
});

test("barge-in racing bounded local playback preserves the validated final", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function consumeVoiceStream(");
  const end = bridge.indexOf("\n}\n\nasync function finishTurn", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const consume = bridge.slice(start, end);
  const terminalAt = consume.indexOf("const completed = parser.finish()");
  const finalizeAt = consume.indexOf(
    "const finalized = finalizeMeaningfulVoiceStream(",
  );
  const sealAt = consume.indexOf("playback.seal()");
  const returnAt = consume.indexOf("return finalized");
  assert.ok(terminalAt >= 0);
  assert.ok(finalizeAt > terminalAt);
  assert.ok(sealAt > finalizeAt);
  assert.ok(sealAt > terminalAt);
  assert.ok(returnAt > sealAt);
  assert.doesNotMatch(consume, /await playback\.completion/u);

  const drainStart = bridge.indexOf(
    "async function awaitValidatedPlaybackCompletion(",
  );
  const drainEnd = bridge.indexOf(
    "\n}\n\nasync function consumeVoiceStream",
    drainStart,
  );
  assert.notEqual(drainStart, -1);
  assert.notEqual(drainEnd, -1);
  const drain = bridge.slice(drainStart, drainEnd);
  assert.match(
    drain,
    /Promise\.race\(\[playback\.completion, deadline\]\)/u,
  );
  assert.match(
    drain,
    /catch \(error\) \{[\s\S]*if \(!playback\.interrupted\) throw error;/u,
  );
});

test("validated playback drain follows scheduled audio with a protocol-sized cap", () => {
  assert.equal(VOICE_PLAYBACK_LIMITS.drainGraceMs, 4_000);
  assert.equal(
    VOICE_PLAYBACK_LIMITS.maximumDrainMs,
    Math.ceil(
      (VOICE_STREAM_LIMITS.maximumAudioTotalBytes /
        (24_000 * 2)) *
        1_000,
    ) + VOICE_PLAYBACK_LIMITS.drainGraceMs,
  );
  assert.equal(
    validatedPlaybackDrainTimeoutMs({
      currentContextTime: 10,
      scheduledEndContextTime: 12.5,
    }),
    6_500,
  );
  assert.equal(
    validatedPlaybackDrainTimeoutMs({
      currentContextTime: 12.5,
      scheduledEndContextTime: 10,
    }),
    VOICE_PLAYBACK_LIMITS.drainGraceMs,
  );
  assert.equal(
    validatedPlaybackDrainTimeoutMs({
      currentContextTime: 0,
      scheduledEndContextTime: 10_000,
    }),
    VOICE_PLAYBACK_LIMITS.maximumDrainMs,
  );
  for (const invalid of [
    {},
    { currentContextTime: -1, scheduledEndContextTime: 1 },
    { currentContextTime: 0, scheduledEndContextTime: Infinity },
    {
      currentContextTime: 0,
      scheduledEndContextTime: 1,
      ignored: true,
    },
  ]) {
    assert.throws(
      () => validatedPlaybackDrainTimeoutMs(invalid),
      /voice_playback_deadline_invalid/u,
    );
  }
});

test("leading silent PCM does not start Speaking or barge monitoring", async () => {
  const silentSamples = new Float32Array(2_400);
  const meaningfulSamples = new Float32Array(2_400);
  meaningfulSamples[1_200] = 0.25;
  const { context, runtime, state } = await createExecutablePlaybackHarness({
    pcmSamples: [silentSamples, meaningfulSamples],
  });
  const playback = runtime.createStreamingPlayback(1);

  const silentAudibleAt = playback.schedulePcm(fakePcmEvent(0));
  assert.equal(silentAudibleAt, undefined);
  assert.equal(playback.hasStreamedAudio(), false);
  assert.equal(state.micEnabled, false);
  assert.equal(state.bargeStartedAt.length, 0);
  assert.equal(
    state.pauseEvents.filter((event) => event.type === "kotae:first-audio")
      .length,
    0,
  );

  const meaningfulAudibleAt = playback.schedulePcm(fakePcmEvent(1));
  assert.ok(Number.isFinite(meaningfulAudibleAt));
  assert.equal(playback.hasStreamedAudio(), true);
  assert.equal(state.micEnabled, true);
  assert.deepEqual(state.bargeStartedAt, [meaningfulAudibleAt]);
  assert.deepEqual(
    state.pauseEvents
      .filter((event) => event.type === "kotae:first-audio")
      .map((event) => event.detail.sequence),
    [1],
  );

  playback.finalReceived = true;
  playback.seal();
  for (const source of context.sources) source.end();
  await playback.completion;
});

test("a committed response monitors Thinking before meaningful audio", async () => {
  const silentSamples = new Float32Array(2_400);
  const meaningfulSamples = new Float32Array(2_400);
  meaningfulSamples[1_200] = 0.25;
  const { context, runtime, state } = await createExecutablePlaybackHarness({
    pcmSamples: [silentSamples, meaningfulSamples],
  });
  const playback = runtime.createStreamingPlayback(1, true, "live");

  playback.armResponseInterruption(25);
  assert.equal(playback.hasCommittedResponse(), true);
  assert.equal(playback.hasStreamedAudio(), false);
  assert.equal(state.micEnabled, true);
  assert.deepEqual(state.bargeStartedAt, [25]);
  assert.equal(state.bargeResetAt.length, 0);
  assert.equal(
    state.pauseEvents.filter((event) => event.type === "kotae:first-audio")
      .length,
    0,
  );

  playback.activateCoach();
  assert.equal(playback.coachActive, true);
  assert.equal(playback.interruptRecording.coachActive, true);

  assert.equal(playback.schedulePcm(fakePcmEvent(0)), undefined);
  assert.deepEqual(state.bargeStartedAt, [25]);
  assert.equal(state.bargeResetAt.length, 0);
  assert.equal(playback.hasStreamedAudio(), false);

  const audibleAt = playback.schedulePcm(fakePcmEvent(1));
  assert.ok(Number.isFinite(audibleAt));
  assert.deepEqual(state.bargeStartedAt, [25]);
  assert.deepEqual(state.bargeResetAt, [audibleAt]);
  assert.equal(playback.hasStreamedAudio(), true);
  assert.deepEqual(
    state.pauseEvents
      .filter((event) => event.type === "kotae:first-audio")
      .map((event) => event.detail.sequence),
    [1],
  );

  playback.finalReceived = true;
  playback.seal();
  for (const source of context.sources) source.end();
  await playback.completion;
});

test("no generated spoken presence cue can ship or be scheduled", async () => {
  await assert.rejects(
    readRawFile(
      new URL("../web/voice-presence-cue.mjs", import.meta.url),
      "utf8",
    ),
    { code: "ENOENT" },
  );
  const sources = await Promise.all([
    readFile(
      new URL("../web/firebase-bridge.js", import.meta.url),
      "utf8",
    ),
    readFile(
      new URL("../../../scripts/build-web.ps1", import.meta.url),
      "utf8",
    ),
    readFile(
      new URL("../../../scripts/deploy-hosting.ps1", import.meta.url),
      "utf8",
    ),
  ]);
  for (const source of sources) {
    assert.doesNotMatch(source, /voice-presence-cue/u);
    assert.doesNotMatch(source, /うん。/u);
  }
});

test("sustained Thinking speech cancels the pending response and owns the foreground turn", async () => {
  const { runtime, state } = await createExecutablePlaybackHarness();
  const playback = runtime.createStreamingPlayback(
    1,
    false,
    "http",
    false,
  );
  playback.armResponseInterruption(0);
  const recording = playback.interruptRecording;
  const candidate = {
    confirmed: false,
    contextFrame: 0,
  };
  recording.candidate = candidate;
  recording.interruptOnsetAt = 0;

  let vadState = advancePastInterruptGuard(
    createInterruptVadState(0),
    0,
  );
  const firstVoiceAt =
    INTERRUPT_VAD_LIMITS.guardMs + INTERRUPT_VAD_LIMITS.intervalMs;
  const confirmationFrames =
    INTERRUPT_VAD_LIMITS.confirmationMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let frame = 0; frame < confirmationFrames; frame += 1) {
    vadState = advanceInterruptVad(vadState, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: false,
      peak: 0.15,
      rms: 0.05,
    });
  }
  assert.equal(vadState.action, "confirm");
  assert.equal(vadState.voiceRunMs, INTERRUPT_VAD_LIMITS.confirmationMs);

  const requestController = new AbortController();
  runtime.setActiveRequestController(requestController);
  runtime.confirmBargeIn(playback, recording, candidate, 1);

  assert.equal(playback.hasStreamedAudio(), false);
  assert.equal(playback.interruptedBeforeFinal, true);
  assert.equal(playback.interrupted, true);
  assert.equal(requestController.signal.aborted, true);
  assert.equal(candidate.confirmed, true);
  assert.equal(runtime.getActiveRecording(), recording);
  assert.equal(runtime.getActivePlayback(), undefined);
  assert.equal(playback.interruptRecording, recording);
  assert.equal(state.clearedCandidateDeadlines, 1);
  assert.equal(state.micEnabled, true);
  assert.equal(
    state.pauseEvents.filter(
      (event) => event.type === "kotae:voice-interrupted",
    ).length,
    1,
  );
  await assert.rejects(playback.completion, /voice_interrupted/u);
});

test("an all-zero PCM response follows the recoverable no-reply path", async () => {
  const { context, runtime, state } =
    await createExecutablePlaybackHarness({
      pcmSamples: [new Float32Array(2_400)],
    });
  const playback = runtime.createStreamingPlayback(1, false, "http");
  playback.armResponseInterruption(0);
  const reader = new ControlledVoiceReader();
  reader.pushText(
    streamLine({ type: "ready", version: 1 }) +
      streamLine({
        type: "audio",
        version: 1,
        sequence: 0,
        audioBase64: "AQIDBA==",
        sampleRateHz: 24_000,
      }) +
      streamLine({
        type: "final",
        version: 1,
        result: finalVoiceResult(),
      }),
  );
  reader.close();

  await assert.rejects(
    runtime.consumeVoiceStream(
      fakeVoiceStreamResponse(reader),
      playback,
      1,
    ),
    /voice_turn_unavailable/u,
  );
  assert.equal(playback.hasStreamedAudio(), false);
  assert.equal(playback.finalReceived, true);
  assert.equal(
    state.pauseEvents.filter((event) => event.type === "kotae:first-audio")
      .length,
    0,
  );
  assert.notEqual(reader.cancelledWith, undefined);
  assert.equal(reader.released, true);

  runtime.haltStreamingPlayback(
    playback,
    new Error("voice_turn_unavailable"),
  );
  assert.equal(context.sources[0].stopped, true);
  assert.equal(state.micEnabled, false);
});

test("a zero-event silent final remains a valid recognition miss", async () => {
  const { runtime } = await createExecutablePlaybackHarness();
  const playback = runtime.createStreamingPlayback(1, false, "http");
  playback.armResponseInterruption(0);
  const reader = new ControlledVoiceReader();
  const expectedFinal = {
    ...finalVoiceResult(),
    audioMimeType: "",
  };
  reader.pushText(
    streamLine({ type: "ready", version: 1 }) +
      streamLine({
        type: "final",
        version: 1,
        result: expectedFinal,
      }),
  );
  reader.close();

  const completed = await runtime.consumeVoiceStream(
    fakeVoiceStreamResponse(reader),
    playback,
    1,
  );
  assert.deepEqual(completed.finalResult, expectedFinal);
  assert.equal(completed.streamedAudio, false);
  assert.equal(playback.hasStreamedAudio(), false);
  await runtime.awaitValidatedPlaybackCompletion(playback, 1);
});

test("an interrupted stateful silent drain preserves its validated final", async () => {
  const { runtime } = await createExecutablePlaybackHarness();
  const playback = runtime.createStreamingPlayback(
    1,
    false,
    "http",
    true,
  );
  playback.armResponseInterruption(0);
  runtime.setActiveRecording(playback.interruptRecording);
  playback.interruptedBeforeFinal = true;
  playback.interrupted = true;
  runtime.haltStreamingPlayback(
    playback,
    new Error("voice_interrupted"),
  );

  const reader = new ControlledVoiceReader();
  const expectedFinal = {
    ...finalVoiceResult(),
    assistanceTarget: "respondent",
    coachAction: "elicit",
    coachPhase: "awaiting_answer",
    respondentStage: "awaiting_answer",
    sessionState: "signed-coach-state",
  };
  reader.pushText(
    streamLine({ type: "ready", version: 1 }) +
      streamLine({
        type: "audio",
        version: 1,
        sequence: 0,
        audioBase64: "AQIDBA==",
        sampleRateHz: 24_000,
      }) +
      streamLine({
        type: "final",
        version: 1,
        result: expectedFinal,
      }),
  );
  reader.close();

  const completed = await runtime.consumeVoiceStream(
    fakeVoiceStreamResponse(reader),
    playback,
    1,
  );
  assert.deepEqual(completed.finalResult, expectedFinal);
  assert.equal(completed.streamedAudio, true);
  assert.equal(playback.hasStreamedAudio(), false);
  assert.equal(playback.finalReceived, true);
  await runtime.awaitValidatedPlaybackCompletion(playback, 1);
});

test("executable finish boundary waits for the last Web Audio source", async () => {
  const { context, runtime, state, timers } =
    await createExecutablePlaybackHarness();
  const playback = runtime.createStreamingPlayback(1);
  playback.schedulePcm(fakePcmEvent(0));
  playback.schedulePcm(fakePcmEvent(1));
  playback.finalReceived = true;
  playback.seal();

  let finished = false;
  const finishBoundary = runtime
    .awaitValidatedPlaybackCompletion(playback, 1)
    .then(() => {
      finished = true;
    });
  await flushPlaybackMicrotasks();
  assert.equal(finished, false);
  assert.equal(state.micEnabled, true, "barge-in remains active while audible");
  assert.equal(context.sources.length, 2);

  context.sources[0].end();
  await flushPlaybackMicrotasks();
  assert.equal(finished, false, "one remaining source must hold the turn");
  assert.equal(state.micEnabled, true);

  context.sources[1].end();
  await finishBoundary;
  assert.equal(finished, true);
  assert.equal(state.micEnabled, false);
  assert.equal(runtime.getActivePlayback(), undefined);
  assert.equal(timers.pending.size, 0);
});

test("executable drain timeout halts sources and disables the barge mic", async () => {
  const { context, runtime, state, timers } =
    await createExecutablePlaybackHarness();
  const playback = runtime.createStreamingPlayback(1);
  playback.schedulePcm(fakePcmEvent(0));
  playback.finalReceived = true;
  playback.seal();

  const finishBoundary = runtime
    .awaitValidatedPlaybackCompletion(playback, 1)
    .catch((error) => {
      runtime.haltStreamingPlayback(playback, error);
      throw error;
    });
  assert.equal(state.micEnabled, true);
  assert.equal(timers.pending.size, 1);
  const delay = timers.fireNext();
  assert.ok(delay >= VOICE_PLAYBACK_LIMITS.drainGraceMs);
  assert.ok(delay <= VOICE_PLAYBACK_LIMITS.maximumDrainMs);

  await assert.rejects(finishBoundary, /audio_playback_blocked/u);
  assert.equal(context.sources[0].stopped, true);
  assert.equal(context.sources[0].disconnected, true);
  assert.equal(state.micEnabled, false);
  assert.equal(runtime.getActivePlayback(), undefined);
  assert.equal(timers.pending.size, 0);
});

test("executable pre-final and post-final barge races keep their ownership", async (t) => {
  await t.test("pre-final interruption aborts without a validated final", async () => {
    const { context, runtime, state } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(1);
    const reader = new ControlledVoiceReader();
    reader.pushText(
      streamLine({ type: "ready", version: 1 }) +
        streamLine({
          type: "audio",
          version: 1,
          sequence: 0,
          audioBase64: "AQIDBA==",
          sampleRateHz: 24_000,
        }),
    );
    const consume = runtime.consumeVoiceStream(
      fakeVoiceStreamResponse(reader),
      playback,
      1,
    );
    await waitForPlaybackState(() => playback.hasStreamedAudio());
    assert.equal(playback.finalReceived, false);

    playback.interruptedBeforeFinal =
      shouldAbortVoiceTransportOnInterrupt(playback.finalReceived);
    playback.interrupted = true;
    const interruption = Object.assign(new Error("voice_interrupted"), {
      name: "AbortError",
    });
    runtime.haltStreamingPlayback(playback, interruption);
    reader.fail(interruption);

    await assert.rejects(consume, (error) => error === interruption);
    assert.equal(playback.interruptedBeforeFinal, true);
    assert.equal(reader.cancelledWith, interruption);
    assert.equal(reader.released, true);
    assert.equal(context.sources[0].stopped, true);
    assert.equal(
      state.micEnabled,
      true,
      "confirmed barge capture remains owned by the next utterance",
    );
  });

  await t.test("post-final interruption preserves the validated final", async () => {
    const { context, runtime, state, timers } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(1);
    const reader = new ControlledVoiceReader();
    const expectedFinal = finalVoiceResult();
    reader.pushText(
      streamLine({ type: "ready", version: 1 }) +
        streamLine({
          type: "audio",
          version: 1,
          sequence: 0,
          audioBase64: "AQIDBA==",
          sampleRateHz: 24_000,
        }) +
        streamLine({
          type: "final",
          version: 1,
          result: expectedFinal,
        }),
    );
    const consume = runtime.consumeVoiceStream(
      fakeVoiceStreamResponse(reader),
      playback,
      1,
    );
    await waitForPlaybackState(() => playback.finalReceived);

    playback.interruptedBeforeFinal =
      shouldAbortVoiceTransportOnInterrupt(playback.finalReceived);
    playback.interrupted = true;
    runtime.haltStreamingPlayback(
      playback,
      new Error("voice_interrupted"),
    );
    reader.close();

    const completed = await consume;
    await runtime.awaitValidatedPlaybackCompletion(playback, 1);
    assert.deepEqual(completed.finalResult, expectedFinal);
    assert.equal(completed.streamedAudio, true);
    assert.equal(playback.interruptedBeforeFinal, false);
    assert.equal(reader.cancelledWith, undefined);
    assert.equal(reader.released, true);
    assert.equal(context.sources[0].stopped, true);
    assert.equal(state.micEnabled, true);
    assert.equal(timers.pending.size, 0);
  });

  await t.test("pre-final HTTP interruption stops audio but commits only a clean final", async () => {
    const { context, runtime, state, timers } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(
      1,
      false,
      "http",
      true,
    );
    const reader = new ControlledVoiceReader();
    const expectedFinal = {
      ...finalVoiceResult(),
      assistanceTarget: "respondent",
      coachAction: "elicit",
      coachPhase: "awaiting_answer",
      respondentStage: "awaiting_answer",
      sessionState: "signed-coach-state",
    };
    let fetchCalls = 0;
    const response = (() => {
      fetchCalls += 1;
      return fakeVoiceStreamResponse(reader);
    })();
    reader.pushText(
      streamLine({ type: "ready", version: 1 }) +
        streamLine({
          type: "audio",
          version: 1,
          sequence: 0,
          audioBase64: "AQIDBA==",
          sampleRateHz: 24_000,
        }),
    );
    const consume = runtime.consumeVoiceStream(response, playback, 1);
    await waitForPlaybackState(() => playback.hasStreamedAudio());
    assert.equal(playback.interruptRecording.coachActive, true);
    const firstAudioEventsBefore = state.pauseEvents.filter(
      (event) => event.type === "kotae:first-audio",
    ).length;

    const requestController = new AbortController();
    playback.interruptedBeforeFinal = true;
    playback.interrupted = true;
    runtime.setActiveRecording(playback.interruptRecording);
    assert.equal(
      runtime.maybeAbortPlaybackTransportOnInterrupt(
        playback,
        requestController,
      ),
      false,
    );
    runtime.haltStreamingPlayback(
      playback,
      new Error("voice_interrupted"),
    );
    assert.equal(context.sources[0].stopped, true);
    assert.equal(requestController.signal.aborted, false);

    reader.pushText(
      streamLine({
        type: "audio",
        version: 1,
        sequence: 1,
        audioBase64: "BQYHCA==",
        sampleRateHz: 24_000,
      }) +
        streamLine({
          type: "final",
          version: 1,
          result: expectedFinal,
        }),
    );
    reader.close();

    const completed = await consume;
    await runtime.awaitValidatedPlaybackCompletion(playback, 1);
    assert.deepEqual(completed.finalResult, expectedFinal);
    assert.equal(completed.streamedAudio, true);
    assert.equal(playback.finalReceived, true);
    assert.equal(fetchCalls, 1);
    assert.equal(context.sources.length, 1, "post-barge audio is not scheduled");
    assert.equal(
      state.pauseEvents.filter(
        (event) => event.type === "kotae:first-audio",
      ).length,
      firstAudioEventsBefore,
      "discarded audio cannot re-fire first-audio",
    );
    assert.equal(reader.cancelledWith, undefined);
    assert.equal(reader.released, true);
    assert.equal(state.micEnabled, true);
    assert.equal(timers.pending.size, 0);
  });

  await t.test("an interrupted HTTP stream rejects trailing data without committing final", async () => {
    const { context, runtime, state } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(
      1,
      false,
      "http",
      true,
    );
    const reader = new ControlledVoiceReader();
    reader.pushText(
      streamLine({ type: "ready", version: 1 }) +
        streamLine({
          type: "audio",
          version: 1,
          sequence: 0,
          audioBase64: "AQIDBA==",
          sampleRateHz: 24_000,
        }),
    );
    const consume = runtime.consumeVoiceStream(
      fakeVoiceStreamResponse(reader),
      playback,
      1,
    );
    await waitForPlaybackState(() => playback.hasStreamedAudio());
    playback.interruptedBeforeFinal = true;
    playback.interrupted = true;
    runtime.setActiveRecording(playback.interruptRecording);
    runtime.haltStreamingPlayback(
      playback,
      new Error("voice_interrupted"),
    );
    reader.pushText(
      streamLine({
        type: "final",
        version: 1,
        result: finalVoiceResult(),
      }) +
        streamLine({ type: "ready", version: 1 }),
    );
    reader.close();

    await assert.rejects(consume, /voice_response_invalid/u);
    assert.equal(
      runtime.shouldDiscardInterruptedPlaybackRecording(playback),
      true,
    );
    runtime.discardInterruptedPlaybackRecording(playback);
    assert.equal(context.sources.length, 1);
    assert.notEqual(reader.cancelledWith, undefined);
    assert.equal(reader.released, true);
    assert.equal(playback.interruptRecording, undefined);
    assert.equal(state.abandonedInterrupts, 1);
    assert.equal(state.micEnabled, false);
  });

  await t.test("a non-stateful HTTP interruption aborts promptly and keeps the held turn", async () => {
    const { context, runtime, state } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(
      1,
      false,
      "http",
      false,
    );
    playback.schedulePcm(fakePcmEvent(0));
    const heldRecording = playback.interruptRecording;
    assert.notEqual(heldRecording, undefined);
    assert.equal(heldRecording.coachActive, false);

    const requestController = new AbortController();
    playback.interruptedBeforeFinal = true;
    playback.interrupted = true;
    runtime.setActiveRecording(heldRecording);
    assert.equal(
      runtime.maybeAbortPlaybackTransportOnInterrupt(
        playback,
        requestController,
      ),
      true,
    );
    runtime.haltStreamingPlayback(
      playback,
      new Error("voice_interrupted"),
    );

    assert.equal(requestController.signal.aborted, true);
    assert.equal(context.sources[0].stopped, true);
    assert.equal(playback.interruptRecording, heldRecording);
    assert.equal(
      runtime.shouldDiscardInterruptedPlaybackRecording(playback),
      false,
    );
    assert.equal(state.abandonedInterrupts, 0);
    assert.equal(state.micEnabled, true);
  });

  await t.test("a non-stateful HTTP interruption after final still validates clean EOF", async () => {
    const { runtime } = await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(
      1,
      false,
      "http",
      false,
    );
    playback.finalReceived = true;
    const requestController = new AbortController();
    assert.equal(
      runtime.maybeAbortPlaybackTransportOnInterrupt(
        playback,
        requestController,
      ),
      false,
    );
    assert.equal(requestController.signal.aborted, false);
  });

  await t.test("a live pre-final interruption still aborts its controller", async () => {
    const { runtime } = await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(1, true, "live");
    const requestController = new AbortController();
    assert.equal(
      runtime.maybeAbortPlaybackTransportOnInterrupt(
        playback,
        requestController,
      ),
      true,
    );
    assert.equal(requestController.signal.aborted, true);
  });

  await t.test("a dynamically activated Native coach drains and binds its held turn", async () => {
    const { context, runtime, state } =
      await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(1, true, "live");
    playback.activateCoach();
    assert.equal(playback.coachActive, true);
    playback.schedulePcm(fakePcmEvent(0));
    const heldRecording = playback.interruptRecording;
    assert.notEqual(heldRecording, undefined);
    assert.equal(heldRecording.coachActive, true);

    const requestController = new AbortController();
    playback.interruptedBeforeFinal = true;
    playback.interrupted = true;
    runtime.setActiveRecording(heldRecording);
    assert.equal(
      runtime.maybeAbortPlaybackTransportOnInterrupt(
        playback,
        requestController,
      ),
      false,
    );
    runtime.haltStreamingPlayback(
      playback,
      new Error("voice_interrupted"),
    );

    assert.equal(requestController.signal.aborted, false);
    assert.equal(context.sources[0].stopped, true);
    assert.equal(playback.interruptRecording, heldRecording);
    assert.equal(
      runtime.shouldDiscardInterruptedPlaybackRecording(playback),
      true,
      "a failed stateful drain cannot submit audio against stale state",
    );
    runtime.discardInterruptedPlaybackRecording(playback);
    assert.equal(playback.interruptRecording, undefined);
    assert.equal(state.abandonedInterrupts, 1);
    assert.equal(state.micEnabled, false);
  });

  await t.test("coach activation fails closed after response audio begins", async () => {
    const { runtime } = await createExecutablePlaybackHarness();
    const playback = runtime.createStreamingPlayback(1, true, "live");
    playback.schedulePcm(fakePcmEvent(0));
    assert.throws(
      () => playback.activateCoach(),
      /voice_response_invalid/u,
    );
  });
});

test("expiry and pagehide cancel an executable playback drain", async (t) => {
  for (const scenario of [
    { reason: "idle", stopCode: "session_expired" },
    { reason: "pagehide", stopCode: "request_cancelled" },
  ]) {
    await t.test(scenario.reason, async () => {
      const { context, runtime, state, timers } =
        await createExecutablePlaybackHarness();
      const playback = runtime.createStreamingPlayback(1);
      playback.schedulePcm(fakePcmEvent(0));
      playback.finalReceived = true;
      playback.seal();
      const drain = runtime.awaitValidatedPlaybackCompletion(playback, 1);

      runtime.stopSession(scenario.reason);

      await assert.rejects(
        drain,
        (error) => error instanceof Error && error.message === scenario.stopCode,
      );
      assert.equal(runtime.getSessionEpoch(), 2);
      assert.equal(runtime.getActivePlayback(), undefined);
      assert.equal(context.sources[0].stopped, true);
      assert.equal(state.micEnabled, false);
      assert.deepEqual(state.releasedCodes, [scenario.stopCode]);
      const pauseEvent = state.pauseEvents.find(
        (event) => event.type === "kotae:voice-session-paused",
      );
      assert.deepEqual(pauseEvent?.detail, {
        reason: scenario.reason,
        version: 1,
      });
      assert.equal(timers.pending.size, 0);
    });
  }
});

test("automatic rearm is foreground and only a fresh gesture is intentional", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const automatic = client.indexOf("VoiceTurnMode::Foreground");
  const explicit = client.indexOf("turn_mode_for_gesture_epoch(true)");
  assert.ok(automatic >= 0);
  assert.ok(explicit >= 0);
  const resumeStart = client.indexOf("fn resume_foreground_interruption(");
  const resumeEnd = client.indexOf(
    "\n}\n\n#[allow(clippy::too_many_arguments)]\nfn submit_turn",
    resumeStart,
  );
  const resume = client.slice(resumeStart, resumeEnd);
  assert.match(
    resume,
    /submit_turn\(\s*operation,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.match(
    resume,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
});

test("an authenticated silent foreground miss rearms without ending the session", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const submitStart = client.indexOf("fn submit_turn(");
  const submitEnd = client.indexOf("\nfn start_or_resume(", submitStart);
  assert.notEqual(submitStart, -1);
  assert.notEqual(submitEnd, -1);
  const submit = client.slice(submitStart, submitEnd);
  const missAt = submit.indexOf("silent_recognition_miss(&result.route)");
  const interruptedAt = submit.indexOf("if result.interrupted", missAt);
  assert.ok(missAt >= 0);
  assert.ok(interruptedAt > missAt);
  const miss = submit.slice(missAt, interruptedAt);
  assert.match(
    miss,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,[\s\S]*return;/u,
  );
  assert.doesNotMatch(
    miss,
    /cloud::stop_session\(\)|VoiceState::Ready/u,
  );
  assert.match(
    client,
    /"stt-silent-no-speech" \| "stt-silent-low-confidence"/u,
  );
});

test("successful and transiently failed turns rearm without resending confirmed speech", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const submitStart = client.indexOf("fn submit_turn(");
  const submitEnd = client.indexOf("\nfn start_or_resume(", submitStart);
  const submit = client.slice(submitStart, submitEnd);
  const recoverableStart = submit.indexOf(
    "Err(FinishTurnError::Recoverable(_message))",
  );
  const terminalStart = submit.indexOf(
    "Err(FinishTurnError::Message(message))",
    recoverableStart,
  );
  const recoverable = submit.slice(recoverableStart, terminalStart);
  assert.match(
    recoverable,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.doesNotMatch(
    recoverable,
    /cloud::stop_session\(\)|finish_turn|submit_turn/u,
  );

  const failureEnd = submit.indexOf("};", terminalStart);
  const failure = submit.slice(terminalStart, failureEnd);
  assert.match(failure, /cloud::stop_session\(\)/u);
  assert.match(failure, /voice_state\.set\(VoiceState::Error\(message\)\)/u);
  assert.doesNotMatch(failure, /finish_turn|submit_turn|arm_listening/u);

  const successStart = submit.lastIndexOf("arm_listening(");
  assert.ok(successStart > failureEnd);
  assert.match(
    submit.slice(successStart),
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
});

test("only reviewed transient finish errors preserve the foreground session", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const classifierStart = client.indexOf("fn recoverable_finish_turn_code(");
  const classifierEnd = client.indexOf("\n}\n", classifierStart) + 2;
  const classifier = client.slice(classifierStart, classifierEnd);
  for (const code of [
    "no_speech",
    "rate_limited",
    "voice_api_unavailable",
    "voice_turn_too_large",
    "voice_turn_timeout",
    "voice_turn_unavailable",
  ]) {
    assert.match(classifier, new RegExp(`"${code}"`, "u"));
  }
  for (const code of [
    "authentication_failed",
    "audio_playback_blocked",
    "request_cancelled",
    "session_expired",
    "voice_response_invalid",
    "voice_turn_invalid",
  ]) {
    assert.doesNotMatch(classifier, new RegExp(`"${code}"`, "u"));
  }
});

test("an oversized local capture rearms from wait without resending or closing the session", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const classifierStart = client.indexOf("fn recoverable_wait_turn_code(");
  const classifierEnd = client.indexOf("\n}\n", classifierStart) + 2;
  const classifier = client.slice(classifierStart, classifierEnd);
  assert.match(classifier, /"voice_turn_too_large"/u);

  const armStart = client.indexOf("fn arm_listening(");
  const armEnd = client.indexOf(
    "\n}\n\n#[allow(clippy::too_many_arguments)]\nfn resume_foreground_interruption",
    armStart,
  );
  const arm = client.slice(armStart, armEnd);
  const recoverableStart = arm.indexOf(
    "Err(WaitTurnError::Recoverable(_message))",
  );
  const terminalStart = arm.indexOf(
    "Err(WaitTurnError::Terminal(message))",
    recoverableStart,
  );
  assert.ok(recoverableStart >= 0);
  assert.ok(terminalStart > recoverableStart);
  const recoverable = arm.slice(recoverableStart, terminalStart);
  assert.match(
    recoverable,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.doesNotMatch(
    recoverable,
    /cloud::stop_session\(\)|finish_turn|submit_turn/u,
  );

  const resumeStart = client.indexOf("fn resume_foreground_interruption(");
  const resumeEnd = client.indexOf(
    "\n}\n\n#[allow(clippy::too_many_arguments)]\nfn submit_turn",
    resumeStart,
  );
  const resume = client.slice(resumeStart, resumeEnd);
  const resumeRecoverableStart = resume.indexOf(
    "Err(WaitTurnError::Recoverable(_message))",
  );
  const resumeTerminalStart = resume.indexOf(
    "Err(WaitTurnError::Terminal(message))",
    resumeRecoverableStart,
  );
  assert.ok(resumeRecoverableStart >= 0);
  assert.ok(resumeTerminalStart > resumeRecoverableStart);
  const resumeRecoverable = resume.slice(
    resumeRecoverableStart,
    resumeTerminalStart,
  );
  assert.match(
    resumeRecoverable,
    /arm_listening\(\s*operation,\s*false,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.doesNotMatch(
    resumeRecoverable,
    /cloud::stop_session\(\)|finish_turn|submit_turn/u,
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

test("foreground rearm never acquires a replacement microphone", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const ensureStart = bridge.indexOf("async function ensureMediaStream(");
  const ensureEnd = bridge.indexOf("\n}\n\nfunction createAudioContext", ensureStart);
  assert.notEqual(ensureStart, -1);
  assert.notEqual(ensureEnd, -1);
  const ensure = bridge.slice(ensureStart, ensureEnd);
  const reuseAt = ensure.indexOf("hasLiveAudioTrack(mediaStream)");
  const foregroundGuardAt = ensure.indexOf("allowAcquisition !== true");
  const pauseAt = ensure.indexOf('stopSession("microphone_lost")');
  const acquisitionAt = ensure.indexOf("navigator.mediaDevices.getUserMedia");
  assert.ok(reuseAt >= 0);
  assert.ok(foregroundGuardAt > reuseAt);
  assert.ok(pauseAt > foregroundGuardAt);
  assert.ok(acquisitionAt > pauseAt);

  const beginStart = bridge.indexOf("async function beginTurn(");
  const beginEnd = bridge.indexOf("\n}\n\nasync function waitForTurnEnd", beginStart);
  const begin = bridge.slice(beginStart, beginEnd);
  assert.match(
    begin,
    /ensureMediaStream\(\s*expectedEpoch,\s*turnMode === "intentional",\s*\)/u,
  );
  const lossListenerStart = bridge.indexOf(
    "function installMediaStreamLossListener(",
  );
  const lossListenerEnd = bridge.indexOf(
    "\n}\n\nfunction releaseMicrophone",
    lossListenerStart,
  );
  const lossListener = bridge.slice(lossListenerStart, lossListenerEnd);
  assert.match(
    lossListener,
    /stopSession\("microphone_lost"\)[\s\S]*addEventListener\("ended", onEnded, \{ once: true \}\)/u,
  );
});

test("finite lifecycle stops pause Rust while preserving opaque session state", async () => {
  const [bridge, client] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);
  const stopStart = bridge.indexOf("function stopSession(");
  const stopEnd = bridge.indexOf("\n}\n\nfunction hasActiveVoiceSession", stopStart);
  const stop = bridge.slice(stopStart, stopEnd);
  const eventAt = stop.indexOf('new CustomEvent("kotae:voice-session-paused"');
  assert.ok(eventAt >= 0);
  const eventBlock = stop.slice(eventAt, eventAt + 350);
  assert.match(eventBlock, /detail: Object\.freeze\(\{ reason: pauseReason, version: 1 \}\)/u);
  assert.doesNotMatch(eventBlock, /audio|caption|sessionState|transcript/u);
  assert.match(bridge, /stopSession\("hidden"\)/u);
  assert.match(bridge, /stopSession\("pagehide"\)/u);
  assert.match(bridge, /expire: \(reason\) => stopSession\(reason\)/u);

  const listenerStart = client.indexOf(
    "pub fn install_voice_session_paused_listener(",
  );
  const listenerEnd = client.indexOf("\n    pub fn stop_session()", listenerStart);
  const listener = client.slice(listenerStart, listenerEnd);
  const metadataAt = listener.indexOf("valid_voice_pause_metadata(");
  const cleanupAt = listener.indexOf("stop_session_js()");
  const pausedAt = listener.indexOf("voice_state.set(VoiceState::Paused)");
  assert.ok(metadataAt >= 0);
  assert.ok(cleanupAt > metadataAt);
  assert.ok(pausedAt > cleanupAt);
  assert.match(listener, /keys\.length\(\)/u);
  assert.match(listener, /session_stop_pauses\(current_state\)/u);
  assert.match(listener, /generation\.set\(next\)/u);
  assert.match(listener, /voice_state\.set\(VoiceState::Paused\)/u);
  assert.doesNotMatch(listener, /session_state\.set|String::new\(\)/u);
  const setupStart = client.indexOf(
    "let result = cloud::register_passkey_account().await;",
  );
  const setupReady = client.indexOf(
    "voice_state.set(VoiceState::Ready);",
    setupStart,
  );
  const setupError = client.indexOf("Err(message) =>", setupReady);
  assert.ok(setupStart >= 0);
  assert.ok(setupReady > setupStart);
  assert.ok(setupError > setupReady);
  const setupSuccess = client.slice(setupStart, setupError);
  for (const reset of [
    /generation\.set\(next\)/u,
    /session_state\.set\(String::new\(\)\)/u,
    /detected_domain\.set\(String::new\(\)\)/u,
    /route\.set\(String::new\(\)\)/u,
    /coach_state\.set\(CoachState::NONE\)/u,
    /needs_paper\.set\(false\)/u,
    /research_status\.set\(ResearchStatus::None\)/u,
    /research_records\.set\(Vec::new\(\)\)/u,
    /document_info\.set\(None\)/u,
    /document_error\.set\(None\)/u,
    /caption\.set\(None\)/u,
    /cloud::stop_session\(\)/u,
  ]) {
    assert.match(setupSuccess, reset);
  }
  const generationAt = setupSuccess.indexOf("generation.set(next)");
  const stopAt = setupSuccess.indexOf("cloud::stop_session()");
  const sessionClearAt = setupSuccess.indexOf(
    "session_state.set(String::new())",
  );
  const statusRefreshAt = setupSuccess.indexOf("cloud_status.restart()");
  const readyAt = setupSuccess.indexOf("voice_state.set(VoiceState::Ready)");
  assert.ok(generationAt >= 0);
  assert.ok(stopAt > generationAt);
  assert.ok(sessionClearAt > stopAt);
  assert.ok(statusRefreshAt > sessionClearAt);
  assert.ok(readyAt > statusRefreshAt);
  const setupReadyStatement = "voice_state.set(VoiceState::Ready);";
  const sessionLifecycleClient =
    client.slice(0, setupReady) +
    client.slice(setupReady + setupReadyStatement.length);
  assert.equal(
    sessionLifecycleClient.match(/voice_state\.set\(VoiceState::Ready\)/gu)
      ?.length,
    1,
    "outside completed account setup, Ready must remain explicit End only",
  );
});

test("stopping a session settles playback before stopping every source", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("function stopSession(");
  const end = bridge.indexOf("\n}\n\nfunction hasActiveVoiceSession", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const stop = bridge.slice(start, end);

  const detachAt = stop.indexOf("activePlayback = undefined");
  const rejectAt = stop.indexOf("playback.reject(new Error(stopCode))");
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
    version: 1,
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
      version: 1,
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

test("voice stream accepts a valid silent final with empty audio MIME", () => {
  const parser = createVoiceStreamParser((result) =>
    Object.freeze({ ...result }),
  );
  parser.push(streamLine({ type: "ready", version: 1 }));
  parser.push(
    streamLine({
      type: "final",
      version: 1,
      result: {
        audioBase64: "",
        audioMimeType: "",
        sessionState: "opaque",
      },
    }),
  );

  const completed = parser.finish();
  assert.equal(completed.audioEventCount, 0);
  assert.equal(completed.finalResult.audioMimeType, "");
});

test("voice stream audio-event ceiling matches the server chunk ceiling", () => {
  const parser = createVoiceStreamParser((result) => result);
  parser.push(streamLine({ type: "ready", version: 1 }));
  for (
    let sequence = 0;
    sequence < VOICE_STREAM_LIMITS.maximumAudioEventCount;
    sequence += 1
  ) {
    parser.push(
      streamLine({
        type: "audio",
        version: 1,
        sequence,
        audioBase64: "AAA=",
        sampleRateHz: 24_000,
      }),
    );
  }
  parser.push(
    streamLine({
      type: "final",
      version: 1,
      result: finalVoiceResult(),
    }),
  );
  assert.equal(
    parser.finish().audioEventCount,
    VOICE_STREAM_LIMITS.maximumAudioEventCount,
  );
  assert.equal(
    VOICE_STREAM_LIMITS.maximumEventCount,
    VOICE_STREAM_LIMITS.maximumAudioEventCount + 2,
  );

  const overflow = createVoiceStreamParser((result) => result);
  overflow.push(streamLine({ type: "ready", version: 1 }));
  for (
    let sequence = 0;
    sequence < VOICE_STREAM_LIMITS.maximumAudioEventCount;
    sequence += 1
  ) {
    overflow.push(
      streamLine({
        type: "audio",
        version: 1,
        sequence,
        audioBase64: "AAA=",
        sampleRateHz: 24_000,
      }),
    );
  }
  assert.throws(
    () =>
      overflow.push(
        streamLine({
          type: "audio",
          version: 1,
          sequence: VOICE_STREAM_LIMITS.maximumAudioEventCount,
          audioBase64: "AAA=",
          sampleRateHz: 24_000,
        }),
      ),
    /voice_response_invalid/,
  );
});

test("one transport read may contain multiple maximum-size NDJSON frames", () => {
  const parser = createVoiceStreamParser((result) => result);
  parser.push(streamLine({ type: "ready", version: 1 }));
  const maximumPCM = Buffer.alloc(
    VOICE_STREAM_LIMITS.maximumAudioChunkBytes,
  ).toString("base64");
  const combined =
    streamLine({
      type: "audio",
      version: 1,
      sequence: 0,
      audioBase64: maximumPCM,
      sampleRateHz: 24_000,
    }) +
    streamLine({
      type: "audio",
      version: 1,
      sequence: 1,
      audioBase64: maximumPCM,
      sampleRateHz: 24_000,
    });

  assert.ok(
    combined.length > VOICE_STREAM_LIMITS.maximumLineCharacters,
  );
  const events = parser.push(combined);
  assert.equal(events.length, 2);
  assert.equal(events[0].decodedBytes, 1024 * 1024);
  assert.equal(events[1].decodedBytes, 1024 * 1024);
});

test("barge-in aborts transport only before a final frame is parsed", () => {
  assert.equal(shouldAbortVoiceTransportOnInterrupt(false), true);
  assert.equal(shouldAbortVoiceTransportOnInterrupt(true), false);
  assert.throws(
    () => shouldAbortVoiceTransportOnInterrupt("yes"),
    /voice_stream_final_latch_invalid/,
  );
});

test("live final commits only after the exact clean server close", () => {
  assert.equal(
    isCleanVoiceLiveTerminalClose({
      code: 1_000,
      reason: "complete",
      wasClean: true,
    }),
    true,
  );
  for (const invalid of [
    { code: 1_000, reason: "", wasClean: true },
    { code: 1_000, reason: "complete", wasClean: false },
    { code: 1_001, reason: "complete", wasClean: true },
    {
      code: 1_000,
      reason: "complete",
      wasClean: true,
      ignored: true,
    },
  ]) {
    assert.equal(isCleanVoiceLiveTerminalClose(invalid), false);
  }
});

test("a parsed final can latch barge-in but trailing junk prevents terminal success", () => {
  const parser = createVoiceStreamParser((result) => result);
  const events = parser.push(
    streamLine({ type: "ready", version: 1 }) +
      streamLine({
        type: "final",
        version: 1,
        result: { ...finalVoiceResult(), audioMimeType: "" },
      }),
  );
  assert.equal(events.at(-1).type, "final");
  assert.equal(
    shouldAbortVoiceTransportOnInterrupt(
      events.some((event) => event.type === "final"),
    ),
    false,
  );
  assert.throws(() => parser.push("junk"), /voice_response_invalid/);
});

test("voice stream fails closed on missing ready, sequence gaps, and truncation", () => {
  const audio = streamLine({
    type: "audio",
    version: 1,
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
          version: 1,
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

test("voice stream preserves reviewed server failures after ready", () => {
  for (const code of ["voice_turn_timeout", "voice_turn_unavailable"]) {
    const parser = createVoiceStreamParser((result) => result);
    parser.push(streamLine({ type: "ready", version: 1 }));
    assert.throws(
      () =>
        parser.push(
          streamLine({
            type: "error",
            version: 1,
            code,
          }),
        ),
      new RegExp(code),
    );
  }

  const unreviewed = createVoiceStreamParser((result) => result);
  unreviewed.push(streamLine({ type: "ready", version: 1 }));
  assert.throws(
    () =>
      unreviewed.push(
        streamLine({
          type: "error",
          version: 1,
          code: "provider private detail",
        }),
      ),
    /voice_response_invalid/,
  );
});

test("voice stream rejects odd PCM, extra fields, and anything after final", () => {
  const oddPcm = createVoiceStreamParser((result) => result);
  oddPcm.push(streamLine({ type: "ready", version: 1 }));
  assert.throws(
    () =>
      oddPcm.push(
        streamLine({
          type: "audio",
          version: 1,
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
      result: { ...finalVoiceResult(), audioMimeType: "" },
    }),
  );
  assert.throws(
    () => afterFinal.push(streamLine({ type: "ready", version: 1 })),
    /voice_response_invalid/,
  );
});

test("voice stream requires version 1 on every PCM frame", () => {
  for (const version of [undefined, 0, 2, "1"]) {
    const parser = createVoiceStreamParser((result) => result);
    parser.push(streamLine({ type: "ready", version: 1 }));
    const frame = {
      type: "audio",
      sequence: 0,
      audioBase64: "AAA=",
      sampleRateHz: 24_000,
    };
    if (version !== undefined) frame.version = version;
    assert.throws(
      () => parser.push(streamLine(frame)),
      /voice_response_invalid/,
    );
  }
});

function liveStartFrame(nativeAudio = false) {
  const frame = {
    type: "start",
    version: 1,
    idToken: "firebase-id-token",
    nativeAudio,
    appCheckToken: "app-check-token",
    sessionState: "opaque-state",
    strictCloudMinimization: false,
    turnMode: "ambient",
    sampleRateHz: 16_000,
  };
  if (nativeAudio) frame.nativeCoachControl = true;
  return frame;
}

class MockWebSocket {
  constructor() {
    this.bufferedAmount = 0;
    this.readyState = 1;
    this.sent = [];
  }

  send(value) {
    this.sent.push(value);
  }
}

test("live capture accepts only exact 20 ms PCM frames and bounds startup", () => {
  assert.equal(VOICE_LIVE_LIMITS.maximumQueuedInputFrames, 200);
  assert.equal(VOICE_LIVE_LIMITS.readyTimeoutMs, 4_000);
  assert.equal(VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs, 300);
  assert.equal(
    VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs %
      BARGE_PCM_LIMITS.frameDurationMs,
    0,
    "the normal-listen lead-in must align to whole PCM frames",
  );
  assert.ok(
    VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs <=
      CONFIRMED_SPEECH_PCM_LIMITS.historyMs,
    "the normal-listen lead-in must fit in the bounded PCM history",
  );
  assert.equal(CONFIRMED_SPEECH_PCM_LIMITS.historyMs, 1_500);
  assert.equal(CONFIRMED_SPEECH_PCM_LIMITS.maximumFrames, 75);
  assert.equal(CONFIRMED_SPEECH_PCM_LIMITS.maximumBytes, 48_000);
  assert.equal(VOICE_LIVE_LIMITS.handoffReadyTimeoutMs, 450);
  assert.equal(VOICE_LIVE_LIMITS.terminalCloseTimeoutMs, 1_500);
  assert.equal(VOICE_LIVE_LIMITS.maximumSocketBufferedBytes, 16 * 1024);
  assert.equal(VOICE_LIVE_LIMITS.outboundChunkBytes, 640);
  assert.equal(VOICE_LIVE_LIMITS.workletCreditWindowFrames, 8);
  assert.equal(VOICE_LIVE_LIMITS.workletSealTimeoutMs, 1_500);
  const pcm = new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes);
  assert.equal(
    safeLiveCaptureFrame({
      contextFrame: 9_700,
      generation: 7,
      sequence: 0,
      type: "frame",
      version: 1,
      pcm,
    }, {
      cutoffContextFrame: 9_600,
      generation: 7,
      sequence: 0,
    }),
    pcm,
  );
  assert.throws(
    () =>
      safeLiveCaptureFrame({
        contextFrame: 9_700,
        generation: 7,
        sequence: 0,
        type: "frame",
        version: 1,
        pcm,
        ignored: true,
      }, {
        cutoffContextFrame: 9_600,
        generation: 7,
        sequence: 0,
      }),
    /voice_live_frame_invalid/,
  );
  const rejectedPcm = filledPcmFrame(91);
  assert.throws(
    () =>
      safeLiveCaptureFrame({
        contextFrame: 9_500,
        generation: 7,
        sequence: 0,
        type: "frame",
        version: 1,
        pcm: rejectedPcm,
      }, {
        cutoffContextFrame: 9_600,
        generation: 7,
        sequence: 0,
      }),
    /voice_live_frame_invalid/,
  );
  assert.equal(
    new Uint8Array(rejectedPcm).every((value) => value === 0),
    true,
  );
  assert.equal(
    safeLiveCaptureSignal(
      {
        generation: 7,
        lastSequence: 0,
        type: "sealed",
        version: 1,
      },
      { generation: 7, lastSequence: 0, sealing: true },
    ),
    "sealed",
  );

  const queue = createLivePcmQueue();
  for (
    let index = 0;
    index < VOICE_LIVE_LIMITS.maximumQueuedInputFrames;
    index += 1
  ) {
    queue.push(new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes));
  }
  const overflowFrame = new ArrayBuffer(
    VOICE_LIVE_LIMITS.inputFrameBytes,
  );
  new Uint8Array(overflowFrame).fill(255);
  assert.throws(
    () => queue.push(overflowFrame),
    /voice_live_queue_overflow/,
  );
  assert.equal(queue.size(), 0);
  assert.equal(
    new Uint8Array(overflowFrame).every((value) => value === 0),
    true,
  );
});

function filledPcmFrame(value) {
  const frame = new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes);
  new Uint8Array(frame).fill(value);
  return frame;
}

test("barge PCM ring retains the finite candidate and 100 ms pre-roll", () => {
  assert.equal(BARGE_PCM_LIMITS.frameDurationMs, 20);
  assert.equal(
    BARGE_PCM_LIMITS.historyMs,
    INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs + 100,
  );
  assert.equal(BARGE_PCM_LIMITS.leadInMs, 100);
  assert.equal(
    BARGE_PCM_LIMITS.maximumFrames,
    BARGE_PCM_LIMITS.historyMs / BARGE_PCM_LIMITS.frameDurationMs,
  );
  assert.equal(
    BARGE_PCM_LIMITS.maximumBytes,
    BARGE_PCM_LIMITS.maximumFrames *
      VOICE_LIVE_LIMITS.inputFrameBytes,
  );

  const ring = createBargePcmRing();
  const evicted = filledPcmFrame(255);
  ring.push(evicted, 0);
  for (
    let index = 1;
    index <= BARGE_PCM_LIMITS.maximumFrames;
    index += 1
  ) {
    ring.push(filledPcmFrame(index), index * 20);
  }
  assert.deepEqual(ring.snapshot(), {
    frameCount: BARGE_PCM_LIMITS.maximumFrames,
    newestAt: BARGE_PCM_LIMITS.historyMs,
    oldestAt: 20,
    totalBytes: BARGE_PCM_LIMITS.maximumBytes,
  });
  assert.equal(
    new Uint8Array(evicted).every((value) => value === 0),
    true,
    "an evicted microphone frame must be zeroized",
  );

  const candidateStartedAt = 100;
  const drained = ring.drainSince(
    candidateStartedAt - BARGE_PCM_LIMITS.leadInMs,
  );
  assert.equal(drained[0].capturedAt, 20);
  assert.equal(
    drained.at(-1).capturedAt,
    BARGE_PCM_LIMITS.historyMs,
  );
  assert.equal(drained.length, BARGE_PCM_LIMITS.maximumFrames);
  assert.deepEqual(ring.snapshot(), {
    frameCount: 0,
    newestAt: null,
    oldestAt: null,
    totalBytes: 0,
  });

  const expired = filledPcmFrame(91);
  const cleared = filledPcmFrame(92);
  ring.push(expired, 500);
  ring.push(cleared, 520 + BARGE_PCM_LIMITS.historyMs);
  assert.equal(
    new Uint8Array(expired).every((value) => value === 0),
    true,
    "timestamp eviction must zero audio older than the finite history",
  );
  ring.clear();
  assert.equal(
    new Uint8Array(cleared).every((value) => value === 0),
    true,
    "monitor teardown must zero the retained frame",
  );
});

test("normal live capture sends zero PCM before confirmation and preserves the shortest valid turn in order", () => {
  const sent = [];
  const gate = createConfirmedSpeechPcmGate((frame) => sent.push(frame));
  const candidateStartedAt = 300;
  const confirmedAt =
    candidateStartedAt +
    VOICE_SESSION_LIMITS.minimumVoiceMs -
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (
    let timestamp = 0;
    timestamp <= confirmedAt;
    timestamp += BARGE_PCM_LIMITS.frameDurationMs
  ) {
    gate.push(filledPcmFrame(timestamp / 20 + 1), timestamp);
  }
  assert.equal(sent.length, 0);
  assert.equal(gate.snapshot().confirmed, false);

  assert.equal(gate.confirm(candidateStartedAt), 20);
  assert.equal(sent.length, 20);
  assert.deepEqual(
    sent.map((frame) => new Uint8Array(frame)[0]),
    Array.from({ length: 20 }, (_, index) => index + 1),
  );

  const endpointAt =
    confirmedAt + VOICE_SESSION_LIMITS.endOfTurnSilenceMs;
  for (
    let timestamp =
      confirmedAt + BARGE_PCM_LIMITS.frameDurationMs;
    timestamp <= endpointAt;
    timestamp += BARGE_PCM_LIMITS.frameDurationMs
  ) {
    gate.push(filledPcmFrame(timestamp / 20 + 1), timestamp);
  }
  const expectedFrameCount =
    20 +
    VOICE_SESSION_LIMITS.endOfTurnSilenceMs /
      BARGE_PCM_LIMITS.frameDurationMs;
  assert.equal(sent.length, expectedFrameCount);
  assert.deepEqual(
    sent.map((frame) => new Uint8Array(frame)[0]),
    Array.from({ length: expectedFrameCount }, (_, index) => index + 1),
    "the 300 ms lead-in and shortest valid speech turn must be sent once, in order",
  );
});

test("normal live confirmation zeroizes PCM older than its bounded 300 ms lead-in", () => {
  const sent = [];
  const gate = createConfirmedSpeechPcmGate((frame) => sent.push(frame));
  const frames = new Map();
  const candidateStartedAt = 400;
  for (
    let timestamp = 20;
    timestamp <= candidateStartedAt;
    timestamp += BARGE_PCM_LIMITS.frameDurationMs
  ) {
    const frame = filledPcmFrame(timestamp / 20);
    frames.set(timestamp, frame);
    gate.push(frame, timestamp);
  }
  assert.equal(sent.length, 0);

  assert.equal(gate.confirm(candidateStartedAt), 16);
  assert.deepEqual(
    sent.map((frame) => new Uint8Array(frame)[0]),
    Array.from({ length: 16 }, (_, index) => index + 5),
  );
  for (const timestamp of [20, 40, 60, 80]) {
    assert.equal(
      new Uint8Array(frames.get(timestamp)).every(
        (value) => value === 0,
      ),
      true,
      `PCM captured at ${timestamp} ms must be zeroized`,
    );
  }
  assert.deepEqual(gate.snapshot(), {
    frameCount: 0,
    newestAt: null,
    oldestAt: null,
    totalBytes: 0,
    closed: false,
    confirmed: true,
  });
});

test("normal live capture keeps the full lead-in across bounded VAD jitter", () => {
  const sent = [];
  const gate = createConfirmedSpeechPcmGate((frame) => sent.push(frame));
  const candidateStartedAt = 300;
  const delayedConfirmationAt = 460;
  for (
    let timestamp = 0;
    timestamp <= delayedConfirmationAt;
    timestamp += BARGE_PCM_LIMITS.frameDurationMs
  ) {
    gate.push(filledPcmFrame(timestamp / 20 + 1), timestamp);
  }

  assert.equal(gate.confirm(candidateStartedAt), 24);
  assert.deepEqual(
    sent.map((frame) => new Uint8Array(frame)[0]),
    Array.from({ length: 24 }, (_, index) => index + 1),
  );
});

test("late quiet confirmation preserves its full finite candidate and 300 ms lead-in", () => {
  assert.equal(
    CONFIRMED_SPEECH_PCM_LIMITS.historyMs,
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs +
      VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs,
  );
  assert.equal(CONFIRMED_SPEECH_PCM_LIMITS.maximumFrames, 75);
  assert.equal(
    CONFIRMED_SPEECH_PCM_LIMITS.maximumBytes,
    CONFIRMED_SPEECH_PCM_LIMITS.maximumFrames *
      VOICE_LIVE_LIMITS.inputFrameBytes,
  );

  let now = 0;
  let vadState = createVadState(now);
  let candidateState = createCandidateCaptureState();
  for (let frame = 0; frame < 10; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, { now, peak: 0.004, rms: 0.003 });
  }

  let candidateStartedAt = null;
  for (let frame = 1; frame <= 29; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms =
      frame < 21 ? 0.0065 : frame % 2 === 1 ? 0.0085 : 0.0065;
    vadState = advanceVad(vadState, { now, peak: rms * 2, rms });
    candidateState = advanceCandidateCapture(
      candidateState,
      vadState,
      now,
    );
    if (candidateState.action === "start") {
      candidateStartedAt = candidateState.candidateStartedAt;
    }
  }
  assert.equal(candidateStartedAt, 440);
  assert.equal(candidateState.action, "confirm");
  assert.equal(vadState.softVoiceConfirmed, true);
  assert.ok(
    now - candidateStartedAt <
      VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  );

  const sent = [];
  const gate = createConfirmedSpeechPcmGate((frame) => sent.push(frame));
  for (
    let capturedAt = 0;
    capturedAt < now;
    capturedAt += CONFIRMED_SPEECH_PCM_LIMITS.frameDurationMs
  ) {
    gate.push(filledPcmFrame(capturedAt / 20 + 1), capturedAt);
  }
  gate.confirm(candidateStartedAt);

  const firstExpectedAt =
    candidateStartedAt - VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs;
  assert.equal(new Uint8Array(sent[0])[0], firstExpectedAt / 20 + 1);
  assert.equal(
    sent.length,
    (now - firstExpectedAt) /
      CONFIRMED_SPEECH_PCM_LIMITS.frameDurationMs,
  );
});

test("discarding an unconfirmed live gate zeroizes retained room audio", () => {
  const gate = createConfirmedSpeechPcmGate(() => {
    assert.fail("unconfirmed audio must not leave the device");
  });
  const retained = filledPcmFrame(77);
  gate.push(retained, 10);
  gate.clear();
  assert.equal(
    new Uint8Array(retained).every((value) => value === 0),
    true,
  );
  assert.equal(gate.snapshot().closed, true);
});

test("a failed confirmed-speech sink zeroizes every unreleased PCM frame", () => {
  const first = filledPcmFrame(31);
  const second = filledPcmFrame(32);
  const gate = createConfirmedSpeechPcmGate(() => {
    throw new Error("voice_api_unavailable");
  });
  gate.push(first, 100);
  gate.push(second, 120);
  assert.throws(
    () => gate.confirm(100),
    /voice_api_unavailable/,
  );
  assert.equal(
    new Uint8Array(first).every((value) => value === 0),
    true,
  );
  assert.equal(
    new Uint8Array(second).every((value) => value === 0),
    true,
  );
  assert.equal(gate.snapshot().closed, true);

  const liveFrame = filledPcmFrame(41);
  const confirmedGate = createConfirmedSpeechPcmGate(() => {
    throw new Error("voice_api_unavailable");
  });
  confirmedGate.confirm(0);
  assert.throws(
    () => confirmedGate.push(liveFrame, 140),
    /voice_api_unavailable/,
  );
  assert.equal(
    new Uint8Array(liveFrame).every((value) => value === 0),
    true,
  );
});

test("audible metrics prefer the output-device timestamp then output latency", () => {
  assert.equal(
    estimateAudiblePerformanceTime({
      baseLatencySeconds: 0.2,
      currentContextTime: 12.05,
      outputLatencySeconds: 0.3,
      outputTimestamp: {
        contextTime: 12,
        performanceTime: 1_000,
      },
      performanceNow: 1_010,
      targetContextTime: 12.25,
    }),
    1_250,
    "the device timestamp already incorporates the output path",
  );
  assert.equal(
    estimateAudiblePerformanceTime({
      baseLatencySeconds: 0.2,
      currentContextTime: 2.95,
      outputLatencySeconds: 0.08,
      outputTimestamp: undefined,
      performanceNow: 5_000,
      targetContextTime: 3,
    }),
    5_130,
  );
  assert.equal(
    estimateAudiblePerformanceTime({
      baseLatencySeconds: 0.04,
      currentContextTime: 2.95,
      outputLatencySeconds: undefined,
      outputTimestamp: {
        contextTime: 2.9,
        performanceTime: 20_000,
      },
      performanceNow: 5_000,
      targetContextTime: 3,
    }),
    5_090,
    "a stale timestamp falls back to base latency when outputLatency is absent",
  );
});

test("mock ambient handoff sends old state first then bounded PCM pre-roll", () => {
  const ring = createBargePcmRing();
  for (let timestamp = 300; timestamp <= 420; timestamp += 20) {
    ring.push(filledPcmFrame(timestamp / 20), timestamp);
  }
  const preRoll = ring
    .drainSince(400 - BARGE_PCM_LIMITS.leadInMs)
    .map((entry) => entry.pcm);
  const socket = new MockWebSocket();
  const start = {
    ...liveStartFrame(),
    sessionState: "old-committed-state",
    turnMode: "ambient",
  };
  const transport = createVoiceLiveClientTransport(socket, start);
  for (const frame of preRoll) transport.pushFrame(frame);
  transport.open();
  assert.deepEqual(JSON.parse(socket.sent[0]), start);
  assert.equal(JSON.parse(socket.sent[0]).turnMode, "ambient");
  assert.equal(
    JSON.parse(socket.sent[0]).sessionState,
    "old-committed-state",
  );
  transport.markReady();
  assert.deepEqual(
    socket.sent
      .slice(1)
      .map((frame) => new Uint8Array(frame)[0]),
    [15, 16, 17, 18, 19, 20, 21],
  );
  assert.equal(
    socket.sent.slice(1).every(
      (frame) =>
        frame instanceof ArrayBuffer &&
        frame.byteLength === VOICE_LIVE_LIMITS.inputFrameBytes,
    ),
    true,
  );
  transport.commit();
  assert.deepEqual(JSON.parse(socket.sent[8]), {
    type: "commit",
    version: 1,
  });
});

test("post-final and stale ambient handoffs fail closed to HTTP ownership", () => {
  assert.equal(
    shouldStartAmbientLiveHandoff({
      captureAvailable: true,
      finalReceived: false,
      liveState: "committed",
    }),
    true,
  );
  assert.equal(
    shouldStartAmbientLiveHandoff({
      captureAvailable: true,
      finalReceived: true,
      liveState: "committed",
    }),
    false,
  );
  assert.equal(
    ambientHandoffAssignmentAllowed({
      activeRecordingMatches: true,
      activeSlotEmpty: true,
      currentEpoch: 9,
      expectedEpoch: 9,
      recordingSettled: false,
    }),
    true,
  );
  for (const stale of [
    { currentEpoch: 10 },
    { activeRecordingMatches: false },
    { activeSlotEmpty: false },
    { recordingSettled: true },
  ]) {
    const cancelled = [];
    const nextLiveSession = {
      cancel(error) {
        cancelled.push(error.message);
      },
    };
    assert.equal(
      claimAmbientLiveHandoff(nextLiveSession, {
        activeRecordingMatches: true,
        activeSlotEmpty: true,
        currentEpoch: 9,
        expectedEpoch: 9,
        recordingSettled: false,
        ...stale,
      }),
      undefined,
    );
    assert.deepEqual(cancelled, ["request_cancelled"]);
  }
});

test("mock WebSocket sends auth first then exact 20 ms PCM frames", () => {
  const socket = new MockWebSocket();
  const transport = createVoiceLiveClientTransport(
    socket,
    liveStartFrame(),
  );
  for (let index = 0; index < 7; index += 1) {
    transport.pushFrame(
      new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes),
    );
  }
  transport.open();
  assert.deepEqual(JSON.parse(socket.sent[0]), liveStartFrame());
  transport.markReady();
  assert.equal(socket.sent[1] instanceof ArrayBuffer, true);
  assert.equal(
    socket.sent[1].byteLength,
    VOICE_LIVE_LIMITS.outboundChunkBytes,
  );
  assert.equal(transport.snapshot().queuedFrames, 0);
  assert.equal(
    socket.sent.slice(1).every(
      (frame) =>
        frame instanceof ArrayBuffer &&
        frame.byteLength === VOICE_LIVE_LIMITS.inputFrameBytes,
    ),
    true,
  );

  for (let index = 0; index < 3; index += 1) {
    transport.pushFrame(
      new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes),
    );
  }
  assert.equal(
    socket.sent[8].byteLength,
    VOICE_LIVE_LIMITS.outboundChunkBytes,
  );
  transport.commit();
  assert.deepEqual(JSON.parse(socket.sent[11]), {
    type: "commit",
    version: 1,
  });
});

test("native audio is explicit and cannot weaken strict mode", () => {
  const socket = new MockWebSocket();
  const native = liveStartFrame(true);
  const transport = createVoiceLiveClientTransport(socket, native);
  transport.open();
  assert.deepEqual(JSON.parse(socket.sent[0]), native);
  assert.throws(
    () =>
      createVoiceLiveClientTransport(new MockWebSocket(), {
        ...native,
        strictCloudMinimization: true,
      }),
    /voice_live_start_invalid/,
  );
  assert.throws(
    () =>
      createVoiceLiveClientTransport(new MockWebSocket(), {
        ...native,
        nativeCoachControl: false,
      }),
    /voice_live_start_invalid/,
  );
  assert.throws(
    () =>
      createVoiceLiveClientTransport(new MockWebSocket(), {
        ...liveStartFrame(),
        nativeCoachControl: true,
      }),
    /voice_live_start_invalid/,
  );
});

test("live commit preserves exact frames and fails on backpressure", () => {
  const emptySocket = new MockWebSocket();
  const empty = createVoiceLiveClientTransport(
    emptySocket,
    { ...liveStartFrame(), turnMode: "foreground" },
  );
  empty.open();
  empty.markReady();
  assert.throws(() => empty.commit(), /voice_api_unavailable/);
  assert.equal(emptySocket.sent.length, 1);
  assert.equal(empty.snapshot().inputFrameCount, 0);

  const partialSocket = new MockWebSocket();
  const partial = createVoiceLiveClientTransport(
    partialSocket,
    liveStartFrame(),
  );
  partial.open();
  partial.markReady();
  partial.pushFrame(
    new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes),
  );
  partial.pushFrame(
    new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes),
  );
  partial.commit();
  assert.equal(
    partialSocket.sent[1].byteLength,
    VOICE_LIVE_LIMITS.inputFrameBytes,
  );
  assert.equal(
    partialSocket.sent[2].byteLength,
    VOICE_LIVE_LIMITS.inputFrameBytes,
  );
  assert.deepEqual(JSON.parse(partialSocket.sent[3]), {
    type: "commit",
    version: 1,
  });

  const stalledSocket = new MockWebSocket();
  const stalled = createVoiceLiveClientTransport(
    stalledSocket,
    liveStartFrame(),
  );
  stalled.open();
  stalled.markReady();
  for (let index = 0; index < 5; index += 1) {
    stalled.pushFrame(
      new ArrayBuffer(VOICE_LIVE_LIMITS.inputFrameBytes),
    );
  }
  stalledSocket.bufferedAmount =
    VOICE_LIVE_LIMITS.maximumSocketBufferedBytes;
  const backpressuredFrames = [];
  for (let index = 0; index < 5; index += 1) {
    const frame = filledPcmFrame(101 + index);
    backpressuredFrames.push(frame);
    stalled.pushFrame(frame);
  }
  assert.equal(stalled.snapshot().queuedFrames, 5);
  assert.throws(() => stalled.commit(), /voice_api_unavailable/);
  assert.equal(stalled.snapshot().queuedFrames, 0);
  assert.equal(stalled.snapshot().state, "closed");
  assert.equal(
    backpressuredFrames.every((frame) =>
      new Uint8Array(frame).every((value) => value === 0),
    ),
    true,
  );
});

test("live server protocol gates binary on ready and commit", () => {
  const protocol = createVoiceLiveServerProtocol(
    (result) => Object.freeze({ ...result }),
    { coachActive: false, nativeAudio: true },
  );
  assert.throws(
    () => protocol.acceptBinary(new ArrayBuffer(4)),
    /voice_response_invalid/,
  );
  assert.deepEqual(
    protocol.acceptText(JSON.stringify({ type: "ready", version: 1 })),
    { type: "ready", version: 1 },
  );
  assert.deepEqual(
    protocol.acceptText(
      JSON.stringify({ type: "endpoint", version: 1 }),
    ),
    { type: "endpoint", version: 1 },
  );
  assert.throws(
    () =>
      protocol.acceptText(
        JSON.stringify({ type: "endpoint", version: 1 }),
      ),
    /voice_response_invalid/,
  );
  protocol.markCommitted();
  const audio = protocol.acceptBinary(new ArrayBuffer(4));
  assert.equal(audio.sequence, 0);
  assert.equal(audio.sampleRateHz, 24_000);
  const final = protocol.acceptText(
    JSON.stringify({
      type: "final",
      version: 1,
      result: finalVoiceResult(),
    }),
  );
  assert.equal(final.type, "final");
  assert.equal(protocol.snapshot().totalAudioBytes, 4);
  assert.throws(
    () =>
      protocol.acceptText(
        JSON.stringify({ type: "ready", version: 1 }),
      ),
    /voice_response_invalid/,
  );
});

test("a committed native turn replays only on reviewed zero-audio failures", () => {
  const eligible = {
    audioEventCount: 0,
    coachActivated: false,
    code: "voice_native_fallback",
    committed: true,
    interrupted: false,
    nativeAudio: true,
  };
  assert.equal(shouldReplayCommittedNativeTurn(eligible), true);
  assert.equal(
    shouldReplayCommittedNativeTurn({
      ...eligible,
      code: "voice_api_unavailable",
    }),
    true,
  );
  for (const unsafe of [
    { ...eligible, audioEventCount: 1 },
    { ...eligible, coachActivated: true },
    { ...eligible, code: "voice_response_invalid" },
    { ...eligible, committed: false },
    { ...eligible, interrupted: true },
    { ...eligible, nativeAudio: false },
    { ...eligible, audioEventCount: 1, code: "voice_api_unavailable" },
    { ...eligible, coachActivated: true, code: "voice_api_unavailable" },
    { ...eligible, committed: false, code: "voice_api_unavailable" },
    { ...eligible, interrupted: true, code: "voice_api_unavailable" },
    { ...eligible, nativeAudio: false, code: "voice_api_unavailable" },
  ]) {
    assert.equal(shouldReplayCommittedNativeTurn(unsafe), false);
  }
  assert.throws(
    () => shouldReplayCommittedNativeTurn({ ...eligible, audioEventCount: -1 }),
    /native_fallback_state_invalid/u,
  );
});

test("a first-turn Native coach preserves authenticated state without replaying the utterance", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const liveStart = bridge.indexOf("async function startVoiceLiveSession(");
  const liveEnd = bridge.indexOf("\n}\n\nfunction isNdjsonContentType", liveStart);
  assert.notEqual(liveStart, -1);
  assert.notEqual(liveEnd, -1);
  const live = bridge.slice(liveStart, liveEnd);
  assert.match(
    live,
    /createVoiceLiveServerProtocol\([\s\S]*safeVoiceResponse\(result, strictCloudMinimization\)[\s\S]*\{ coachActive, nativeAudio \},\s*\);/u,
  );

  assert.match(
    live,
    /message\.type === "coach"[\s\S]*state !== "committed"[\s\S]*session\.playback\.activateCoach\(\)/u,
  );
  const coachStart = live.indexOf('if (message.type === "coach")');
  const finalStart = live.indexOf('if (message.type === "final")', coachStart);
  assert.ok(coachStart >= 0);
  assert.ok(finalStart > coachStart);
  const coachBranch = live.slice(coachStart, finalStart);
  assert.match(
    coachBranch,
    /detail: Object\.freeze\(\{[\s\S]*assistanceTarget: message\.assistanceTarget,[\s\S]*coachAction: message\.coachAction,[\s\S]*coachPhase: message\.coachPhase,[\s\S]*respondentStage: message\.respondentStage,[\s\S]*route: message\.route,[\s\S]*sessionState: message\.sessionState,[\s\S]*version: 1/u,
  );
  assert.ok(
    coachBranch.indexOf("session.playback.activateCoach();") <
      coachBranch.indexOf("globalThis.dispatchEvent("),
  );
  assert.ok(
    coachBranch.indexOf("globalThis.dispatchEvent(") <
      coachBranch.lastIndexOf("return;"),
    "the exact checkpoint event is committed synchronously before audio handling",
  );
  assert.match(
    live,
    /coachActivated: snapshot\.coachActivated/u,
  );
  assert.match(
    live,
    /const audioEvent = protocol\.acceptBinary\(event\.data\);[\s\S]*session\.playback\.interrupted[\s\S]*session\.playback\.coachActive[\s\S]*new Uint8Array\(audioEvent\.pcm\)\.fill\(0\);[\s\S]*return;/u,
  );
  assert.match(
    live,
    /session\.playback\.finalReceived = true;\s*if \(!session\.playback\.interrupted\) \{\s*session\.playback\.seal\(\);/u,
  );
  assert.match(
    live,
    /requiresStatefulLiveDrain\(\)[\s\S]*state === "committed"[\s\S]*session\.playback\?\.coachActive === true[\s\S]*session\.playback\.finalReceived === false/u,
  );

  const confirmStart = bridge.indexOf("function confirmBargeIn(");
  const confirmEnd = bridge.indexOf(
    "\n}\n\nfunction startBargeInMonitoring(",
    confirmStart,
  );
  const confirm = bridge.slice(confirmStart, confirmEnd);
  assert.match(
    confirm,
    /const preserveLiveCoachState = Boolean\([\s\S]*requiresStatefulLiveDrain\(\)/u,
  );
  assert.match(
    confirm,
    /const handoffPromise =\s*!preserveLiveCoachState[\s\S]*interruptedLiveSession\.handoffAmbient/u,
  );
  assert.match(
    confirm,
    /if \(!preserveLiveCoachState\) \{\s*interruptedLiveSession\?\.interrupt\(interruption\);\s*\}/u,
  );
  assert.match(confirm, /haltStreamingPlayback\(playback, interruption\)/u);

  const recordingStart = bridge.indexOf("function createRecordingState(");
  const recordingEnd = bridge.indexOf("\n}\n\nfunction createRecording(", recordingStart);
  const recording = bridge.slice(recordingStart, recordingEnd);
  assert.doesNotMatch(recording, /nativeAudio && coachActive/u);

  const finishStart = bridge.indexOf("async function finishTurn(");
  const finishEnd = bridge.indexOf("\n}\n\nfunction safeDocumentName", finishStart);
  const finish = bridge.slice(finishStart, finishEnd);
  assert.match(
    finish,
    /shouldDiscardInterruptedPlaybackRecording\(playback\)[\s\S]*discardInterruptedPlaybackRecording\(playback\)/u,
  );
});

test("Rust accepts only the exact coach checkpoint and preserves it across a recoverable turn", async () => {
  const client = await readFile(
    new URL("../src/main.rs", import.meta.url),
    "utf8",
  );
  const listenerStart = client.indexOf(
    "pub fn install_coach_checkpoint_listener(",
  );
  const listenerEnd = client.indexOf("\n    pub fn end_turn()", listenerStart);
  assert.ok(listenerStart >= 0);
  assert.ok(listenerEnd > listenerStart);
  const listener = client.slice(listenerStart, listenerEnd);
  assert.match(listener, /js_sys::Object::keys\(detail_object\)/u);
  assert.match(listener, /valid_coach_checkpoint_keys\(&key_names\)/u);
  assert.match(
    listener,
    /valid_coach_checkpoint_metadata\([\s\S]*&checkpoint,[\s\S]*&checkpoint_route,[\s\S]*&assistance_target,[\s\S]*&respondent_stage,[\s\S]*coach_phase,[\s\S]*coach_action,[\s\S]*version,[\s\S]*keys\.length\(\)/u,
  );
  const sessionAt = listener.indexOf("session_state.set(checkpoint);");
  const routeAt = listener.indexOf("route.set(checkpoint_route);");
  const coachAt = listener.indexOf("coach_state.set(CoachState::from_result(");
  assert.ok(sessionAt >= 0);
  assert.ok(routeAt > sessionAt);
  assert.ok(coachAt > routeAt);
  assert.match(listener.slice(coachAt), /coach_phase, coach_action/u);
  assert.doesNotMatch(
    listener.slice(coachAt),
    /CoachPhase::AwaitingAnswer|CoachAction::Elicit/u,
  );
  assert.match(
    listener,
    /add_event_listener_with_callback\(\s*"kotae:coach-checkpoint"/u,
  );

  const armStart = client.indexOf("fn arm_listening(");
  const armEnd = client.indexOf("\nfn resume_foreground_interruption(", armStart);
  const arm = client.slice(armStart, armEnd);
  assert.match(
    arm,
    /coach_state\.peek\(\)\.requires_staged_route\(\)[\s\S]*session_state\.peek\(\)\.clone\(\)[\s\S]*cloud::begin_turn\([\s\S]*&state_snapshot,[\s\S]*coach_active_snapshot/u,
  );

  const submitStart = client.indexOf("fn submit_turn(");
  const submitEnd = client.indexOf("\nfn start_or_resume(", submitStart);
  const submit = client.slice(submitStart, submitEnd);
  const recoverableStart = submit.indexOf(
    "Err(FinishTurnError::Recoverable(_message))",
  );
  const terminalStart = submit.indexOf(
    "Err(FinishTurnError::Message(message))",
    recoverableStart,
  );
  assert.ok(recoverableStart >= 0);
  assert.ok(terminalStart > recoverableStart);
  const recoverable = submit.slice(recoverableStart, terminalStart);
  assert.match(recoverable, /arm_listening\(/u);
  assert.doesNotMatch(
    recoverable,
    /session_state\.set|route\.set|coach_state\.set|cloud::stop_session/u,
  );
});

test("a Native coach control is exact, one-shot, and precedes response audio", () => {
  const checkpoint = "authenticated-coach-checkpoint";
  const coachControl = Object.freeze({
    type: "coach",
    active: true,
    assistanceTarget: "respondent",
    coachAction: "elicit",
    coachPhase: "awaiting_answer",
    respondentStage: "awaiting_answer",
    route: "native-respondent-coach",
    sessionState: checkpoint,
    version: 1,
  });
  const coachFinal = (overrides = {}) => ({
    ...finalVoiceResult(),
    assistanceTarget: "respondent",
    coachAction: "elicit",
    coachPhase: "awaiting_answer",
    respondentStage: "awaiting_answer",
    route: "native-respondent-coach",
    sessionState: checkpoint,
    ...overrides,
  });
  const createCommitted = (
    nativeAudio = true,
    coachActive = false,
  ) => {
    const protocol = createVoiceLiveServerProtocol(
      (result) => result,
      { coachActive, nativeAudio },
    );
    protocol.acceptText(JSON.stringify({ type: "ready", version: 1 }));
    protocol.markCommitted();
    return protocol;
  };

  for (const expectations of [
    undefined,
    null,
    {},
    { coachActive: false, nativeAudio: 1 },
    { coachActive: 1, nativeAudio: true },
    { coachActive: true, nativeAudio: false },
    { coachActive: false, nativeAudio: true, extra: true },
  ]) {
    assert.throws(
      () => createVoiceLiveServerProtocol((result) => result, expectations),
      /voice_live_protocol_expectation_invalid/u,
    );
  }

  const protocol = createCommitted();
  assert.deepEqual(
    protocol.acceptText(JSON.stringify(coachControl)),
    coachControl,
  );
  assert.equal(protocol.snapshot().coachActivated, true);
  assert.throws(
    () => protocol.acceptText(JSON.stringify(coachControl)),
    /voice_response_invalid/u,
  );

  for (const invalid of [
    { ...coachControl, active: false },
    { ...coachControl, version: 2 },
    { ...coachControl, phase: "elicit" },
    { ...coachControl, assistanceTarget: "assistant" },
    { ...coachControl, respondentStage: "none" },
    { ...coachControl, route: "native-audio" },
    { ...coachControl, coachPhase: "awaiting_answer", coachAction: "retry" },
    { ...coachControl, sessionState: "" },
    { ...coachControl, sessionState: ` ${checkpoint}` },
    { ...coachControl, sessionState: `${checkpoint}\n` },
    { ...coachControl, sessionState: `${checkpoint}\u007f` },
    { ...coachControl, sessionState: `\u0085${checkpoint}` },
    {
      ...coachControl,
      sessionState: "x".repeat(
        VOICE_LIVE_LIMITS.maximumSessionStateCharacters + 1,
      ),
    },
  ]) {
    const candidate = createCommitted();
    assert.throws(
      () => candidate.acceptText(JSON.stringify(invalid)),
      /voice_response_invalid/u,
    );
  }

  const beforeCommit = createVoiceLiveServerProtocol(
    (result) => result,
    { coachActive: false, nativeAudio: true },
  );
  beforeCommit.acceptText(JSON.stringify({ type: "ready", version: 1 }));
  assert.throws(
    () => beforeCommit.acceptText(JSON.stringify(coachControl)),
    /voice_response_invalid/u,
  );

  const afterAudio = createCommitted();
  afterAudio.acceptBinary(new ArrayBuffer(4));
  assert.throws(
    () => afterAudio.acceptText(JSON.stringify(coachControl)),
    /voice_response_invalid/u,
  );

  const staged = createCommitted(false, false);
  assert.throws(
    () => staged.acceptText(JSON.stringify(coachControl)),
    /voice_response_invalid/u,
  );

  const completed = createCommitted();
  completed.acceptText(JSON.stringify(coachControl));
  completed.acceptBinary(new ArrayBuffer(4));
  assert.equal(
    completed.acceptText(
      JSON.stringify({
        type: "final",
        version: 1,
        result: coachFinal(),
      }),
    ).type,
    "final",
  );

  for (const mismatch of [
    { sessionState: `${checkpoint}-mismatch` },
    { route: "native-audio" },
    { assistanceTarget: "assistant" },
    { respondentStage: "restructure" },
    { coachPhase: "complete" },
    { coachAction: "complete" },
  ]) {
    const candidate = createCommitted();
    candidate.acceptText(JSON.stringify(coachControl));
    candidate.acceptBinary(new ArrayBuffer(4));
    assert.throws(
      () =>
        candidate.acceptText(
          JSON.stringify({
            type: "final",
            version: 1,
            result: coachFinal(mismatch),
          }),
        ),
      /voice_response_invalid/u,
    );
  }

  const unannouncedNativeCoach = createCommitted();
  unannouncedNativeCoach.acceptBinary(new ArrayBuffer(4));
  assert.throws(
    () =>
      unannouncedNativeCoach.acceptText(
        JSON.stringify({
          type: "final",
          version: 1,
          result: coachFinal(),
        }),
      ),
    /voice_response_invalid/u,
  );

  const stagedRespondent = createCommitted(false, false);
  stagedRespondent.acceptBinary(new ArrayBuffer(4));
  assert.equal(
    stagedRespondent.acceptText(
      JSON.stringify({
        type: "final",
        version: 1,
        result: coachFinal(),
      }),
    ).type,
    "final",
  );

  const knownActiveCoach = createCommitted(true, true);
  assert.throws(
    () => knownActiveCoach.acceptBinary(new ArrayBuffer(4)),
    /voice_response_invalid/u,
    "known Coach response PCM must not precede its exact checkpoint",
  );
  assert.deepEqual(
    knownActiveCoach.acceptText(JSON.stringify(coachControl)),
    coachControl,
  );
  assert.equal(
    knownActiveCoach.acceptBinary(new ArrayBuffer(4)).sequence,
    0,
  );

  const expandingControl = Object.freeze({
    ...coachControl,
    coachAction: "expand",
    coachPhase: "expanding",
    respondentStage: "restructure",
  });
  const expanding = createCommitted(true, true);
  assert.deepEqual(
    expanding.acceptText(JSON.stringify(expandingControl)),
    expandingControl,
  );
  expanding.acceptBinary(new ArrayBuffer(4));
  assert.equal(
    expanding.acceptText(
      JSON.stringify({
        type: "final",
        version: 1,
        result: coachFinal({
          coachAction: "expand",
          coachPhase: "expanding",
          respondentStage: "restructure",
        }),
      }),
    ).type,
    "final",
  );
});

test("hybrid endpoint requires provider and local silence agreement", () => {
  const short = {
    firstVoiceAt: 100,
    hasSpeech: true,
    lastVoiceAt: 1_000,
    providerEndpointAt: 1_300,
  };
  assert.equal(
    shouldCommitHybridEndpoint({ ...short, now: 2_199 }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({ ...short, now: 2_200 }),
    true,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      nativeAudio: true,
      now: 1_399,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      nativeAudio: true,
      now: 1_400,
    }),
    true,
    "native input is already streaming and can use a 400 ms agreement",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      coachActive: true,
      now: 1_439,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      coachActive: true,
      now: 1_440,
    }),
    true,
    "a short clear active-coach answer can use a 440 ms agreement",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      lastVoiceAt: 1_350,
      now: 1_800,
    }),
    false,
    "speech resumed after the provider endpoint",
  );
  assert.equal(
    shouldCommitHybridEndpoint({ ...short, now: 3_301 }),
    false,
    "a stale provider endpoint cannot stop a later thought",
  );

  const reflective = {
    firstVoiceAt: 100,
    hasSpeech: true,
    lastVoiceAt: 2_500,
    providerEndpointAt: 2_900,
  };
  assert.equal(
    shouldCommitHybridEndpoint({
      ...reflective,
      coachActive: true,
      now: 4_699,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...reflective,
      coachActive: true,
      now: 4_700,
    }),
    true,
  );

  assert.equal(
    shouldCommitHybridEndpoint({
      ...reflective,
      nativeAudio: true,
      providerEndpointAt: 2_700,
      now: 2_899,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...reflective,
      nativeAudio: true,
      providerEndpointAt: 2_700,
      now: 2_900,
    }),
    true,
    "clear Native speech keeps the 400 ms agreement at reflective length",
  );

  const softVoice = {
    firstVoiceAt: 600,
    hasSpeech: true,
    lastVoiceAt: 1_000,
    providerEndpointAt: 1_300,
    softVoiceConfirmed: true,
  };
  assert.equal(
    shouldCommitHybridEndpoint({
      ...softVoice,
      coachActive: true,
      now: 3_999,
    }),
    false,
    "a confirmed quiet speaker keeps a three-second thinking pause",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...softVoice,
      coachActive: true,
      now: 4_000,
    }),
    true,
  );
  assert.equal(
    shouldCommitHybridEndpoint({ ...softVoice, now: 4_801 }),
    false,
    "soft-voice provider agreement is longer but still finite",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...softVoice,
      nativeAudio: true,
      now: 3_999,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...softVoice,
      nativeAudio: true,
      now: 4_000,
    }),
    true,
    "confirmed quiet Native speech retains its three-second pause",
  );

  const monologue = {
    firstVoiceAt: 100,
    hasSpeech: true,
    lastVoiceAt: 12_100,
    providerEndpointAt: 12_500,
  };
  assert.equal(
    shouldCommitHybridEndpoint({
      ...monologue,
      coachActive: true,
      now: 17_099,
    }),
    false,
    "a long monologue may continue after a reflective pause",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...monologue,
      coachActive: true,
      now: 17_100,
    }),
    true,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...monologue,
      nativeAudio: true,
      now: 17_099,
    }),
    false,
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...monologue,
      nativeAudio: true,
      now: 17_100,
    }),
    true,
    "a Native monologue retains its five-second continuation window",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      coachActive: true,
      nativeAudio: true,
      now: 1_439,
    }),
    false,
    "dynamic Native-to-Coach promotion must not regress to 400 ms",
  );
  assert.equal(
    shouldCommitHybridEndpoint({
      ...short,
      coachActive: true,
      nativeAudio: true,
      now: 1_440,
    }),
    true,
  );
});

test("bridge applies the bounded coach override to local endpointing", async () => {
  const source = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  assert.match(
    source,
    /softVoiceConfirmed: recording\.softVoiceConfirmed/u,
  );
  assert.match(
    source,
    /coachActive: recording\.coachActive/u,
  );
  assert.match(
    source,
    /recording\.softVoiceConfirmed = vadState\.softVoiceConfirmed/u,
  );
  const armVadStart = source.indexOf("function armVad(recording)");
  const armVadEnd = source.indexOf(
    "\n\nfunction createRecordingState(",
    armVadStart,
  );
  assert.ok(armVadStart >= 0 && armVadEnd > armVadStart);
  assert.match(
    source.slice(armVadStart, armVadEnd),
    /coachActive: recording\.coachActive[\s\S]*VOICE_SESSION_LIMITS\.coachEndOfTurnSilenceMs/u,
    "active coach continuation must receive the bounded short override",
  );
  assert.match(
    source.slice(armVadStart, armVadEnd),
    /endOfTurnSilenceMs:\s*VOICE_SESSION_LIMITS\.nativeAudioEndOfTurnSilenceMs,[\s\S]*reflectiveEndOfTurnSilenceMs:\s*VOICE_SESSION_LIMITS\.nativeAudioEndOfTurnSilenceMs/u,
    "clear Native speech must use 520 ms locally at ordinary and reflective lengths",
  );
});

test("live endpoint is rejected before ready and ignored after commit", () => {
  const beforeReady = createVoiceLiveServerProtocol(
    (result) => result,
    { coachActive: false, nativeAudio: true },
  );
  assert.throws(
    () =>
      beforeReady.acceptText(
        JSON.stringify({ type: "endpoint", version: 1 }),
      ),
    /voice_response_invalid/,
  );

  const afterCommit = createVoiceLiveServerProtocol(
    (result) => result,
    { coachActive: false, nativeAudio: true },
  );
  afterCommit.acceptText(JSON.stringify({ type: "ready", version: 1 }));
  afterCommit.markCommitted();
  assert.deepEqual(
    afterCommit.acceptText(
      JSON.stringify({ type: "endpoint", version: 1 }),
    ),
    { type: "endpoint", version: 1 },
  );
  assert.equal(afterCommit.snapshot().endpointReceived, true);
  assert.throws(
    () =>
      afterCommit.acceptText(
        JSON.stringify({ type: "endpoint", version: 1 }),
      ),
    /voice_response_invalid/,
  );

  const afterAudio = createVoiceLiveServerProtocol(
    (result) => result,
    { coachActive: false, nativeAudio: true },
  );
  afterAudio.acceptText(JSON.stringify({ type: "ready", version: 1 }));
  afterAudio.markCommitted();
  afterAudio.acceptBinary(new ArrayBuffer(4));
  assert.throws(
    () =>
      afterAudio.acceptText(
        JSON.stringify({ type: "endpoint", version: 1 }),
      ),
    /voice_response_invalid/,
  );
});

test("live bridge keeps credentials out of URL and latency detail", async () => {
  const [bridge, worklet] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(
      new URL("../web/pcm-capture-worklet.js", import.meta.url),
      "utf8",
    ),
  ]);
  assert.match(
    bridge,
    /wss:\/\/kotae-api-r6kgkvtrmq-an\.a\.run\.app\/api\/v1\/voice\/live/u,
  );
  assert.doesNotMatch(
    bridge,
    /wss:\/\/kotae-api-r6kgkvtrmq-an\.a\.run\.app\/api\/v1\/voice\/live\?/u,
  );
  assert.match(bridge, /new WebSocket\(VOICE_LIVE_ENDPOINT\)/u);
  const liveAt = bridge.indexOf("async function startVoiceLiveSession(");
  const liveSession = bridge.slice(liveAt, liveAt + 30_000);
  assert.ok(
    liveSession.indexOf("new WebSocket(VOICE_LIVE_ENDPOINT)") <
      liveSession.indexOf("loadPcmCaptureWorklet(audioContext)"),
    "the WSS handshake must overlap AudioWorklet loading",
  );
  assert.match(
    liveSession,
    /readyTimeoutMs\s*-\s*\(performance\.now\(\)\s*-\s*liveStartedAt\)/u,
  );
  assert.match(
    liveSession,
    /authReadyTimer\s*=\s*setTimeout\(/u,
  );
  const metricAt = bridge.indexOf("function dispatchVoiceLatency(");
  const metricEnd = bridge.indexOf("function isPlainRecord(", metricAt);
  const metric = bridge.slice(metricAt, metricEnd);
  assert.match(metric, /ws_open_ms/u);
  assert.match(metric, /auth_ready_ms/u);
  assert.match(metric, /commit_to_first_audio_ms/u);
  assert.match(metric, /commit_to_estimated_audible_ms/u);
  assert.match(metric, /speech_end_to_estimated_audible_ms/u);
  assert.match(metric, /substantive_audio:\s*substantiveAudio/u);
  assert.match(
    metric,
    /speech_end_to_estimated_audible_ms:[\s\S]*substantiveAudio &&[\s\S]*Number\.isFinite\(speechEndToEstimatedAudibleMs\)[\s\S]*:\s*null/u,
  );
  assert.match(metric, /turn_total_ms/u);
  assert.match(metric, /barge_halt_ms/u);
  assert.doesNotMatch(
    metric,
    /idToken|appCheckToken|sessionState|audioBase64|caption|presence/u,
  );
  const receiptAt = bridge.indexOf("function setVoiceReceiptVisible(");
  const receiptEnd = bridge.indexOf(
    "function nextPcmCaptureGeneration()",
    receiptAt,
  );
  const receipt = bridge.slice(receiptAt, receiptEnd);
  assert.doesNotMatch(
    receipt,
    /dispatchVoiceLatency|speech_end_to_estimated_audible/u,
    "a content-free receipt must never masquerade as substantive audio",
  );

  assert.match(worklet, /OUTPUT_SAMPLE_RATE_HZ = 16_000/u);
  assert.match(worklet, /FRAME_BYTES = FRAME_SAMPLES \* 2/u);
  assert.match(worklet, /setInt16\(this\.frameOffset, pcm, true\)/u);
  assert.match(worklet, /\[completed\]/u);
  assert.match(worklet, /weighted \/ this\.ratio/u);
  const audioContextAt = bridge.indexOf("function createAudioContext()");
  const audioContextFactory = bridge.slice(
    audioContextAt,
    audioContextAt + 700,
  );
  assert.doesNotMatch(audioContextFactory, /sampleRate:\s*16_000/u);
  assert.match(audioContextFactory, /latencyHint:\s*"interactive"/u);
  const microphoneAt = bridge.indexOf(
    "navigator.mediaDevices.getUserMedia",
  );
  const microphoneConstraints = bridge.slice(
    microphoneAt,
    microphoneAt + 500,
  );
  assert.match(microphoneConstraints, /autoGainControl:\s*true/u);
  assert.match(microphoneConstraints, /noiseSuppression:\s*true/u);
  assert.match(microphoneConstraints, /echoCancellation:\s*true/u);
  assert.match(bridge, /let pendingLiveSession/u);
  assert.match(
    bridge,
    /expectedPending = pendingLiveSession/u,
  );
  assert.match(
    bridge,
    /pendingLiveSession === pending/u,
  );
  assert.match(
    bridge,
    /activeRecording === recording/u,
  );
  const endpointAt = liveSession.indexOf(
    'if (message.type === "endpoint")',
  );
  const endpointHandler = liveSession.slice(endpointAt, endpointAt + 900);
  assert.ok(endpointAt >= 0, "live endpoint handler must exist");
  assert.match(
    endpointHandler,
    /if \(state === "committed"\) \{\s*return;\s*\}/u,
  );
  assert.ok(
    endpointHandler.indexOf('state === "committed"') <
      endpointHandler.indexOf('state !== "ready"'),
    "an in-flight endpoint advisory must be ignored after local commit",
  );
});

function advancePastInterruptGuard(state, startedAt) {
  let next = state;
  for (
    let now = startedAt + INTERRUPT_VAD_LIMITS.intervalMs;
    now <= startedAt + INTERRUPT_VAD_LIMITS.guardMs;
    now += INTERRUPT_VAD_LIMITS.intervalMs
  ) {
    next = advanceInterruptVad(next, {
      now,
      outputActive: true,
      peak: 0.02,
      rms: 0.006,
    });
  }
  return next;
}

test("interrupt VAD ignores guarded playback and rejects short echo bursts", () => {
  const startedAt = 10_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  assert.equal(state.phase, "armed");

  for (let sample = 1; sample <= 8; sample += 1) {
    state = advanceInterruptVad(state, {
      now:
        startedAt +
        INTERRUPT_VAD_LIMITS.guardMs +
        sample * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.04,
      rms: 0.015,
    });
    assert.equal(state.action, null);
    assert.notEqual(state.phase, "confirmed");
  }

  const candidateAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    9 * INTERRUPT_VAD_LIMITS.intervalMs;
  state = advanceInterruptVad(state, {
    now: candidateAt,
    outputActive: true,
    peak: 0.14,
    rms: 0.05,
  });
  assert.equal(state.action, "start");
  const gapFrames =
    INTERRUPT_VAD_LIMITS.candidateGapMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let gap = 1; gap <= gapFrames; gap += 1) {
    state = advanceInterruptVad(state, {
      now: candidateAt + gap * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.04,
      rms: 0.015,
    });
    if (gap < gapFrames) {
      assert.equal(state.action, null);
      assert.equal(state.phase, "candidate");
    }
  }
  assert.equal(state.action, "discard");
  assert.equal(state.phase, "armed");
});

test("interrupt capture starts inside the guard and retains its first frame", () => {
  const startedAt = 15_000;
  let state = createInterruptVadState(startedAt);
  state = advanceInterruptVad(state, {
    now: startedAt + 40,
    outputActive: false,
    peak: 0.01,
    rms: 0.004,
  });
  const firstVoiceAt = startedAt + 80;
  for (
    let now = firstVoiceAt;
    now <=
    firstVoiceAt +
      (INTERRUPT_VAD_LIMITS.confirmationMs /
          INTERRUPT_VAD_LIMITS.intervalMs -
        1) *
        INTERRUPT_VAD_LIMITS.intervalMs;
    now += INTERRUPT_VAD_LIMITS.intervalMs
  ) {
    state = advanceInterruptVad(state, {
      now,
      outputActive: false,
      peak: 0.15,
      rms: 0.05,
    });
    if (now === firstVoiceAt) assert.equal(state.action, "start");
  }
  assert.equal(state.action, "confirm");
  assert.equal(state.firstVoiceAt, firstVoiceAt);
  assert.equal(state.candidateStartedAt, firstVoiceAt);
});

test("interrupt VAD confirms foreground speech after sustained proof", () => {
  assert.equal(INTERRUPT_VAD_LIMITS.confirmationMs, 720);
  assert.equal(INTERRUPT_VAD_LIMITS.quietConfirmationMs, 1_200);
  assert.equal(INTERRUPT_VAD_LIMITS.trailingSilenceMs, 1_200);
  assert.equal(INTERRUPT_VAD_LIMITS.reflectiveSilenceMs, 2_200);
  const startedAt = 20_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  const firstVoiceAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    INTERRUPT_VAD_LIMITS.intervalMs;
  const confirmationFrames =
    INTERRUPT_VAD_LIMITS.confirmationMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  const provisionalFrames =
    INTERRUPT_VAD_LIMITS.provisionalMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  assert.equal(provisionalFrames, 4);
  assert.equal(confirmationFrames, 18);

  for (let frame = 0; frame < confirmationFrames; frame += 1) {
    state = advanceInterruptVad(state, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.15,
      rms: 0.05,
    });
    if (frame === 0) assert.equal(state.action, "start");
    if (frame === provisionalFrames - 1) {
      assert.equal(state.action, "provisional");
      assert.equal(state.phase, "provisional");
      assert.equal(state.firstVoiceAt, null);
    }
  }
  assert.equal(state.action, "confirm");
  assert.equal(state.phase, "confirmed");
  assert.equal(state.firstVoiceAt, firstVoiceAt);
  assert.equal(
    state.lastVoiceAt - state.firstVoiceAt,
    INTERRUPT_VAD_LIMITS.confirmationMs -
      INTERRUPT_VAD_LIMITS.intervalMs,
  );

  state = advanceInterruptVad(state, {
    now:
      state.lastVoiceAt +
      INTERRUPT_VAD_LIMITS.reflectiveSilenceMs -
      1,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceInterruptVad(state, {
    now:
      state.lastVoiceAt + INTERRUPT_VAD_LIMITS.reflectiveSilenceMs,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("sustained quiet speech confirms only on the longer floor", () => {
  const startedAt = 30_000;
  let now = startedAt + INTERRUPT_VAD_LIMITS.guardMs;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  let candidateStartedAt = null;
  const quietFrames =
    INTERRUPT_VAD_LIMITS.quietConfirmationMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let frame = 1; frame <= quietFrames; frame += 1) {
    now += INTERRUPT_VAD_LIMITS.intervalMs;
    state = advanceInterruptVad(state, {
      now,
      outputActive: true,
      peak: 0.075,
      rms: 0.03,
    });
    if (state.action === "start") {
      candidateStartedAt = state.candidateStartedAt;
    }
    if (frame < quietFrames) {
      assert.notEqual(state.action, "confirm");
    }
  }

  assert.equal(state.action, "confirm");
  assert.equal(state.foregroundVoiceMs, 0);
  assert.equal(state.voiceRunMs, INTERRUPT_VAD_LIMITS.quietConfirmationMs);
  assert.equal(
    now - candidateStartedAt,
    INTERRUPT_VAD_LIMITS.quietConfirmationMs -
      INTERRUPT_VAD_LIMITS.intervalMs,
  );
  assert.ok(
    state.voiceRunMs /
      (now - candidateStartedAt + INTERRUPT_VAD_LIMITS.intervalMs) >=
      INTERRUPT_VAD_LIMITS.minimumVoiceDensity,
  );
  assert.ok(
    now - candidateStartedAt <
      INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs,
  );
  assert.equal(INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs, 2_400);
  assert.equal(
    BARGE_PCM_LIMITS.historyMs,
    INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs +
      BARGE_PCM_LIMITS.leadInMs,
  );
  assert.equal(
    BARGE_PCM_LIMITS.maximumFrames,
    BARGE_PCM_LIMITS.historyMs / BARGE_PCM_LIMITS.frameDurationMs,
  );
});

test("quiet floor uses continuation hysteresis across natural gaps", () => {
  const startedAt = 35_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  let now = startedAt + INTERRUPT_VAD_LIMITS.guardMs;
  let voicedFrames = 0;
  const gapFrames = new Set([9, 10, 21, 22]);

  for (let frame = 1; frame <= 34; frame += 1) {
    now += INTERRUPT_VAD_LIMITS.intervalMs;
    const gap = gapFrames.has(frame);
    state = advanceInterruptVad(state, {
      now,
      outputActive: true,
      // The onset clears the regular floor. Later syllables sit below that
      // onset floor but above the 0.86 continuation floor.
      peak: gap ? 0.003 : frame === 1 ? 0.075 : 0.057,
      rms: gap ? 0.003 : frame === 1 ? 0.03 : 0.023,
    });
    if (!gap) voicedFrames += 1;
    if (gap) {
      assert.equal(state.phase, "provisional");
      assert.notEqual(state.action, "discard");
    }
  }

  assert.equal(
    voicedFrames * INTERRUPT_VAD_LIMITS.intervalMs,
    INTERRUPT_VAD_LIMITS.quietConfirmationMs,
  );
  assert.equal(state.action, "confirm");
  assert.equal(state.foregroundVoiceMs, 0);
  assert.ok(
    state.voiceRunMs /
      (now - state.candidateStartedAt + INTERRUPT_VAD_LIMITS.intervalMs) >=
      INTERRUPT_VAD_LIMITS.minimumVoiceDensity,
  );
});

test("a short mutter is only provisional and is discarded without interruption", () => {
  const startedAt = 40_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  const firstVoiceAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    INTERRUPT_VAD_LIMITS.intervalMs;
  const provisionalFrames =
    INTERRUPT_VAD_LIMITS.provisionalMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let frame = 0; frame < provisionalFrames; frame += 1) {
    state = advanceInterruptVad(state, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.15,
      rms: 0.05,
    });
  }
  assert.equal(state.action, "provisional");
  assert.equal(state.phase, "provisional");

  const gapFrames =
    INTERRUPT_VAD_LIMITS.candidateGapMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let gap = 1; gap <= gapFrames; gap += 1) {
    state = advanceInterruptVad(state, {
      now:
        firstVoiceAt +
        (provisionalFrames - 1 + gap) *
          INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.003,
      rms: 0.003,
    });
  }
  assert.equal(state.action, "discard");
  assert.equal(state.phase, "armed");
  assert.equal(state.firstVoiceAt, null);
});

test("a 600 ms quiet mutter cannot interrupt the current reply", () => {
  const startedAt = 50_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  const firstVoiceAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    INTERRUPT_VAD_LIMITS.intervalMs;
  const mutterFrames = 600 / INTERRUPT_VAD_LIMITS.intervalMs;

  for (let frame = 0; frame < mutterFrames; frame += 1) {
    state = advanceInterruptVad(state, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.075,
      rms: 0.03,
    });
    assert.notEqual(state.action, "confirm");
  }
  assert.equal(state.phase, "provisional");
  assert.equal(state.voiceRunMs, 600);
  assert.ok(state.voiceRunMs < INTERRUPT_VAD_LIMITS.confirmationMs);

  const finalVoiceAt =
    firstVoiceAt + (mutterFrames - 1) * INTERRUPT_VAD_LIMITS.intervalMs;
  const gapFrames =
    INTERRUPT_VAD_LIMITS.candidateGapMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let gap = 1; gap <= gapFrames; gap += 1) {
    state = advanceInterruptVad(state, {
      now: finalVoiceAt + gap * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.003,
      rms: 0.003,
    });
  }
  assert.equal(state.action, "discard");
  assert.equal(state.phase, "armed");
  assert.equal(state.firstVoiceAt, null);
});

test("a sub-720 ms clear mutter stays below the deliberate gate", () => {
  for (const durationMs of [600]) {
    const startedAt = 60_000 + durationMs * 10;
    let state = advancePastInterruptGuard(
      createInterruptVadState(startedAt),
      startedAt,
    );
    const firstVoiceAt =
      startedAt +
      INTERRUPT_VAD_LIMITS.guardMs +
      INTERRUPT_VAD_LIMITS.intervalMs;
    const frames = durationMs / INTERRUPT_VAD_LIMITS.intervalMs;
    for (let frame = 0; frame < frames; frame += 1) {
      state = advanceInterruptVad(state, {
        now:
          firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
        outputActive: true,
        peak: 0.15,
        rms: 0.05,
      });
      assert.notEqual(
        state.action,
        "confirm",
        `${durationMs} ms must remain reversible`,
      );
    }
    assert.equal(state.phase, "provisional");
    assert.equal(state.foregroundVoiceMs, durationMs);

    const finalVoiceAt =
      firstVoiceAt + (frames - 1) * INTERRUPT_VAD_LIMITS.intervalMs;
    const gapFrames =
      INTERRUPT_VAD_LIMITS.candidateGapMs /
      INTERRUPT_VAD_LIMITS.intervalMs;
    for (let gap = 1; gap <= gapFrames; gap += 1) {
      state = advanceInterruptVad(state, {
        now: finalVoiceAt + gap * INTERRUPT_VAD_LIMITS.intervalMs,
        outputActive: true,
        peak: 0.003,
        rms: 0.003,
      });
    }
    assert.equal(state.action, "discard");
    assert.equal(state.phase, "armed");
  }
});

test("sparse muttering cannot cross the hard interrupt density gate", () => {
  let now = 0;
  let state = createInterruptVadState(now);
  const voicedFrames = new Set([1, 2, 4, 5, 7, 8, 10, 12, 14, 16]);
  for (let frame = 1; frame <= 16; frame += 1) {
    now += INTERRUPT_VAD_LIMITS.intervalMs;
    const voiced = voicedFrames.has(frame);
    state = advanceInterruptVad(state, {
      now,
      outputActive: true,
      peak: voiced ? 0.16 : 0.003,
      rms: voiced ? 0.08 : 0.003,
    });
  }
  assert.ok(state.voiceRunMs < INTERRUPT_VAD_LIMITS.confirmationMs);
  assert.equal(state.phase, "provisional");
  assert.notEqual(state.action, "confirm");
});

test("an overdue interrupt candidate discards before it can confirm without its recorder", async () => {
  const startedAt = 0;
  let state = createInterruptVadState(startedAt);
  state = advanceInterruptVad(state, {
    now: 40,
    outputActive: true,
    peak: 0.16,
    rms: 0.08,
  });
  assert.equal(state.action, "start");
  const candidateStartedAt = state.candidateStartedAt;

  // Model a blocked main thread: the independent MediaRecorder watchdog may
  // have detached the candidate before this next VAD task gets to run.
  state = advanceInterruptVad(state, {
    now:
      candidateStartedAt +
      INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs,
    outputActive: true,
    peak: 0.16,
    rms: 0.08,
  });
  assert.equal(state.action, "discard");
  assert.equal(state.phase, "armed");
  assert.equal(state.candidateStartedAt, null);
  assert.equal(state.voiceRunMs, 0);

  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const monitorStart = bridge.indexOf("function startBargeInMonitoring(");
  const monitorEnd = bridge.indexOf(
    "\n}\n\nfunction createStreamingPlayback",
    monitorStart,
  );
  const monitor = bridge.slice(monitorStart, monitorEnd);
  assert.match(
    monitor,
    /vadState\.action === "discard"[\s\S]*restorePlaybackGain\(playback\)[\s\S]*discardCurrentCandidate\(recording, "interrupt-rejected"\)/u,
  );
});

test("interrupt VAD preserves 2.2 seconds for reflective speech", () => {
  const startedAt = 24_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  const firstVoiceAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    INTERRUPT_VAD_LIMITS.intervalMs;
  const reflectiveFrames =
    INTERRUPT_VAD_LIMITS.reflectiveSpeechMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let frame = 0; frame < reflectiveFrames; frame += 1) {
    state = advanceInterruptVad(state, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.15,
      rms: 0.05,
    });
  }
  const lastVoiceAt = state.lastVoiceAt;
  assert.notEqual(lastVoiceAt, null);
  state = advanceInterruptVad(state, {
    now: lastVoiceAt + INTERRUPT_VAD_LIMITS.trailingSilenceMs,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceInterruptVad(state, {
    now: lastVoiceAt + INTERRUPT_VAD_LIMITS.reflectiveSilenceMs,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("interrupt VAD permits a three-minute follow-up monologue", () => {
  const startedAt = 28_000;
  let state = advancePastInterruptGuard(
    createInterruptVadState(startedAt),
    startedAt,
  );
  const firstVoiceAt =
    startedAt +
    INTERRUPT_VAD_LIMITS.guardMs +
    INTERRUPT_VAD_LIMITS.intervalMs;
  const confirmationFrames =
    INTERRUPT_VAD_LIMITS.confirmationMs /
    INTERRUPT_VAD_LIMITS.intervalMs;
  for (let frame = 0; frame < confirmationFrames; frame += 1) {
    state = advanceInterruptVad(state, {
      now: firstVoiceAt + frame * INTERRUPT_VAD_LIMITS.intervalMs,
      outputActive: true,
      peak: 0.15,
      rms: 0.05,
    });
  }
  state = advanceInterruptVad(state, {
    now: firstVoiceAt + 3 * 60_000,
    outputActive: false,
    peak: 0.15,
    rms: 0.05,
  });
  assert.equal(state.action, null);
  const lastVoiceAt = state.lastVoiceAt;
  state = advanceInterruptVad(state, {
    now: lastVoiceAt + INTERRUPT_VAD_LIMITS.reflectiveSilenceMs,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceInterruptVad(state, {
    now: lastVoiceAt + INTERRUPT_VAD_LIMITS.monologueSilenceMs,
    outputActive: false,
    peak: 0.003,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("committed-response barge-in preserves foreground response mode", async () => {
  const [bridge, client] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);
  const confirmAt = bridge.indexOf("function confirmBargeIn(");
  const confirm = bridge.slice(confirmAt, confirmAt + 7_000);
  const manualEndAt = bridge.indexOf("function endTurn()");
  const manualEnd = bridge.slice(manualEndAt, manualEndAt + 650);
  assert.match(
    manualEnd,
    /requestRecordingStop\(recording, "manual"\)/u,
    "the explicit button path must not wait for the acoustic barge gate",
  );
  assert.match(
    confirm,
    /playback\.hasCommittedResponse\?\.\(\) !== true/u,
  );
  assert.match(confirm, /playback\.interruptRecording !== recording/u);
  assert.match(
    confirm,
    /maybeAbortPlaybackTransportOnInterrupt\(\s*playback,\s*activeRequestController,\s*\)/u,
  );
  assert.doesNotMatch(confirm, /activeRequestController\.abort\(\)/u);
  assert.match(confirm, /haltStreamingPlayback\(playback, interruption\)/u);
  assert.ok(
    confirm.indexOf("markSessionSpeech(expectedEpoch)") <
      confirm.indexOf("candidate.confirmed = true"),
  );
  assert.match(
    confirm,
    /!markSessionSpeech\(expectedEpoch\)[\s\S]*expectedEpoch !== sessionEpoch[\s\S]*playback !== activePlayback[\s\S]*recording\.settled[\s\S]*!candidateEventIsCurrent\(recording, candidate\)/u,
  );
  assert.match(confirm, /new CustomEvent\("kotae:voice-interrupted"/u);
  assert.match(confirm, /finalReceived: playback\.finalReceived/u);
  assert.match(confirm, /activeLiveSession = undefined/u);
  assert.match(
    confirm,
    /interruptedLiveSession\.handoffAmbient\(/u,
  );
  assert.match(
    confirm,
    /claimAmbientLiveHandoff\(/u,
  );
  assert.match(bridge, /getSettings\(\)\.echoCancellation === true/u);
  assert.doesNotMatch(bridge, /softDuckPlayback/u);
  assert.doesNotMatch(bridge, /rampPlaybackGain\(playback, 0\.55/u);
  assert.match(bridge, /rampPlaybackGain\(playback, 1, 0\.02\)/u);
  assert.match(bridge, /source\.connect\(gainNode\)/u);
  const bargeMonitorAt = bridge.indexOf("function startBargeInMonitoring(");
  const bargeMonitor = bridge.slice(bargeMonitorAt, bargeMonitorAt + 3_500);
  assert.match(
    bargeMonitor,
    /createInterruptVadState\(guardStartedAt\)/u,
  );
  assert.match(
    bargeMonitor,
    /resetInterruptGuard = \(nextAudibleAt\)[\s\S]*createInterruptVadState\(nextAudibleAt\)/u,
  );
  assert.match(bargeMonitor, /now < vadState\.startedAt/u);
  assert.match(
    bargeMonitor,
    /outputActive:\s*playback\.hasStreamedAudio\(\) && playback\.sources\.size > 0/u,
  );
  assert.match(
    bargeMonitor,
    /vadState\.action === "start"[\s\S]*startCandidateRecorder\([\s\S]*\} else if \(vadState\.action === "provisional"\) \{[\s\S]*hard interruption gate/u,
  );
  const provisionalAt = bargeMonitor.indexOf(
    'vadState.action === "provisional"',
  );
  const discardAt = bargeMonitor.indexOf(
    'vadState.action === "discard"',
    provisionalAt,
  );
  const provisionalBranch = bargeMonitor.slice(provisionalAt, discardAt);
  assert.doesNotMatch(
    provisionalBranch,
    /confirmBargeIn|haltStreamingPlayback|voice-interrupted|abort\(|rampPlaybackGain|restorePlaybackGain/u,
  );
  assert.ok(
    bargeMonitor.indexOf("sessionClock.check()") <
      bargeMonitor.indexOf("setTracksEnabled(true)"),
  );
  assert.match(
    bargeMonitor,
    /stopSession\(sessionStatus\.expiry \?\? "maximum"\);[\s\S]*fail\("session_expired"\)/u,
  );
  const stopAt = bridge.indexOf("function stopSession(");
  const stop = bridge.slice(stopAt, stopAt + 2_500);
  assert.match(stop, /sessionExpiryWatchdog\.disarm\(\)/u);
  assert.match(stop, /releaseMicrophone\(stopCode\)/u);
  assert.match(stop, /rememberStoppedSession\(stoppedEpoch, stopCode\)/u);
  assert.match(
    bridge,
    /const MAX_STOPPED_SESSION_CODES = 8;[\s\S]*const stoppedSessionCodes = new Map\(\)/u,
  );
  assert.match(bridge, /fail\(stoppedSessionCode\(expectedEpoch\)\)/u);
  const finishAt = bridge.indexOf("async function finishTurn(");
  const finishEnd = bridge.indexOf(
    "\n}\n\nfunction safeDocumentName",
    finishAt,
  );
  const finish = bridge.slice(finishAt, finishEnd);
  assert.doesNotMatch(
    finish,
    /startBargeInMonitoring\(playback, expectedEpoch\)/u,
  );
  const stoppedSessionAt = finish.indexOf(
    'stopCode === "session_expired"',
  );
  const interruptedFailureAt = finish.indexOf(
    "if (interruptionAbortedTransport)",
  );
  const timeoutFailureAt = finish.indexOf(
    "if (turnTimedOut)",
    interruptedFailureAt,
  );
  assert.ok(stoppedSessionAt < interruptedFailureAt);
  assert.ok(interruptedFailureAt < timeoutFailureAt);
  assert.match(
    finish,
    /const interruptionAbortedTransport = Boolean\(\s*playback\?\.interruptedBeforeFinal &&\s*shouldAbortPlaybackTransportOnInterrupt\(playback\),\s*\)/u,
  );
  assert.match(
    finish,
    /if \(shouldDiscardInterruptedPlaybackRecording\(playback\)\) \{\s*discardInterruptedPlaybackRecording\(playback\);\s*\}/u,
  );
  const scheduleAt = bridge.indexOf("function scheduleBuffer(");
  const schedule = bridge.slice(scheduleAt, scheduleAt + 4_500);
  assert.match(
    schedule,
    /if \(!streamedAudio && Number\.isFinite\(audibleAt\)\)[\s\S]*playback\.armResponseInterruption\(audibleAt\)/u,
  );
  assert.match(
    schedule,
    /playback\.interruptRecording\.coachActive = true/u,
  );
  const handoffAt = bridge.indexOf("handoffAmbient({");
  const handoff = bridge.slice(handoffAt, handoffAt + 2_000);
  assert.match(handoff, /liveState: state/u);
  assert.match(handoff, /session\.playback\.finalReceived/u);
  assert.match(handoff, /appCheckToken,/u);
  assert.match(handoff, /idToken,/u);
  assert.match(handoff, /sessionState,/u);
  assert.match(handoff, /turnMode: "foreground"/u);
  assert.doesNotMatch(handoff, /preRollCutoffAt/u);
  assert.match(
    confirm,
    /BARGE_PCM_LIMITS\.leadInMs[\s\S]*BARGE_PCM_LIMITS\.frameDurationMs[\s\S]*captureHandoff\.confirm\(\{[\s\S]*candidateContextFrame: candidate\.contextFrame/u,
  );
  const liveSessionAt = bridge.indexOf(
    "async function startVoiceLiveSession(",
  );
  const liveSession = bridge.slice(liveSessionAt, liveSessionAt + 30_000);
  assert.match(
    liveSession,
    /if \(captureHandoff === undefined\) \{[\s\S]*new AudioWorkletNode/u,
  );
  assert.match(
    liveSession,
    /processorOptions:[\s\S]*maximumPreConfirmFrames:[\s\S]*maximumQueuedFrames:/u,
  );
  assert.match(
    liveSession,
    /await sealCapture\(\);[\s\S]*protocol\.markCommitted\(\)/u,
  );
  assert.match(liveSession, /captureHandoff\.adopt\(\{/u);
  assert.match(liveSession, /adoptedCapture = captureHandoff/u);
  assert.match(liveSession, /ownedCapture\.stop\(\)/u);
  const interruptAt = liveSession.indexOf(
    'interrupt(error = new Error("voice_interrupted"))',
  );
  const interrupt = liveSession.slice(interruptAt, interruptAt + 700);
  assert.ok(
    interrupt.indexOf('state = "cancelled"') <
      interrupt.indexOf("closeSocket(4000"),
    "the old live turn must reject queued final frames before close",
  );
  const monitorAt = bridge.indexOf(
    "async function startBargePcmMonitoring(",
  );
  const monitor = bridge.slice(monitorAt, monitorAt + 12_000);
  assert.match(
    monitor,
    /maximumPreConfirmFrames: BARGE_PCM_LIMITS\.maximumFrames/u,
  );
  assert.match(
    monitor,
    /initialCredit: 0[\s\S]*type: "confirm"/u,
  );
  assert.match(
    monitor,
    /safeLiveCaptureFrame\(event\.data,[\s\S]*sequence: expectedSequence/u,
  );
  assert.match(
    monitor,
    /frames: 1,[\s\S]*type: "credit"/u,
  );
  assert.match(monitor, /frameSink = onFrame/u);
  assert.match(
    monitor,
    /async seal\(\)[\s\S]*type: "seal"[\s\S]*await sealed/u,
  );

  const interruptionAt = client.indexOf(
    "fn resume_foreground_interruption(",
  );
  const continuation = client.slice(interruptionAt, interruptionAt + 2_500);
  assert.match(continuation, /cloud::wait_for_turn_end\(\)\.await/u);
  assert.match(
    continuation,
    /submit_turn\(\s*operation,\s*VoiceTurnMode::Foreground,/u,
  );
  assert.doesNotMatch(
    continuation,
    /session_state\.set\(result\.session_state/u,
  );
});

test("confirmed follow-up speech expires only the previous turn proof", async () => {
  const [bridge, client] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);

  const monitorStart = bridge.indexOf("function startBargeInMonitoring(");
  const monitorEnd = bridge.indexOf(
    "\n}\n\nfunction createStreamingPlayback(",
    monitorStart,
  );
  assert.ok(monitorStart >= 0);
  assert.ok(monitorEnd > monitorStart);
  const monitor = bridge.slice(monitorStart, monitorEnd);
  const priorAt = monitor.indexOf(
    "const hadConfirmedSpeech = recording.vadHasSpeech;",
  );
  const transitionAt = monitor.indexOf(
    'recording.vadHasSpeech = vadState.phase === "confirmed";',
  );
  const eventAt = monitor.indexOf(
    'new CustomEvent("kotae:voice-input-confirmed"',
  );
  const endpointAt = monitor.indexOf("maybeCommitHybridEndpoint(recording, now)");
  assert.ok(priorAt >= 0);
  assert.ok(transitionAt > priorAt);
  assert.ok(eventAt > transitionAt);
  assert.ok(endpointAt > eventAt);
  assert.match(
    monitor,
    /if \(!hadConfirmedSpeech && recording\.vadHasSpeech\) \{\s*globalThis\.dispatchEvent\(\s*new CustomEvent\("kotae:voice-input-confirmed", \{\s*detail: Object\.freeze\(\{ version: 1 \}\),\s*\}\),\s*\);\s*\}/u,
  );
  const eventBlock = monitor.slice(eventAt, eventAt + 220);
  assert.doesNotMatch(eventBlock, /answerProof|phase|action|audio|transcript/u);

  const captureStart = bridge.indexOf("function armVad(recording)");
  const captureEnd = bridge.indexOf(
    "\n}\n\nfunction createRecordingState(",
    captureStart,
  );
  assert.ok(captureStart >= 0);
  assert.ok(captureEnd > captureStart);
  const capture = bridge.slice(captureStart, captureEnd);
  assert.match(
    capture,
    /const hadConfirmedSpeech = recording\.vadHasSpeech;\s*recording\.vadHasSpeech = vadState\.hasSpeech;\s*if \(!hadConfirmedSpeech && recording\.vadHasSpeech\) \{[\s\S]*new CustomEvent\("kotae:voice-input-confirmed", \{\s*detail: Object\.freeze\(\{ version: 1 \}\)/u,
  );

  const listenerStart = client.indexOf(
    "pub fn install_voice_input_confirmed_listener(",
  );
  const listenerEnd = client.indexOf(
    "\n    pub fn install_first_audio_listener(",
    listenerStart,
  );
  assert.ok(listenerStart >= 0);
  assert.ok(listenerEnd > listenerStart);
  const listener = client.slice(listenerStart, listenerEnd);
  assert.match(listener, /js_sys::Object::keys\(detail_object\)/u);
  assert.match(
    listener,
    /confirmed_voice_input_state\(\*coach_state\.peek\(\), version, &key_names\)/u,
  );
  assert.match(listener, /coach_state\.set\(next_state\)/u);
  assert.doesNotMatch(listener, /CoachState::from_|phase:|action:/u);
  assert.match(
    listener,
    /add_event_listener_with_callback\(\s*"kotae:voice-input-confirmed"/u,
  );
  assert.match(
    client,
    /use_hook\(\|\| cloud::install_voice_input_confirmed_listener\(coach_state\)\)/u,
  );
  assert.ok(
    [...client.matchAll(/pub fn install_voice_input_confirmed_listener\(/gu)]
      .length >= 2,
    "the native test build keeps a non-wasm listener stub",
  );

  assert.doesNotMatch(client, /本人回答証明/u);
  assert.doesNotMatch(client, /実際に聞かれた問いへ/u);
  assert.match(client, /今回の入力 \/ A先頭確認/u);
  assert.match(client, /報告された問いへの入力が、Aから始まりました/u);
  for (const boundary of [
    "話者",
    "ライブネス",
    "外部で実際にその問いを聞かれた事実",
    "正解",
    "能力",
    "上達",
  ]) {
    assert.match(client, new RegExp(boundary, "u"));
  }
});

test("network keeps one finite deadline while validated playback drains separately", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  assert.match(
    bridge,
    /const VOICE_TURN_CLIENT_TIMEOUT_MS = 60_000;/u,
  );
  const finishAt = bridge.indexOf("async function finishTurn(");
  const finishEnd = bridge.indexOf(
    "\n}\n\nfunction safeDocumentName",
    finishAt,
  );
  const finish = bridge.slice(finishAt, finishEnd);
  assert.match(
    finish,
    /turnDeadlineAt[\s\S]*function awaitVoiceTurnResult\(/u,
  );
  assert.match(
    finish,
    /turnTimedOut = true;[\s\S]*reject\(new Error\("voice_turn_timeout"\)\)/u,
  );
  assert.match(
    finish,
    /awaitVoiceTurnResult\(\s*liveSession\.commit\(/u,
  );
  assert.match(
    finish,
    /const responsePromise = fetch\(VOICE_ENDPOINT[\s\S]*playback\.armResponseInterruption\(performance\.now\(\)\);[\s\S]*await awaitVoiceTurnResult\(\s*responsePromise/u,
  );
  assert.match(
    finish,
    /awaitVoiceTurnResult\(\s*consumeVoiceStream\(/u,
  );
  assert.equal(
    (
      finish.match(
        /await awaitValidatedPlaybackCompletion\([\s\S]*?playback,[\s\S]*?expectedEpoch,?[\s\S]*?\)/gu,
      ) ?? []
    ).length,
    2,
  );
  assert.doesNotMatch(
    finish,
    /awaitVoiceTurnResult\(\s*awaitValidatedPlaybackCompletion/u,
  );
  assert.match(
    finish,
    /turnTimedOut \|\|[\s\S]*!liveSession\.canFallback\(\)/u,
  );
  assert.match(
    finish,
    /if \(turnTimedOut\) \{[\s\S]*fail\("voice_turn_timeout"\)/u,
  );

  const liveStart = bridge.indexOf(
    "async function startVoiceLiveSession(",
  );
  const liveEnd = bridge.indexOf(
    "\n}\n\nfunction isNdjsonContentType",
    liveStart,
  );
  const live = bridge.slice(liveStart, liveEnd);
  const commitAt = live.indexOf("async commit(playback, lastVoiceAt)");
  const interruptAt = live.indexOf(
    'interrupt(error = new Error("voice_interrupted"))',
    commitAt,
  );
  const commit = live.slice(commitAt, interruptAt);
  const committedAt = commit.indexOf('state = "committed"');
  const clockAt = commit.indexOf("commitAt = performance.now()");
  const monitorAt = commit.indexOf(
    "playback.armResponseInterruption(commitAt)",
  );
  const resultAt = commit.indexOf("const result = await resultPromise");
  assert.ok(committedAt >= 0);
  assert.ok(clockAt > committedAt);
  assert.ok(monitorAt > clockAt);
  assert.ok(resultAt > monitorAt);
  assert.match(commit, /const result = await resultPromise/u);
  assert.match(commit, /finalizeMeaningfulVoiceStream\(/u);
  assert.doesNotMatch(commit, /await playback\.completion/u);

  const consumeStart = bridge.indexOf(
    "async function consumeVoiceStream(",
  );
  const consumeEnd = bridge.indexOf(
    "\n}\n\nasync function finishTurn",
    consumeStart,
  );
  const consume = bridge.slice(consumeStart, consumeEnd);
  assert.match(consume, /const completed = parser\.finish\(\)/u);
  assert.match(consume, /finalizeMeaningfulVoiceStream\(/u);
  assert.doesNotMatch(consume, /await playback\.completion/u);
});

test("coach metadata accepts only authoritative phase and action pairs", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const validatorSource = executableBridgeFunction(
    bridge,
    "function hasValidCoachMetadata(",
    "function clearPendingDocument(",
  );
  const validate = Function(
    `"use strict"; ${validatorSource}; return hasValidCoachMetadata;`,
  )();
  const validateAnswerProof = Function(
    `"use strict"; ${validatorSource}; return hasValidAnswerProofMetadata;`,
  )();

  for (const [target, phase, action] of [
    ["assistant", "none", "none"],
    ["respondent", "awaiting_answer", "elicit"],
    ["respondent", "awaiting_restatement", "restate"],
    ["respondent", "expanding", "expand"],
    ["respondent", "complete", "complete"],
    ["respondent", "blocked", "retry"],
    ["respondent", "blocked", "release"],
  ]) {
    assert.equal(validate(target, phase, action), true);
  }
  for (const [target, phase, action] of [
    ["assistant", "complete", "complete"],
    ["respondent", "none", "none"],
    ["respondent", "awaiting_answer", "complete"],
    ["respondent", "blocked", "expand"],
    ["unknown", "none", "none"],
    ["respondent", "scored", "answer-for-user"],
  ]) {
    assert.equal(validate(target, phase, action), false);
  }

  assert.equal(
    validateAnswerProof(
      "question_bound_input_answer_first",
      "respondent",
      "restructure",
      "complete",
      "complete",
    ),
    true,
  );
  assert.equal(
    validateAnswerProof(
      "question_bound_input_answer_first",
      "respondent",
      "restructure",
      "expanding",
      "expand",
    ),
    true,
  );
  for (const candidate of [
    ["verified", "respondent", "restructure", "complete", "complete"],
    [
      "question_bound_input_answer_first",
      "assistant",
      "none",
      "none",
      "none",
    ],
    [
      "question_bound_input_answer_first",
      "respondent",
      "awaiting_answer",
      "awaiting_answer",
      "elicit",
    ],
  ]) {
    assert.equal(validateAnswerProof(...candidate), false);
  }
  assert.equal(
    validateAnswerProof(
      "none",
      "assistant",
      "none",
      "none",
      "none",
    ),
    true,
  );

  const responseStart = bridge.indexOf("function safeVoiceResponse(");
  const responseEnd = bridge.indexOf(
    "\n}\n\nfunction mapVoiceResponseError",
    responseStart,
  );
  const response = bridge.slice(responseStart, responseEnd);
  assert.match(
    response,
    /hasValidCoachMetadata\(\s*payload\.assistanceTarget,\s*payload\.coachPhase,\s*payload\.coachAction,\s*\)/u,
  );
  assert.match(response, /coachPhase: payload\.coachPhase/u);
  assert.match(response, /coachAction: payload\.coachAction/u);
  assert.match(
    response,
    /hasValidAnswerProofMetadata\(\s*answerProof,\s*payload\.assistanceTarget,\s*payload\.respondentStage,\s*payload\.coachPhase,\s*payload\.coachAction,\s*\)/u,
  );
  assert.match(
    response,
    /expectedStrictCloudMinimization && answerProof !== "none"/u,
  );
  assert.match(response, /answerProof,/u);
  assert.match(
    response,
    /expectedStrictCloudMinimization[\s\S]*payload\.privacyStatus !== "blocked"[\s\S]*payload\.privacyStatus !== "clear"/u,
  );
  assert.match(
    response,
    /!expectedStrictCloudMinimization && payload\.privacyStatus !== ""/u,
  );
  assert.match(
    bridge,
    /safeVoiceResponse\(result, strictCloudMinimization\)/u,
  );
  assert.match(
    bridge,
    /safeVoiceResponse\(result, expectedStrictCloudMinimization\)/u,
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
  const fetchAt = bridge.indexOf("fetch(VOICE_ENDPOINT");
  const fetchBlock = bridge.slice(fetchAt, fetchAt + 700);
  assert.match(fetchBlock, /credentials: "omit"/u);
  assert.match(fetchBlock, /mode: "cors"/u);
  assert.match(fetchBlock, /Authorization: `Bearer \$\{idToken\}`/u);
  assert.match(fetchBlock, /"X-Firebase-AppCheck": appCheckToken/u);
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

test("voice session pause reasons are finite and contain no user content", () => {
  const expected = new Map([
    ["idle", "session_expired"],
    ["maximum", "session_expired"],
    ["hidden", "request_cancelled"],
    ["pagehide", "request_cancelled"],
    ["microphone_lost", "microphone_unavailable"],
  ]);

  for (const [reason, stopCode] of expected) {
    const classified = classifyVoiceSessionStopReason(reason);
    assert.deepEqual(classified, { pauseReason: reason, stopCode });
    assert.equal(Object.isFrozen(classified), true);
  }

  assert.deepEqual(
    classifyVoiceSessionStopReason("私の会話内容を含む任意文字列"),
    { pauseReason: null, stopCode: "request_cancelled" },
  );
  assert.deepEqual(classifyVoiceSessionStopReason({ transcript: "secret" }), {
    pauseReason: null,
    stopCode: "request_cancelled",
  });
});

test("the pending document deadline remains bounded", () => {
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

  now = 1_000_000;
  const absoluteClock = createSessionClock({
    now: () => now,
    idleLimitMs: VOICE_SESSION_LIMITS.maximumSessionMs * 2,
  });
  assert.deepEqual(absoluteClock.begin(), { expiry: null, ok: true });
  now += VOICE_SESSION_LIMITS.maximumSessionMs - 1;
  assert.deepEqual(absoluteClock.begin(), { expiry: null, ok: true });
  now += 1;
  assert.deepEqual(absoluteClock.begin(), {
    expiry: "maximum",
    ok: false,
  });
});

test("an active response defers only idle expiry until playback completes", () => {
  let now = 20_000;
  const clock = createSessionClock({ now: () => now });
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  assert.deepEqual(clock.beginResponse(), { expiry: null, ok: true });

  now += VOICE_SESSION_LIMITS.idleSessionLimitMs;
  assert.deepEqual(clock.check(), { expiry: null, ok: true });
  assert.equal(clock.snapshot().responseActive, true);
  assert.ok(clock.millisecondsUntilExpiry() > 0);

  assert.deepEqual(clock.completeResponse(), { expiry: null, ok: true });
  assert.equal(clock.snapshot().lastSpeechAt, now);
  assert.equal(clock.snapshot().responseActive, false);

  now += VOICE_SESSION_LIMITS.idleSessionLimitMs - 1;
  assert.deepEqual(clock.check(), { expiry: null, ok: true });
  now += 1;
  assert.deepEqual(clock.check(), { expiry: "idle", ok: false });

  now = 2_000_000;
  const absoluteClock = createSessionClock({ now: () => now });
  assert.deepEqual(absoluteClock.begin(), { expiry: null, ok: true });
  assert.deepEqual(absoluteClock.beginResponse(), { expiry: null, ok: true });
  now += VOICE_SESSION_LIMITS.maximumSessionMs;
  assert.deepEqual(absoluteClock.check(), { expiry: "maximum", ok: false });
  assert.deepEqual(absoluteClock.completeResponse(), {
    expiry: "maximum",
    ok: false,
  });
});

test("a failed response clears the hold without extending the idle lease", () => {
  let now = 30_000;
  const clock = createSessionClock({ now: () => now });
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  const initialSpeechAt = clock.snapshot().lastSpeechAt;
  assert.deepEqual(clock.beginResponse(), { expiry: null, ok: true });

  now += 60_000;
  assert.deepEqual(clock.cancelResponse(), { expiry: null, ok: true });
  assert.equal(clock.snapshot().responseActive, false);
  assert.equal(clock.snapshot().lastSpeechAt, initialSpeechAt);

  now = initialSpeechAt + VOICE_SESSION_LIMITS.idleSessionLimitMs;
  assert.deepEqual(clock.check(), { expiry: "idle", ok: false });
});

test("finishTurn holds the idle clock through validated playback only", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("async function finishTurn(");
  const end = bridge.indexOf("\n}\n\nfunction safeDocumentName", start);
  assert.notEqual(start, -1);
  assert.notEqual(end, -1);
  const finish = bridge.slice(start, end);

  const beginAt = finish.indexOf("beginSessionResponse(expectedEpoch)");
  const liveDrainAt = finish.indexOf(
    "await awaitValidatedPlaybackCompletion(\n          playback,\n          expectedEpoch,\n        )",
  );
  const liveCompleteAt = finish.indexOf(
    "completeSessionResponse(expectedEpoch)",
    liveDrainAt,
  );
  const httpDrainAt = finish.lastIndexOf(
    "await awaitValidatedPlaybackCompletion(playback, expectedEpoch)",
  );
  const httpCompleteAt = finish.indexOf(
    "completeSessionResponse(expectedEpoch)",
    httpDrainAt,
  );
  const failureCleanupAt = finish.indexOf(
    "if (responseClockActive)",
    httpCompleteAt,
  );
  const failureCancelAt = finish.indexOf(
    "cancelSessionResponse(expectedEpoch)",
    failureCleanupAt,
  );

  assert.ok(beginAt >= 0);
  assert.ok(liveDrainAt > beginAt);
  assert.ok(liveCompleteAt > liveDrainAt);
  assert.ok(httpDrainAt > liveCompleteAt);
  assert.ok(httpCompleteAt > httpDrainAt);
  assert.ok(failureCleanupAt > httpCompleteAt);
  assert.ok(failureCancelAt > failureCleanupAt);
  assert.equal(
    finish.indexOf("completeSessionResponse(expectedEpoch)", failureCleanupAt),
    -1,
  );
});

test("expired speech cannot revive a session clock", () => {
  let now = 5_000;
  const clock = createSessionClock({ now: () => now });
  assert.deepEqual(clock.begin(), { expiry: null, ok: true });
  const initial = clock.snapshot();

  now += VOICE_SESSION_LIMITS.idleSessionLimitMs;
  assert.deepEqual(clock.markSpeech(), { expiry: "idle", ok: false });
  assert.deepEqual(clock.check(), { expiry: "idle", ok: false });
  assert.equal(clock.snapshot().lastSpeechAt, initial.lastSpeechAt);
  assert.equal(clock.millisecondsUntilExpiry(), 0);
});

test("a stale session watchdog cannot stop a replacement session", () => {
  let now = 10_000;
  let nextTimer = 0;
  const callbacks = new Map();
  const expired = [];
  const clock = createSessionClock({ now: () => now });
  const watchdog = createSessionExpiryWatchdog({
    check: () => clock.check(),
    clearTimer: () => {},
    expire: (reason) => expired.push(reason),
    millisecondsUntilExpiry: () => clock.millisecondsUntilExpiry(),
    setTimer: (callback) => {
      nextTimer += 1;
      callbacks.set(nextTimer, callback);
      return nextTimer;
    },
  });

  clock.begin();
  assert.equal(watchdog.arm(), true);
  const staleCallback = callbacks.get(1);
  watchdog.disarm();
  clock.reset();
  now = 20_000;
  clock.begin();
  assert.equal(watchdog.arm(), true);

  staleCallback();
  assert.deepEqual(expired, []);

  now += VOICE_SESSION_LIMITS.idleSessionLimitMs;
  callbacks.get(2)();
  assert.deepEqual(expired, ["idle"]);
});

test("a watchdog reports synchronous expiry instead of rearming a stopped session", () => {
  const expired = [];
  let timerCreated = false;
  const watchdog = createSessionExpiryWatchdog({
    check: () => ({ expiry: "maximum", ok: false }),
    clearTimer: () => {},
    expire: (reason) => expired.push(reason),
    millisecondsUntilExpiry: () => 0,
    setTimer: () => {
      timerCreated = true;
      return 1;
    },
  });

  assert.equal(watchdog.arm(), false);
  assert.deepEqual(expired, ["maximum"]);
  assert.equal(timerCreated, false);
});

test("VAD confirms 120 ms of voice then ends after 1.2 seconds silence", () => {
  assert.equal(VOICE_SESSION_LIMITS.endOfTurnSilenceMs, 1_200);
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

test("active coach local VAD accelerates only a short clear answer", () => {
  assert.equal(VOICE_SESSION_LIMITS.coachEndOfTurnSilenceMs, 640);
  const coachOptions = { coachActive: true };
  const speakClearly = (startedAt, durationMs) => {
    let state = createVadState(startedAt);
    for (
      let elapsed = VOICE_SESSION_LIMITS.vadIntervalMs;
      elapsed <= durationMs;
      elapsed += VOICE_SESSION_LIMITS.vadIntervalMs
    ) {
      state = advanceVad(
        state,
        { now: startedAt + elapsed, peak: 0.08, rms: 0.03 },
        coachOptions,
      );
    }
    return state;
  };
  const advanceSilence = (state, silenceMs) =>
    advanceVad(
      state,
      {
        now: state.lastVoiceAt + silenceMs,
        peak: 0,
        rms: 0.003,
      },
      coachOptions,
    );

  let short = speakClearly(
    2_000,
    VOICE_SESSION_LIMITS.minimumVoiceMs,
  );
  const shortLastVoiceAt = short.lastVoiceAt;
  short = advanceSilence(
    short,
    VOICE_SESSION_LIMITS.coachEndOfTurnSilenceMs - 1,
  );
  assert.equal(short.action, null);
  short = advanceVad(
    short,
    {
      now:
        shortLastVoiceAt +
        VOICE_SESSION_LIMITS.coachEndOfTurnSilenceMs,
      peak: 0,
      rms: 0.003,
    },
    coachOptions,
  );
  assert.equal(short.action, "end-of-turn");

  let reflective = speakClearly(
    5_000,
    VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs,
  );
  const reflectiveLastVoiceAt = reflective.lastVoiceAt;
  reflective = advanceSilence(
    reflective,
    VOICE_SESSION_LIMITS.coachEndOfTurnSilenceMs,
  );
  assert.equal(
    reflective.action,
    null,
    "coach mode must not shorten a reflective pause",
  );
  reflective = advanceVad(
    reflective,
    {
      now:
        reflectiveLastVoiceAt +
        VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
      peak: 0,
      rms: 0.003,
    },
    coachOptions,
  );
  assert.equal(reflective.action, "end-of-turn");

  let now = 10_000;
  let soft = createVadState(now);
  const softFrames =
    VOICE_SESSION_LIMITS.softVoiceMinimumMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < softFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    soft = advanceVad(
      soft,
      { now, peak: rms * 2, rms },
      coachOptions,
    );
  }
  assert.equal(soft.softVoiceConfirmed, true);
  const softLastVoiceAt = soft.lastVoiceAt;
  soft = advanceSilence(
    soft,
    VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
  );
  assert.equal(
    soft.action,
    null,
    "coach mode must keep the full quiet-speech pause",
  );
  soft = advanceVad(
    soft,
    {
      now:
        softLastVoiceAt +
        VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
      peak: 0,
      rms: 0.003,
    },
    coachOptions,
  );
  assert.equal(soft.action, "end-of-turn");

  let monologue = speakClearly(
    20_000,
    VOICE_SESSION_LIMITS.monologueSpeechSpanMs,
  );
  const monologueLastVoiceAt = monologue.lastVoiceAt;
  monologue = advanceSilence(
    monologue,
    VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
  );
  assert.equal(
    monologue.action,
    null,
    "coach mode must keep the full monologue pause",
  );
  monologue = advanceVad(
    monologue,
    {
      now:
        monologueLastVoiceAt +
        VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
      peak: 0,
      rms: 0.003,
    },
    coachOptions,
  );
  assert.equal(monologue.action, "end-of-turn");
});

test("clear Native speech ends at 520 ms while protected speech keeps wider pauses", () => {
  assert.equal(VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs, 520);
  const nativeVadOptions = {
    endOfTurnSilenceMs:
      VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs,
    reflectiveEndOfTurnSilenceMs:
      VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs,
  };
  const speakClearly = (startedAt, durationMs) => {
    let state = createVadState(startedAt);
    for (
      let elapsed = VOICE_SESSION_LIMITS.vadIntervalMs;
      elapsed <= durationMs;
      elapsed += VOICE_SESSION_LIMITS.vadIntervalMs
    ) {
      state = advanceVad(
        state,
        { now: startedAt + elapsed, peak: 0.08, rms: 0.03 },
        nativeVadOptions,
      );
    }
    return state;
  };
  const expectEndpointAt = (state, silenceMs, message) => {
    const lastVoiceAt = state.lastVoiceAt;
    state = advanceVad(
      state,
      { now: lastVoiceAt + silenceMs - 1, peak: 0, rms: 0.003 },
      nativeVadOptions,
    );
    assert.equal(state.action, null, message);
    state = advanceVad(
      state,
      { now: lastVoiceAt + silenceMs, peak: 0, rms: 0.003 },
      nativeVadOptions,
    );
    assert.equal(state.action, "end-of-turn");
  };

  expectEndpointAt(
    speakClearly(2_000, VOICE_SESSION_LIMITS.minimumVoiceMs),
    VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs,
    "a short clear Native answer must not commit before 520 ms",
  );
  expectEndpointAt(
    speakClearly(5_000, VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs),
    VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs,
    "clear Native speech keeps 520 ms at reflective length",
  );

  let now = 10_000;
  let soft = createVadState(now);
  const softFrames =
    VOICE_SESSION_LIMITS.softVoiceMinimumMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < softFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    soft = advanceVad(
      soft,
      { now, peak: rms * 2, rms },
      nativeVadOptions,
    );
  }
  assert.equal(soft.softVoiceConfirmed, true);
  expectEndpointAt(
    soft,
    VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
    "confirmed quiet Native speech must keep the full pause",
  );

  expectEndpointAt(
    speakClearly(20_000, VOICE_SESSION_LIMITS.monologueSpeechSpanMs),
    VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
    "a Native monologue must keep the full continuation window",
  );
});

test("VAD opens the relative-SNR path only at its noise-relative boundary", () => {
  const startedAt = 2_500;
  let now = startedAt;
  let state = createVadState(startedAt);
  for (let frame = 0; frame < 15; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
  }

  const belowRms =
    state.noiseFloor * (VOICE_SESSION_LIMITS.softVoiceSnrRatio - 0.01);
  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  state = advanceVad(
    state,
    {
      now,
      peak: belowRms * 2,
      rms: belowRms,
    },
    { softVoiceBootstrapMs: 0 },
  );
  assert.equal(state.sampleVoiced, false);
  assert.equal(state.firstVoiceAt, null);

  const boundaryRms =
    state.noiseFloor * VOICE_SESSION_LIMITS.softVoiceSnrRatio;
  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  state = advanceVad(
    state,
    {
      now,
      peak:
        boundaryRms * VOICE_SESSION_LIMITS.softVoicePeakToRmsRatio,
      rms: boundaryRms,
    },
    { softVoiceBootstrapMs: 0 },
  );
  assert.equal(state.sampleVoiced, true);
  assert.equal(state.softVoiceCandidate, true);
  assert.equal(state.hasSpeech, false);
});

test("cold-start bootstrap confirms changing quiet speech without learning it as room noise", () => {
  let now = 0;
  let state = createVadState(now);
  let candidate = createCandidateCaptureState();
  const confirmationFrames =
    VOICE_SESSION_LIMITS.softVoiceMinimumMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < confirmationFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    state = advanceVad(state, { now, peak: rms * 2, rms });
    candidate = advanceCandidateCapture(candidate, state, now);
  }

  assert.equal(state.hasSpeech, true);
  assert.equal(state.softVoiceConfirmed, true);
  assert.equal(state.noiseFloor, 0.006);
  assert.equal(candidate.action, "confirm");
  assert.equal(candidate.phase, "confirmed");
});

test("cold-start bootstrap rejects stationary room sound and does not immediately rearm it", () => {
  let now = 0;
  let state = createVadState(now);
  let candidate = createCandidateCaptureState();
  const probeFrames =
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < probeFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.013, rms: 0.0065 });
    candidate = advanceCandidateCapture(candidate, state, now);
  }

  assert.equal(state.hasSpeech, false);
  assert.equal(state.sampleVoiced, false);
  assert.equal(state.softVoiceCandidate, false);
  assert.equal(state.noiseFloor, 0.0065);
  assert.equal(candidate.action, "discard");

  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  state = advanceVad(state, { now, peak: 0.013, rms: 0.0065 });
  candidate = advanceCandidateCapture(candidate, state, now);
  assert.equal(state.sampleVoiced, false);
  assert.equal(candidate.action, null);
  assert.equal(candidate.phase, "armed");
});

test("sustained dynamic quiet speech gets a longer candidate and thinking pause", () => {
  assert.equal(VOICE_SESSION_LIMITS.softVoiceMinimumMs, 600);
  assert.equal(VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs, 3_000);
  assert.ok(
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs >
      VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
  );

  const startedAt = 4_000;
  let now = startedAt;
  let vadState = createVadState(startedAt);
  let candidateState = createCandidateCaptureState();
  for (let frame = 0; frame < 15; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, {
      now,
      peak: 0.004,
      rms: 0.003,
    });
  }

  const confirmationFrames =
    VOICE_SESSION_LIMITS.softVoiceMinimumMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 1; frame <= confirmationFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.009;
    assert.ok(rms < 0.014, "test signal must stay below the clear path");
    vadState = advanceVad(vadState, {
      now,
      peak: rms * 2,
      rms,
    });
    candidateState = advanceCandidateCapture(
      candidateState,
      vadState,
      now,
    );
    if (
      now - candidateState.candidateStartedAt ===
      VOICE_SESSION_LIMITS.candidateCaptureLimitMs
    ) {
      assert.equal(candidateState.phase, "candidate");
      assert.equal(candidateState.action, null);
    }
  }

  assert.equal(vadState.hasSpeech, true);
  assert.equal(vadState.softVoiceConfirmed, true);
  assert.equal(candidateState.action, "confirm");
  assert.equal(candidateState.phase, "confirmed");
  const lastVoiceAt = vadState.lastVoiceAt;

  vadState = advanceVad(vadState, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs - 1,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(vadState.action, null);
  vadState = advanceVad(vadState, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(vadState.action, "end-of-turn");
});

test("a clear utterance can fade into verified quiet speech without a 1.2 second cutoff", () => {
  let now = 0;
  let state = createVadState(now);
  for (let frame = 0; frame < 15; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
  }
  for (let frame = 0; frame < 3; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.08, rms: 0.03 });
  }
  const clearLastVoiceAt = state.lastVoiceAt;
  assert.equal(state.hasSpeech, true);
  assert.equal(state.softVoiceConfirmed, false);

  const quietFrames =
    (VOICE_SESSION_LIMITS.softVoiceMinimumMs +
      VOICE_SESSION_LIMITS.endOfTurnSilenceMs) /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < quietFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    state = advanceVad(state, { now, peak: rms * 2, rms });
    assert.equal(state.action, null);
  }

  assert.ok(now - clearLastVoiceAt > VOICE_SESSION_LIMITS.endOfTurnSilenceMs);
  assert.equal(state.softVoiceConfirmed, true);
  assert.equal(state.lastVoiceAt, now);
});

test("a brief dynamic quiet word refreshes a clear turn without forcing long-pause mode", () => {
  let now = 0;
  let state = createVadState(now);
  for (let frame = 0; frame < 15; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
  }
  for (let frame = 0; frame < 3; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.08, rms: 0.03 });
  }
  const clearLastVoiceAt = state.lastVoiceAt;

  for (let frame = 0; frame < 5; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    state = advanceVad(state, { now, peak: rms * 2, rms });
  }
  assert.ok(state.lastVoiceAt > clearLastVoiceAt);
  assert.equal(state.lastVoiceAt, now);
  assert.equal(state.softVoiceConfirmed, false);

  state = advanceVad(state, {
    now: now + VOICE_SESSION_LIMITS.endOfTurnSilenceMs - 1,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now: now + VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("stationary soft tail and a short echo burst cannot extend a clear utterance", () => {
  let now = 0;
  let state = createVadState(now);
  for (let frame = 0; frame < 15; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
  }
  for (let frame = 0; frame < 3; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.08, rms: 0.03 });
  }
  const clearLastVoiceAt = state.lastVoiceAt;

  for (let frame = 0; frame < 4; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    state = advanceVad(state, { now, peak: rms * 2, rms });
  }
  assert.equal(state.softVoiceConfirmed, false);
  assert.equal(state.lastVoiceAt, clearLastVoiceAt);

  while (
    now < clearLastVoiceAt + VOICE_SESSION_LIMITS.endOfTurnSilenceMs
  ) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.017, rms: 0.0085 });
  }
  assert.equal(state.softVoiceConfirmed, false);
  assert.equal(state.lastVoiceAt, clearLastVoiceAt);
  assert.equal(state.action, "end-of-turn");
});

test("relative-SNR VAD rejects short spikes and stationary room noise", () => {
  const primeRoom = (startedAt) => {
    let now = startedAt;
    let state = createVadState(startedAt);
    for (let frame = 0; frame < 15; frame += 1) {
      now += VOICE_SESSION_LIMITS.vadIntervalMs;
      state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
    }
    return { now, state };
  };

  let { now, state } = primeRoom(7_000);
  let candidate = createCandidateCaptureState();
  for (let frame = 0; frame < 4; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.009 : 0.0065;
    state = advanceVad(state, { now, peak: rms * 2, rms });
    candidate = advanceCandidateCapture(candidate, state, now);
  }
  for (let frame = 0; frame < 4; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.004, rms: 0.003 });
    candidate = advanceCandidateCapture(candidate, state, now);
  }
  assert.equal(state.hasSpeech, false);
  assert.equal(state.voiceRunMs, 0);
  assert.equal(candidate.action, "discard");

  ({ now, state } = primeRoom(10_000));
  candidate = createCandidateCaptureState();
  const probeFrames =
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < probeFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    state = advanceVad(state, { now, peak: 0.015, rms: 0.0075 });
    candidate = advanceCandidateCapture(candidate, state, now);
    assert.equal(state.hasSpeech, false);
  }
  assert.equal(state.sampleVoiced, false);
  assert.equal(state.voiceRunMs, 0);
  assert.equal(state.firstVoiceAt, null);
  assert.equal(candidate.action, "discard");
});

test("VAD gives a reflective utterance 2.2 seconds to continue", () => {
  assert.equal(
    VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
    2_200,
  );
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

test("VAD caps a spoken capture at three minutes thirty seconds", () => {
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

test("VAD keeps a three-minute clear monologue open through a natural pause", () => {
  assert.ok(
    VOICE_SESSION_LIMITS.spokenCaptureLimitMs >=
      VOICE_SESSION_LIMITS.silentCaptureLimitMs + 3 * 60_000,
    "a slow start must still leave three full minutes of capture",
  );
  assert.ok(
    VOICE_SESSION_LIMITS.idleSessionLimitMs >
      VOICE_SESSION_LIMITS.spokenCaptureLimitMs +
        VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
    "the idle watchdog must not terminate an active bounded monologue",
  );
  const startedAt = 60_000;
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
  state = advanceVad(state, {
    now: startedAt + 3 * 60_000,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.action, null);
  const lastVoiceAt = state.lastVoiceAt;
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now:
      lastVoiceAt +
      VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs -
      1,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("cold-start changing quiet speech remains open for a three-minute monologue", () => {
  let now = 0;
  let state = createVadState(now);
  const speechFrames =
    (3 * 60_000) / VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 1; frame <= speechFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.009;
    state = advanceVad(state, { now, peak: rms * 2, rms });
    assert.equal(state.action, null, `quiet speech ended at frame ${frame}`);
  }
  assert.equal(state.hasSpeech, true);
  assert.equal(state.softVoiceConfirmed, true);
  assert.ok(state.lastVoiceAt >= now - VOICE_SESSION_LIMITS.vadIntervalMs);
  const lastVoiceAt = state.lastVoiceAt;
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
});

test("cold-start changing quiet speech remains open for a three-minute monologue", () => {
  let now = 0;
  let state = createVadState(now);
  const speechFrames =
    (3 * 60_000) / VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 1; frame <= speechFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.009;
    state = advanceVad(state, { now, peak: rms * 2, rms });
    assert.equal(state.action, null, `quiet speech ended at frame ${frame}`);
  }
  assert.equal(state.hasSpeech, true);
  assert.equal(state.softVoiceConfirmed, true);
  assert.ok(state.lastVoiceAt >= now - VOICE_SESSION_LIMITS.vadIntervalMs);
  const lastVoiceAt = state.lastVoiceAt;
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, null);
  state = advanceVad(state, {
    now: lastVoiceAt + VOICE_SESSION_LIMITS.monologueEndOfTurnSilenceMs,
    peak: 0.004,
    rms: 0.003,
  });
  assert.equal(state.action, "end-of-turn");
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

test("live PCM keeps recording when the bounded HTTP fallback overflows", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const recorderSource = executableBridgeFunction(
    bridge,
    "function recordingHasLivePrimary(",
    "function confirmLiveSpeech(",
  );

  class FakeMediaRecorder {
    constructor() {
      this.listeners = new Map();
      this.mimeType = "audio/webm";
      this.state = "inactive";
      this.stopCalls = 0;
    }

    addEventListener(type, listener) {
      this.listeners.set(type, listener);
    }

    emitData(size) {
      this.listeners.get("dataavailable")?.({
        data: { size, type: this.mimeType },
      });
    }

    start() {
      this.state = "recording";
    }

    stop() {
      this.stopCalls += 1;
      this.state = "inactive";
    }
  }

  const factory = new Function(
    "dependencies",
    `"use strict";
let activeLiveSession;
let activeRecording;
let pendingLiveSession;
const AUDIO_MAX_BYTES = dependencies.maximumBytes;
const MediaRecorder = dependencies.MediaRecorder;
const createCaptureBuffer = dependencies.createCaptureBuffer;
const recorderOptions = () => ({});
const armCandidateDeadline = () => true;
const resolveRecording = () => {};
const rejectRecording = (recording, code) => {
  recording.rejectedWith = code;
};
const candidateEventIsCurrent = (recording, candidate) =>
  !recording.settled &&
  !candidate.discarded &&
  recording.candidate === candidate;
const requestRecordingStop = (recording, reason) => {
  recording.stopRequests.push(reason);
  recording.candidate?.recorder.stop();
};

${recorderSource}

return Object.freeze({
  clearLive(recording) {
    activeRecording = recording;
    activeLiveSession = undefined;
    pendingLiveSession = undefined;
  },
  setLive(recording) {
    activeRecording = recording;
    activeLiveSession = Object.freeze({});
    pendingLiveSession = undefined;
  },
  start(recording) {
    return startCandidateRecorder(recording, true, 0, null, undefined);
  },
});`,
  );
  const runtime = factory({
    createCaptureBuffer,
    maximumBytes: 100,
    MediaRecorder: FakeMediaRecorder,
  });
  const createRecording = () => ({
    candidate: undefined,
    discard: false,
    fallbackAudioComplete: true,
    settled: false,
    stopLatch: { isRequested: () => false },
    stopRequests: [],
    stream: Object.freeze({}),
    totalBytes: 0,
  });

  const liveRecording = createRecording();
  runtime.setLive(liveRecording);
  assert.equal(runtime.start(liveRecording), true);
  const liveRecorder = liveRecording.candidate.recorder;
  liveRecorder.emitData(100);
  liveRecorder.emitData(1);

  assert.equal(liveRecording.discard, false);
  assert.equal(liveRecording.fallbackAudioComplete, false);
  assert.equal(liveRecording.totalBytes, 0);
  assert.deepEqual(liveRecording.stopRequests, []);
  assert.equal(liveRecorder.state, "recording");
  assert.equal(liveRecorder.stopCalls, 0);
  assert.deepEqual(liveRecording.candidate.captureBuffer.snapshot(), {
    retainedBytes: 0,
    retainedChunks: 0,
    tooLarge: false,
    totalBytes: 0,
  });

  // Later recorder events are dropped; they can neither rebuild a partial
  // fallback nor grow local audio retention while live PCM continues.
  liveRecorder.emitData(75);
  assert.equal(liveRecording.totalBytes, 0);
  assert.equal(
    liveRecording.candidate.captureBuffer.snapshot().retainedBytes,
    0,
  );

  const fallbackOnlyRecording = createRecording();
  runtime.clearLive(fallbackOnlyRecording);
  assert.equal(runtime.start(fallbackOnlyRecording), true);
  const fallbackOnlyRecorder = fallbackOnlyRecording.candidate.recorder;
  fallbackOnlyRecorder.emitData(100);
  fallbackOnlyRecorder.emitData(1);
  assert.equal(fallbackOnlyRecording.discard, true);
  assert.deepEqual(fallbackOnlyRecording.stopRequests, ["too-large"]);
  assert.equal(fallbackOnlyRecorder.stopCalls, 1);
});

test("an incomplete recorder fallback can never be uploaded after live fails", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const resolveStart = bridge.indexOf("function resolveRecording(");
  const resolveEnd = bridge.indexOf(
    "\n}\n\nfunction requestRecordingStop",
    resolveStart,
  );
  const finishStart = bridge.indexOf("async function finishTurn(");
  const finishEnd = bridge.indexOf(
    "\n}\n\nfunction safeDocumentName",
    finishStart,
  );
  assert.notEqual(resolveStart, -1);
  assert.notEqual(resolveEnd, -1);
  assert.notEqual(finishStart, -1);
  assert.notEqual(finishEnd, -1);
  const resolve = bridge.slice(resolveStart, resolveEnd);
  const finish = bridge.slice(finishStart, finishEnd);

  assert.match(resolve, /fallbackAudioComplete: recording\.fallbackAudioComplete/u);
  assert.match(
    resolve,
    /captured\.totalBytes > 0 \|\| !recording\.fallbackAudioComplete/u,
  );
  const liveCommitAt = finish.indexOf("liveSession.commit(");
  const fallbackGuardAt = finish.indexOf(
    "capture.fallbackAudioComplete !== true",
  );
  const uploadReadAt = finish.indexOf("capture.blob.arrayBuffer()");
  assert.ok(liveCommitAt >= 0);
  assert.ok(fallbackGuardAt > liveCommitAt);
  assert.ok(uploadReadAt > fallbackGuardAt);
  assert.match(
    finish.slice(fallbackGuardAt, uploadReadAt),
    /fail\("voice_turn_too_large"\)/u,
  );
  assert.match(
    bridge,
    /nativeFallbackAllowed = shouldReplayCommittedNativeTurn\(\{[\s\S]*audioEventCount: snapshot\.audioEventCount,[\s\S]*code: message\.code,[\s\S]*committed: state === "committed",[\s\S]*interrupted: session\?\.playback\?\.interrupted === true,[\s\S]*nativeAudio,[\s\S]*\}\);/u,
  );
  assert.match(
    bridge,
    /canFallback\(\) \{\s*return !commitSent \|\| nativeFallbackAllowed;\s*\}/u,
  );
  assert.match(
    bridge,
    /nativeFallbackRequiresStatefulHTTP =\s*nativeFallbackAllowed && message\.code === "voice_native_fallback";/u,
  );
  assert.match(
    bridge,
    /requiresStatefulHTTPFallback\(\) \{[\s\S]*return nativeFallbackRequiresStatefulHTTP;\s*\}/u,
  );
  const statefulLatchAt = finish.indexOf(
    "liveSession.requiresStatefulHTTPFallback()",
  );
  const liveCancelAt = finish.indexOf("liveSession.cancel(", statefulLatchAt);
  const httpPlaybackAt = finish.indexOf(
    '"http",',
    statefulLatchAt,
  );
  assert.ok(statefulLatchAt > liveCommitAt);
  assert.ok(liveCancelAt > statefulLatchAt);
  assert.ok(httpPlaybackAt > liveCancelAt);
});

test("late recorder resolve and reject preserve the newer microphone owner", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const lifecycle = executableBridgeFunction(
    bridge,
    "function rejectRecording(",
    "function requestRecordingStop(",
  );
  const trackChanges = [];
  const runtime = new Function(
    "dependencies",
    `"use strict";
let activeRecording;
let activePlayback;
const candidateEventIsCurrent = () => true;
const stopVad = dependencies.stopVad;
const setStreamTracksEnabled = dependencies.setStreamTracksEnabled;
const discardCurrentCandidate = dependencies.discardCurrentCandidate;
${lifecycle}
return Object.freeze({
  rejectRecording,
  resolveRecording,
  setOwners(recording, playback) {
    activeRecording = recording;
    activePlayback = playback;
  },
});`,
  )({
    discardCurrentCandidate: () => true,
    setStreamTracksEnabled: (stream, enabled) => {
      trackChanges.push({ enabled, stream });
    },
    stopVad: () => {},
  });
  const makeRecording = (name) => {
    const results = {};
    return {
      recording: {
        candidate: undefined,
        discard: false,
        fallbackAudioComplete: false,
        rejectEnd(error) {
          results.endError = error;
        },
        rejectTurnEnded(error) {
          results.turnError = error;
        },
        resolveEnd(value) {
          results.end = value;
        },
        settled: false,
        stopLatch: {
          isRequested: () => false,
          request: () => true,
        },
        stopReason: "end-of-turn",
        stream: { name },
        turnEnded: false,
      },
      results,
    };
  };

  const lateResolve = makeRecording("late-resolve");
  runtime.setOwners({ stream: { name: "new-recording" } }, undefined);
  runtime.resolveRecording(lateResolve.recording, undefined);
  assert.equal(lateResolve.recording.settled, true);
  assert.equal(lateResolve.results.end.hasSpeech, false);
  assert.deepEqual(trackChanges, []);

  const lateReject = makeRecording("late-reject");
  runtime.setOwners(lateReject.recording, { interrupted: false });
  runtime.rejectRecording(lateReject.recording, "voice_turn_timeout");
  assert.match(lateReject.results.endError.message, /voice_turn_timeout/u);
  assert.match(lateReject.results.turnError.message, /voice_turn_timeout/u);
  assert.deepEqual(trackChanges, []);

  const current = makeRecording("current");
  runtime.setOwners(current.recording, undefined);
  runtime.resolveRecording(current.recording, undefined);
  assert.deepEqual(trackChanges, [
    { enabled: false, stream: current.recording.stream },
  ]);
});

test("abandon settles both the endpoint and recorder promises", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const source = executableBridgeFunction(
    bridge,
    "function abandonInterruptRecording(",
    "function stopBargeInMonitoring(",
  );
  const discarded = [];
  let stopCalls = 0;
  const abandon = new Function(
    "dependencies",
    `"use strict";
const stopVad = dependencies.stopVad;
const discardCurrentCandidate = dependencies.discardCurrentCandidate;
${source}
return abandonInterruptRecording;`,
  )({
    discardCurrentCandidate: (_recording, reason) => {
      discarded.push(reason);
      return true;
    },
    stopVad: () => {
      stopCalls += 1;
    },
  });
  let rejectTurnEnded;
  const turnEndedPromise = new Promise((_resolve, reject) => {
    rejectTurnEnded = reject;
  });
  let resolveEnd;
  const endPromise = new Promise((resolve) => {
    resolveEnd = resolve;
  });
  const recording = {
    resolveEnd,
    rejectTurnEnded,
    settled: false,
    turnEnded: false,
  };

  abandon(recording);
  await assert.rejects(turnEndedPromise, /request_cancelled/u);
  const end = await endPromise;
  assert.equal(recording.settled, true);
  assert.equal(recording.turnEnded, true);
  assert.equal(end.hasSpeech, false);
  assert.equal(end.reason, "interrupt-abandoned");
  assert.deepEqual(discarded, ["interrupt-abandoned"]);
  assert.equal(stopCalls, 1);
});

test("endpoint latch settles before MediaRecorder finalizes fallback", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const stop = executableBridgeFunction(
    bridge,
    "function requestRecordingStop(",
    "function currentAudioContextFrame(",
  );
  const finish = executableBridgeFunction(
    bridge,
    "async function finishTurn(",
    "function safeDocumentName(",
  );
  const resolve = executableBridgeFunction(
    bridge,
    "function resolveRecording(",
    "function requestRecordingStop(",
  );

  const endpointAt = stop.indexOf("recording.resolveTurnEnded(");
  const recorderStopAt = stop.indexOf("candidate.recorder.stop()");
  assert.ok(endpointAt >= 0);
  assert.ok(recorderStopAt > endpointAt);

  const turnEndAt = finish.indexOf("recording.turnEndedPromise");
  const liveCommitAt = finish.indexOf("liveSession.commit(");
  const fallbackCaptureAt = finish.indexOf("recording.endPromise");
  assert.ok(turnEndAt >= 0);
  assert.ok(liveCommitAt > turnEndAt);
  assert.ok(
    fallbackCaptureAt > liveCommitAt,
    "the fallback Blob must not gate a successful live commit",
  );
  assert.match(
    resolve,
    /activeRecording === recording && activePlayback === undefined/u,
    "a late fallback stop must not disable a newer barge-in microphone owner",
  );

  const requestStop = new Function(
    "dependencies",
    `"use strict";
const stopVad = dependencies.stopVad;
const rejectRecording = dependencies.rejectRecording;
const discardCurrentCandidate = dependencies.discardCurrentCandidate;
const resolveRecording = dependencies.resolveRecording;
${stop}
return requestRecordingStop;`,
  )({
    discardCurrentCandidate: () => true,
    rejectRecording: (_recording, code) => {
      throw new Error(code);
    },
    resolveRecording: () => {},
    stopVad: () => {},
  });
  let resolveTurnEnded;
  const turnEndedPromise = new Promise((resolveTurn) => {
    resolveTurnEnded = resolveTurn;
  });
  let resolveFallback;
  const endPromise = new Promise((resolveEnd) => {
    resolveFallback = resolveEnd;
  });
  let stopCalls = 0;
  const recording = {
    candidate: {
      confirmed: true,
      recorder: {
        state: "recording",
        stop() {
          stopCalls += 1;
          this.state = "inactive";
          // Intentionally never emit MediaRecorder's stop event.
        },
      },
    },
    discard: false,
    resolveTurnEnded,
    settled: false,
    stopLatch: {
      requested: false,
      isRequested() {
        return this.requested;
      },
      request() {
        if (this.requested) return false;
        this.requested = true;
        return true;
      },
    },
    endPromise,
    turnEnded: false,
    turnEndedPromise,
  };

  assert.equal(requestStop(recording, "end-of-turn"), true);
  assert.deepEqual(await turnEndedPromise, {
    hasSpeech: true,
    reason: "end-of-turn",
  });
  assert.equal(stopCalls, 1);
  let fallbackSettled = false;
  void endPromise.then(() => {
    fallbackSettled = true;
  });
  await Promise.resolve();
  assert.equal(fallbackSettled, false);

  let commitCalls = 0;
  const finishRuntime = new Function(
    "dependencies",
    `"use strict";
let activeLiveSession = dependencies.liveSession;
let activeRecording = dependencies.recording;
let activeRequestController;
let pendingDocument;
let pendingLiveSession;
let sessionEpoch = 1;
let audioContext = { state: "running" };
const AUDIO_MAX_BYTES = 1_000_000;
const SESSION_STATE_MAX_CHARS = 16_384;
const VOICE_TURN_CLIENT_TIMEOUT_MS = 5_000;
const performance = dependencies.performance;
const finishGate = dependencies.finishGate;
const isValidTurnMode = () => true;
const requestRecordingStop = dependencies.requestRecordingStop;
const beginSessionResponse = () => true;
const completeSessionResponse = () => true;
const cancelSessionResponse = () => {};
const stoppedSessionCode = () => "request_cancelled";
const clearPendingDocument = () => {};
const takePendingLiveSession = async () => undefined;
const setTracksEnabled = () => {};
const createStreamingPlayback = dependencies.createStreamingPlayback;
const awaitValidatedPlaybackCompletion = async () => {};
const retirePendingLiveSession = () => {};
const haltStreamingPlayback = () => {};
const shouldDiscardInterruptedPlaybackRecording = () => false;
const discardInterruptedPlaybackRecording = () => {};
const shouldAbortPlaybackTransportOnInterrupt = () => false;
const beginSession = () => {};
const secureCredentials = async () => { throw new Error("unexpected_fallback"); };
const arrayBufferToBase64 = () => { throw new Error("unexpected_fallback"); };
const fetch = () => { throw new Error("unexpected_fallback"); };
const VOICE_ENDPOINT = "https://invalid.example";
const consumeVoiceStream = async () => { throw new Error("unexpected_fallback"); };
const fail = (code) => { throw new Error(code); };
${finish}
return Object.freeze({
  finishTurn,
  getActiveRecording: () => activeRecording,
});`,
  )({
    createStreamingPlayback: () => ({ interrupted: false }),
    finishGate: {
      acquire: () => 1,
      isBusy: () => false,
      release: () => {},
    },
    liveSession: {
      nativeAudio: true,
      matches: () => true,
      async commit() {
        commitCalls += 1;
        return { finalResult: { state: "ok" } };
      },
      recordCompletion() {},
    },
    performance: { now: () => 0 },
    recording,
    requestRecordingStop: requestStop,
  });
  const finished = await finishRuntime.finishTurn(
    "",
    "foreground",
    false,
  );
  assert.equal(commitCalls, 1);
  assert.equal(finished.state, "ok");
  assert.equal(finishRuntime.getActiveRecording(), undefined);
  assert.equal(fallbackSettled, false);
  resolveFallback();
});

test("latency serialization distinguishes missing values from measured zero", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const start = bridge.indexOf("function boundedLatency(");
  const end = bridge.indexOf("\n\nfunction isPlainRecord(", start);
  assert.ok(start >= 0 && end > start);
  const events = [];
  const dispatch = new Function(
    "eventTarget",
    "CustomEvent",
    `"use strict";
const globalThis = eventTarget;
${bridge.slice(start, end)}
return dispatchVoiceLatency;`,
  )(
    { dispatchEvent: (event) => events.push(event) },
    class {
      constructor(type, options) {
        this.type = type;
        this.detail = options.detail;
      }
    },
  );

  dispatch({ substantiveAudio: false });
  const missing = events.at(-1).detail;
  for (const key of [
    "auth_ready_ms",
    "barge_halt_ms",
    "commit_to_estimated_audible_ms",
    "commit_to_first_audio_ms",
    "first_binary_ms",
    "speech_end_to_estimated_audible_ms",
    "turn_total_ms",
    "ws_open_ms",
  ]) {
    assert.equal(missing[key], null, `${key} must remain missing`);
  }

  dispatch({
    authReadyMs: 0,
    bargeHaltMs: 0,
    commitToEstimatedAudibleMs: 0,
    commitToFirstAudioMs: 0,
    firstBinaryMs: 0,
    speechEndToEstimatedAudibleMs: 0,
    substantiveAudio: true,
    turnTotalMs: 0,
    wsOpenMs: 0,
  });
  const measuredZero = events.at(-1).detail;
  assert.equal(measuredZero.commit_to_first_audio_ms, 0);
  assert.equal(measuredZero.commit_to_estimated_audible_ms, 0);
  assert.equal(measuredZero.first_binary_ms, 0);
  assert.equal(measuredZero.speech_end_to_estimated_audible_ms, 0);
  assert.equal(measuredZero.substantive_audio, true);
  assert.equal(
    Object.keys(measuredZero).some((key) => key.includes("presence")),
    false,
  );

  dispatch({
    commitToFirstAudioMs: Number.NaN,
    firstBinaryMs: -1,
    substantiveAudio: false,
  });
  const invalid = events.at(-1).detail;
  assert.equal(invalid.commit_to_first_audio_ms, null);
  assert.equal(invalid.first_binary_ms, null);

  dispatch({
    commitToFirstAudioMs: 17,
    firstBinaryMs: 16,
    substantiveAudio: false,
  });
  const silentTransport = events.at(-1).detail;
  assert.equal(silentTransport.commit_to_first_audio_ms, 17);
  assert.equal(silentTransport.first_binary_ms, 16);
  assert.equal(silentTransport.commit_to_estimated_audible_ms, null);
  assert.equal(silentTransport.speech_end_to_estimated_audible_ms, null);
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
    captureLimitMs: VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
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
    captureLimitMs: null,
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
    captureLimitMs: VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
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
        captureLimitMs: VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
        phase: "candidate",
      });
    } else if (sample < confirmationFrames) {
      assert.deepEqual(candidateState, {
        action: null,
        candidateStartedAt:
          startedAt + VOICE_SESSION_LIMITS.vadIntervalMs,
        captureLimitMs: VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
        phase: "candidate",
      });
    } else {
      assert.equal(vadState.hasSpeech, true);
      assert.deepEqual(candidateState, {
        action: "confirm",
        candidateStartedAt:
          startedAt + VOICE_SESSION_LIMITS.vadIntervalMs,
        captureLimitMs: VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
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
  assert.equal(VOICE_SESSION_LIMITS.candidateCaptureLimitMs, 200);
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
    captureLimitMs: null,
    phase: "armed",
  });
});

test("a late accumulated VAD confirmation cannot cross the privacy deadline", () => {
  const startedAt = 80_000;
  const candidate = advanceCandidateCapture(
    createCandidateCaptureState(),
    {
      hasSpeech: false,
      sampleVoiced: true,
      voiceRunMs: VOICE_SESSION_LIMITS.vadIntervalMs,
    },
    startedAt,
  );

  assert.deepEqual(
    advanceCandidateCapture(
      candidate,
      {
        hasSpeech: true,
        sampleVoiced: true,
        voiceRunMs: VOICE_SESSION_LIMITS.minimumVoiceMs,
      },
      startedAt + VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
    ),
    {
      action: "discard",
      candidateStartedAt: null,
      captureLimitMs: null,
      phase: "armed",
    },
  );
});

test("a clear onset upgrades once to the absolute soft deadline and barge-in remains finite", async () => {
  let now = 0;
  let vadState = createVadState(now);
  let candidate = createCandidateCaptureState();

  now += VOICE_SESSION_LIMITS.vadIntervalMs;
  vadState = advanceVad(vadState, { now, peak: 0.05, rms: 0.02 });
  candidate = advanceCandidateCapture(candidate, vadState, now);
  assert.equal(candidate.action, "start");
  assert.equal(vadState.softVoiceCandidate, false);

  const softFrames =
    VOICE_SESSION_LIMITS.softVoiceMinimumMs /
    VOICE_SESSION_LIMITS.vadIntervalMs;
  for (let frame = 0; frame < softFrames; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    const rms = frame % 2 === 0 ? 0.0065 : 0.0085;
    vadState = advanceVad(vadState, { now, peak: rms * 2, rms });
    candidate = advanceCandidateCapture(candidate, vadState, now);
  }
  assert.equal(candidate.action, "confirm");
  assert.equal(vadState.softVoiceConfirmed, true);
  assert.ok(
    now - candidate.candidateStartedAt >
      VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
  );
  assert.ok(
    now - candidate.candidateStartedAt <
      VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  );

  assert.equal(INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs, 2_400);
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const deadlineAt = bridge.indexOf("function armCandidateDeadline(");
  const deadline = bridge.slice(deadlineAt, deadlineAt + 2_400);
  assert.match(
    deadline,
    /captureLimitMs <= candidate\.captureLimitMs[\s\S]*candidate\.startedAt \+ captureLimitMs - performance\.now\(\)/u,
    "the quiet upgrade must be one-way and remain anchored to candidate onset",
  );
  const normalVadAt = bridge.indexOf("function armVad(");
  const normalVad = bridge.slice(normalVadAt, normalVadAt + 5_500);
  assert.match(
    normalVad,
    /vadState\.softVoiceCandidate[\s\S]*armCandidateDeadline\([\s\S]*VOICE_SESSION_LIMITS\.softCandidateCaptureLimitMs/u,
  );
  const interruptAt = bridge.indexOf("function startBargeInMonitoring(");
  const interrupt = bridge.slice(interruptAt, interruptAt + 5_500);
  assert.match(
    interrupt,
    /startCandidateRecorder\([\s\S]*vadState\.candidateStartedAt,[\s\S]*INTERRUPT_VAD_LIMITS\.candidateCaptureLimitMs/u,
  );
});

test("a quiet candidate keeps its finite upgraded deadline when the ending becomes clear", () => {
  let now = 0;
  let vadState = createVadState(now);
  let candidate = createCandidateCaptureState();
  for (let frame = 0; frame < 6; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, {
      now,
      peak: 0.013,
      rms: 0.0065,
    });
    candidate = advanceCandidateCapture(candidate, vadState, now);
  }
  assert.equal(candidate.phase, "candidate");
  assert.equal(
    candidate.captureLimitMs,
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  );

  for (let frame = 0; frame < 3; frame += 1) {
    now += VOICE_SESSION_LIMITS.vadIntervalMs;
    vadState = advanceVad(vadState, { now, peak: 0.08, rms: 0.03 });
    candidate = advanceCandidateCapture(candidate, vadState, now);
  }
  assert.equal(vadState.softVoiceCandidate, false);
  assert.equal(vadState.hasSpeech, true);
  assert.equal(candidate.action, "confirm");
  assert.equal(candidate.phase, "confirmed");
  assert.equal(
    candidate.captureLimitMs,
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  );
  assert.ok(
    now - candidate.candidateStartedAt >
      VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
  );
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

test("gesture epoch separates intentional authority from foreground response mode", () => {
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
  assert.equal(automaticFollowUp.turnMode, "foreground");
  assert.equal(isValidTurnMode(automaticFollowUp.turnMode), true);
  assert.equal(isValidTurnMode("derived-from-session-state"), false);
  assert.throws(() => turnModeForGestureEpoch("first"), /gesture_epoch_invalid/);
});
