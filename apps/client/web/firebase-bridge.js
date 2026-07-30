import { getApp, getApps, initializeApp } from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app.js";
import {
  browserSessionPersistence,
  getIdToken,
  initializeAuth,
  signInAnonymously,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-auth.js";
import {
  getToken as getAppCheckToken,
  initializeAppCheck,
  ReCaptchaEnterpriseProvider,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app-check.js";
import {
  advanceCandidateCapture,
  advanceVad,
  createCandidateCaptureState,
  createCaptureBuffer,
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
  VOICE_SESSION_LIMITS,
} from "./voice-session-policy.mjs";
import {
  advanceInterruptVad,
  createInterruptVadState,
  createVoiceLiveClientTransport,
  createVoiceLiveServerProtocol,
  createVoiceStreamParser,
  INTERRUPT_VAD_LIMITS,
  safeLiveCaptureFrame,
  VOICE_LIVE_LIMITS,
  shouldAbortVoiceTransportOnInterrupt,
  VOICE_STREAM_LIMITS,
} from "./voice-stream-policy.mjs";

const EXPECTED_PROJECT_ID = "kotae-ai-u22-2026";
const EXPECTED_APP_ID = "1:551920539470:web:6518baf6d84d7ab89eb01f";
const EXPECTED_AUTH_DOMAIN = "kotae-ai-u22-2026.firebaseapp.com";
const EXPECTED_MESSAGING_SENDER_ID = "551920539470";
// reCAPTCHA Enterprise site keys are public identifiers. The matching secret
// configuration and verification remain in Firebase App Check.
const RECAPTCHA_SITE_KEY = "6Le4EmotAAAAAPEp5sfcmDtCAeaKd4y9er6KA71U";
const VOICE_ENDPOINT =
  "https://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/turns:stream";
const VOICE_LIVE_ENDPOINT =
  "wss://kotae-api-r6kgkvtrmq-an.a.run.app/api/v1/voice/live";
const PCM_CAPTURE_WORKLET_URL = "/pcm-capture-worklet.js";
const VOICE_ORIGIN = new URL(VOICE_ENDPOINT).origin;
const VOICE_WARMUP_ENDPOINT = `${VOICE_ORIGIN}/healthz`;

const DOCUMENT_MAX_BYTES = 7 * 1024 * 1024;
const AUDIO_MAX_BYTES = 2 * 1024 * 1024;
const RESPONSE_AUDIO_MAX_BASE64_CHARS = 4 * Math.ceil(AUDIO_MAX_BYTES / 3);
const SESSION_STATE_MAX_CHARS = 16 * 1024;
const VAD_INTERVAL_MS = VOICE_SESSION_LIMITS.vadIntervalMs;

const ALLOWED_CONFIG_KEYS = Object.freeze([
  "apiKey",
  "appId",
  "authDomain",
  "messagingSenderId",
  "projectId",
]);

let authInstance;
let mediaStream;
let audioContext;
let analyser;
let analyserSource;
let analyserStream;
let activeRecording;
let activeRequestController;
let activePlayback;
let activeLiveSession;
let pendingDocument;
let pendingDocumentTimer;
let voiceTransportPrimed = false;
let sessionEpoch = 0;
let documentEpoch = 0;
const beginGate = createTurnGate();
const finishGate = createTurnGate();
const sessionClock = createSessionClock({
  now: () => performance.now(),
});
const pcmCaptureWorkletLoads = new WeakMap();

function fail(code) {
  throw new Error(code);
}

function boundedLatency(value) {
  if (!Number.isFinite(value) || value < 0) return 0;
  return Math.min(120_000, Math.round(value * 10) / 10);
}

function dispatchVoiceLatency({
  authReadyMs = 0,
  bargeHaltMs = 0,
  commitToFirstAudioMs = 0,
  turnTotalMs = 0,
  wsOpenMs = 0,
}) {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-latency", {
      detail: Object.freeze({
        auth_ready_ms: boundedLatency(authReadyMs),
        barge_halt_ms: boundedLatency(bargeHaltMs),
        commit_to_first_audio_ms: boundedLatency(
          commitToFirstAudioMs,
        ),
        turn_total_ms: boundedLatency(turnTotalMs),
        version: 1,
        ws_open_ms: boundedLatency(wsOpenMs),
      }),
    }),
  );
}

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function verifiedConfig(raw) {
  if (!isPlainRecord(raw)) {
    fail("firebase_config_invalid");
  }
  if (raw.projectId !== EXPECTED_PROJECT_ID || raw.appId !== EXPECTED_APP_ID) {
    fail("firebase_project_mismatch");
  }
  if (
    typeof raw.apiKey !== "string" ||
    raw.apiKey.length < 20 ||
    raw.authDomain !== EXPECTED_AUTH_DOMAIN ||
    raw.messagingSenderId !== EXPECTED_MESSAGING_SENDER_ID
  ) {
    fail("firebase_config_invalid");
  }

  const config = Object.create(null);
  for (const key of ALLOWED_CONFIG_KEYS) {
    const value = raw[key];
    if (typeof value === "string" && value.length > 0) {
      config[key] = value;
    }
  }
  return Object.freeze(config);
}

async function loadFirebaseConfig() {
  const response = await fetch("/__/firebase/init.json", {
    cache: "no-store",
    credentials: "same-origin",
    redirect: "error",
    referrerPolicy: "no-referrer",
  });
  if (!response.ok) {
    fail("firebase_config_unavailable");
  }
  return verifiedConfig(await response.json());
}

function siteKeyConfigured() {
  return (
    RECAPTCHA_SITE_KEY !== "__RECAPTCHA_SITE_KEY__" &&
    RECAPTCHA_SITE_KEY.length >= 20
  );
}

async function initializeAppServices() {
  if (!siteKeyConfigured()) {
    fail("app_check_not_configured");
  }

  const config = await loadFirebaseConfig();
  const app = getApps().length === 0 ? initializeApp(config) : getApp();
  if (
    app.options.projectId !== EXPECTED_PROJECT_ID ||
    app.options.appId !== EXPECTED_APP_ID
  ) {
    fail("firebase_project_mismatch");
  }

  const appCheck = initializeAppCheck(app, {
    provider: new ReCaptchaEnterpriseProvider(RECAPTCHA_SITE_KEY),
    isTokenAutoRefreshEnabled: true,
  });
  return Object.freeze({ app, appCheck });
}

const appServices = createRetryableInitializer(initializeAppServices);

async function initializeAuthenticatedUser() {
  const { app, appCheck } = await appServices();
  // Authentication has App Check enforcement enabled in production. Prime the
  // attestation token before the first anonymous sign-in so a fresh browser
  // never races Auth with the reCAPTCHA Enterprise exchange.
  await getAppCheckToken(appCheck, false);
  authInstance ??= initializeAuth(app, {
    persistence: browserSessionPersistence,
  });
  const auth = authInstance;
  const credential = auth.currentUser
    ? { user: auth.currentUser }
    : await signInAnonymously(auth);
  return Object.freeze({ user: credential.user });
}

const authenticatedUser = createRetryableInitializer(
  initializeAuthenticatedUser,
);

async function secureCredentials() {
  try {
    const [{ appCheck }, { user }] = await Promise.all([
      appServices(),
      authenticatedUser(),
    ]);
    const [idToken, appCheckResult] = await Promise.all([
      getIdToken(user, false),
      getAppCheckToken(appCheck, false),
    ]);
    return Object.freeze({
      appCheckToken: appCheckResult.token,
      idToken,
    });
  } catch (error) {
    if (error instanceof Error && error.message === "app_check_not_configured") {
      throw error;
    }
    fail("authentication_failed");
  }
}

function primeVoiceTransportConnection() {
  if (voiceTransportPrimed) return;
  voiceTransportPrimed = true;

  const preconnect = document.createElement("link");
  preconnect.rel = "preconnect";
  preconnect.href = VOICE_ORIGIN;
  preconnect.crossOrigin = "anonymous";
  document.head.append(preconnect);

  // This carries no audio, transcript, identity token, or session state. It is
  // started only after the user opens a voice session, so DNS/TLS and a
  // possible Cloud Run cold start overlap the user's first utterance.
  void fetch(VOICE_WARMUP_ENDPOINT, {
    method: "GET",
    cache: "no-store",
    credentials: "omit",
    mode: "no-cors",
    redirect: "error",
    referrerPolicy: "no-referrer",
  }).catch(() => {});
}

async function getStatus() {
  if (!siteKeyConfigured()) {
    return Object.freeze({ state: "configuration-required" });
  }
  try {
    const { appCheckToken, idToken } = await secureCredentials();
    const response = await fetch("/api/v1/me", {
      method: "GET",
      cache: "no-store",
      credentials: "same-origin",
      redirect: "error",
      referrerPolicy: "no-referrer",
      headers: {
        Authorization: `Bearer ${idToken}`,
        "X-Firebase-AppCheck": appCheckToken,
      },
    });
    if (!response.ok) {
      fail("voice_api_unavailable");
    }
    return Object.freeze({ state: "ready" });
  } catch {
    return Object.freeze({ state: "unavailable" });
  }
}

function classifyMicrophoneError(error) {
  const name =
    error && typeof error === "object" && typeof error.name === "string"
      ? error.name
      : "";
  if (name === "NotAllowedError" || name === "SecurityError") {
    return "microphone_permission_denied";
  }
  if (
    name === "NotFoundError" ||
    name === "NotReadableError" ||
    name === "OverconstrainedError"
  ) {
    return "microphone_unavailable";
  }
  return "microphone_unsupported";
}

function setStreamTracksEnabled(stream, enabled) {
  if (!stream) return;
  for (const track of stream.getAudioTracks()) {
    if (track.readyState === "live") {
      track.enabled = enabled;
    }
  }
}

function setTracksEnabled(enabled) {
  setStreamTracksEnabled(mediaStream, enabled);
}

function stopTracks(stream) {
  if (!stream) return;
  for (const track of stream.getTracks()) {
    track.stop();
  }
}

function releaseMicrophone() {
  const recording = activeRecording;
  activeRecording = undefined;
  if (recording) {
    recording.discard = true;
    recording.totalBytes = 0;
    // Rejecting the owned recording is the single cancellation path: it
    // stops VAD, clears and detaches the current candidate recorder, and
    // settles any Rust task waiting on endPromise.
    rejectRecording(recording, "request_cancelled");
  }
  setTracksEnabled(false);
  stopTracks(mediaStream);
  mediaStream = undefined;
  if (analyserSource) {
    analyserSource.disconnect();
    analyserSource = undefined;
  }
  if (analyser) {
    analyser.disconnect();
    analyser = undefined;
  }
  analyserStream = undefined;
  if (audioContext && audioContext.state !== "closed") {
    void audioContext.close();
  }
  audioContext = undefined;
}

function hasLiveAudioTrack(stream) {
  return Boolean(
    stream &&
      stream
        .getAudioTracks()
        .some((track) => track.readyState === "live"),
  );
}

async function ensureMediaStream(expectedEpoch) {
  if (hasLiveAudioTrack(mediaStream)) {
    return mediaStream;
  }
  if (
    !navigator.mediaDevices ||
    typeof navigator.mediaDevices.getUserMedia !== "function" ||
    typeof globalThis.MediaRecorder !== "function"
  ) {
    fail("microphone_unsupported");
  }

  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        autoGainControl: true,
        channelCount: 1,
        echoCancellation: true,
        noiseSuppression: true,
      },
      video: false,
    });
  } catch (error) {
    fail(classifyMicrophoneError(error));
  }

  if (expectedEpoch !== sessionEpoch) {
    stopTracks(stream);
    fail("request_cancelled");
  }
  if (!hasLiveAudioTrack(stream) || stream.getVideoTracks().length !== 0) {
    stopTracks(stream);
    fail("microphone_unavailable");
  }
  mediaStream = stream;
  return mediaStream;
}

function createAudioContext() {
  const AudioContextConstructor =
    globalThis.AudioContext || globalThis.webkitAudioContext;
  if (typeof AudioContextConstructor !== "function") {
    fail("microphone_unsupported");
  }
  try {
    return new AudioContextConstructor({
      latencyHint: "interactive",
      sampleRate: 16_000,
    });
  } catch {
    try {
      return new AudioContextConstructor({ latencyHint: "interactive" });
    } catch {
      return new AudioContextConstructor();
    }
  }
}

async function ensureAudioGraph(stream, expectedEpoch) {
  let context = audioContext;
  if (!context || context.state === "closed") {
    context = createAudioContext();
    audioContext = context;
    analyser = undefined;
    analyserSource = undefined;
    analyserStream = undefined;
  }
  if (context.state === "suspended") {
    await context.resume();
  }
  if (expectedEpoch !== sessionEpoch || audioContext !== context) {
    if (audioContext === context) {
      audioContext = undefined;
      analyser = undefined;
      analyserSource = undefined;
      analyserStream = undefined;
    }
    if (context.state !== "closed") {
      void context.close();
    }
    fail("request_cancelled");
  }
  if (!analyser || !analyserSource || analyserStream !== stream) {
    if (analyserSource) {
      analyserSource.disconnect();
    }
    if (analyser) {
      analyser.disconnect();
    }
    const nextAnalyser = context.createAnalyser();
    nextAnalyser.fftSize = 1024;
    nextAnalyser.smoothingTimeConstant = 0.18;
    const nextSource = context.createMediaStreamSource(stream);
    nextSource.connect(nextAnalyser);
    analyser = nextAnalyser;
    analyserSource = nextSource;
    analyserStream = stream;
  }
}

function recorderOptions() {
  const candidates = [
    "audio/webm;codecs=opus",
    "audio/webm",
    "audio/mp4",
  ];
  for (const mimeType of candidates) {
    if (MediaRecorder.isTypeSupported(mimeType)) {
      return { mimeType, audioBitsPerSecond: 48_000 };
    }
  }
  return { audioBitsPerSecond: 48_000 };
}

function stopVad(recording) {
  if (recording.vadTimer !== undefined) {
    clearInterval(recording.vadTimer);
    recording.vadTimer = undefined;
  }
}

function recordingErrorCode(recording) {
  return recording.stopReason === "too-large"
    ? "voice_turn_too_large"
    : "request_cancelled";
}

function candidateEventIsCurrent(recording, candidate) {
  return (
    !recording.settled &&
    !candidate.discarded &&
    recording.candidate === candidate
  );
}

function stopDetachedCandidate(candidate) {
  candidate.discarded = true;
  if (candidate.recorder.state === "inactive") {
    return true;
  }
  try {
    candidate.recorder.stop();
    return true;
  } catch {
    return false;
  }
}

function discardCurrentCandidate(recording, reason = "candidate-rejected") {
  const candidate = recording.candidate;
  recording.candidate = undefined;
  recording.totalBytes = 0;
  if (!candidate) {
    return true;
  }
  candidate.captureBuffer.clear();
  candidate.stopReason = reason;
  return stopDetachedCandidate(candidate);
}

function rejectRecording(recording, code) {
  if (recording.settled) return;
  recording.settled = true;
  recording.discard = true;
  if (!recording.stopLatch.isRequested()) {
    recording.stopLatch.request("failed");
  }
  stopVad(recording);
  setStreamTracksEnabled(recording.stream, false);
  discardCurrentCandidate(recording);
  recording.rejectEnd(new Error(code));
}

function resolveRecording(recording, candidate) {
  if (recording.settled) return;
  if (
    candidate !== undefined &&
    (!candidateEventIsCurrent(recording, candidate) || !candidate.confirmed)
  ) {
    rejectRecording(recording, "voice_turn_invalid");
    return;
  }
  recording.settled = true;
  stopVad(recording);
  setStreamTracksEnabled(recording.stream, false);
  const captured =
    candidate === undefined
      ? Object.freeze({ chunks: [], totalBytes: 0 })
      : candidate.captureBuffer.take();
  const mimeType =
    candidate?.recorder.mimeType ||
    captured.chunks[0]?.type ||
    "audio/webm";
  const confirmedSpeech =
    candidate !== undefined &&
    candidate.confirmed &&
    captured.totalBytes > 0;
  recording.candidate = undefined;
  if (candidate) {
    candidate.discarded = true;
  }
  const blob = new Blob(confirmedSpeech ? captured.chunks : [], {
    type: mimeType,
  });
  recording.resolveEnd(
    Object.freeze({
      blob,
      hasSpeech: confirmedSpeech && blob.size > 0,
      mimeType,
      reason: recording.stopReason,
    }),
  );
}

function requestRecordingStop(recording, reason) {
  if (
    !recording ||
    recording.settled ||
    !recording.stopLatch.request(reason)
  ) {
    return false;
  }
  recording.stopReason = reason;
  stopVad(recording);
  if (recording.discard) {
    rejectRecording(recording, recordingErrorCode(recording));
    return true;
  }

  const candidate = recording.candidate;
  if (!candidate || !candidate.confirmed) {
    if (!discardCurrentCandidate(recording)) {
      rejectRecording(recording, "voice_turn_invalid");
      return true;
    }
    resolveRecording(recording, undefined);
    return true;
  }
  if (candidate.recorder.state !== "recording") {
    rejectRecording(recording, "voice_turn_invalid");
    return true;
  }
  try {
    candidate.recorder.stop();
  } catch {
    rejectRecording(recording, "voice_turn_invalid");
  }
  return true;
}

function startCandidateRecorder(recording, confirmed) {
  if (
    recording.candidate ||
    recording.settled ||
    recording.discard ||
    recording.stopLatch.isRequested()
  ) {
    rejectRecording(recording, "voice_turn_invalid");
    return false;
  }

  let recorder;
  try {
    recorder = new MediaRecorder(recording.stream, recorderOptions());
  } catch {
    rejectRecording(recording, "voice_turn_invalid");
    return false;
  }
  const candidate = {
    captureBuffer: createCaptureBuffer({ maximumBytes: AUDIO_MAX_BYTES }),
    confirmed,
    discarded: false,
    recorder,
    stopReason: "",
  };
  recording.totalBytes = 0;
  recording.candidate = candidate;
  recorder.addEventListener("dataavailable", (event) => {
    // A discarded recorder still emits a final Blob. Object identity keeps
    // that stale event out of a newer candidate and its fresh container.
    if (
      !candidateEventIsCurrent(recording, candidate) ||
      !event.data ||
      event.data.size === 0
    ) {
      return;
    }
    const captureState = candidate.captureBuffer.append(event.data);
    recording.totalBytes = captureState.totalBytes;
    if (captureState.tooLarge) {
      recording.discard = true;
      requestRecordingStop(recording, "too-large");
    }
  });

  recorder.addEventListener(
    "error",
    () => {
      if (candidateEventIsCurrent(recording, candidate)) {
        rejectRecording(recording, "voice_turn_invalid");
      }
    },
    { once: true },
  );

  recorder.addEventListener(
    "stop",
    () => {
      if (!candidateEventIsCurrent(recording, candidate)) {
        return;
      }
      if (
        recording.discard ||
        !recording.stopLatch.isRequested() ||
        !candidate.confirmed
      ) {
        rejectRecording(
          recording,
          recording.discard
            ? recordingErrorCode(recording)
            : "voice_turn_invalid",
        );
        return;
      }
      resolveRecording(recording, candidate);
    },
    { once: true },
  );

  try {
    recorder.start(250);
    return true;
  } catch {
    if (recording.candidate === candidate) {
      recording.candidate = undefined;
    }
    candidate.discarded = true;
    candidate.captureBuffer.clear();
    recording.totalBytes = 0;
    rejectRecording(recording, "voice_turn_invalid");
    return false;
  }
}

function armVad(recording) {
  const pcm = new Float32Array(analyser.fftSize);
  let vadState = createVadState(recording.startedAt);
  let candidateCapture = createCandidateCaptureState();

  recording.vadTimer = setInterval(() => {
    if (
      recording.discard ||
      recording.settled ||
      recording.stopLatch.isRequested() ||
      !analyser
    ) {
      return;
    }

    analyser.getFloatTimeDomainData(pcm);
    let sumSquares = 0;
    let peak = 0;
    for (let index = 0; index < pcm.length; index += 1) {
      const magnitude = Math.abs(pcm[index]);
      sumSquares += magnitude * magnitude;
      if (magnitude > peak) peak = magnitude;
    }
    const rms = Math.sqrt(sumSquares / pcm.length);
    const now = performance.now();
    vadState = advanceVad(vadState, { now, peak, rms });
    candidateCapture = advanceCandidateCapture(
      candidateCapture,
      vadState,
      now,
    );
    if (candidateCapture.action === "start") {
      // Start on the first voiced analysis frame, limiting loss at the start
      // of a word to one VAD interval. Each candidate owns a fresh recorder
      // and therefore a self-contained WebM/MP4 header.
      if (
        !startCandidateRecorder(
          recording,
          candidateCapture.phase === "confirmed",
        )
      ) {
        return;
      }
    }
    if (candidateCapture.action === "confirm") {
      const candidate = recording.candidate;
      if (!candidate || !candidateEventIsCurrent(recording, candidate)) {
        rejectRecording(recording, "voice_turn_invalid");
        return;
      }
      candidate.confirmed = true;
    }
    if (candidateCapture.action === "discard") {
      if (!discardCurrentCandidate(recording)) {
        rejectRecording(recording, "voice_turn_invalid");
        return;
      }
    }
    if (vadState.action !== null) {
      requestRecordingStop(recording, vadState.action);
    }
  }, VAD_INTERVAL_MS);
}

function createRecordingState(stream) {
  let resolveEnd;
  let rejectEnd;
  const endPromise = new Promise((resolve, reject) => {
    resolveEnd = resolve;
    rejectEnd = reject;
  });
  // A stop can race the Rust caller before it starts awaiting the turn.
  // Mark the rejection handled without changing what later awaiters observe.
  void endPromise.catch(() => {});
  const recording = {
    candidate: undefined,
    discard: false,
    endPromise,
    resolveEnd,
    rejectEnd,
    settled: false,
    startedAt: performance.now(),
    stopLatch: createStopLatch(),
    stopReason: "",
    stream,
    totalBytes: 0,
    vadTimer: undefined,
  };
  return recording;
}

function createRecording(stream) {
  const recording = createRecordingState(stream);
  armVad(recording);
  return recording;
}

async function beginTurn(serializedSessionState, turnMode) {
  if (document.hidden) {
    stopSession();
    fail("request_cancelled");
  }
  if (
    typeof serializedSessionState !== "string" ||
    serializedSessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode) ||
    activeRecording ||
    activeLiveSession ||
    beginGate.isBusy() ||
    finishGate.isBusy()
  ) {
    fail("voice_turn_invalid");
  }
  const beginToken = beginGate.acquire();
  if (beginToken === null) {
    fail("voice_turn_invalid");
  }

  try {
    const sessionStatus = sessionClock.begin();
    if (!sessionStatus.ok) {
      stopSession();
      fail("session_expired");
    }
    primeVoiceTransportConnection();

    const expectedEpoch = sessionEpoch;
    return await initializeWithCleanup(
      async () => {
        const credentials = await secureCredentials();
        if (expectedEpoch !== sessionEpoch) {
          fail("request_cancelled");
        }
        const stream = await ensureMediaStream(expectedEpoch);
        await ensureAudioGraph(stream, expectedEpoch);
        if (expectedEpoch !== sessionEpoch) {
          fail("request_cancelled");
        }

        setStreamTracksEnabled(stream, true);
        activeRecording = createRecording(stream);
        activeLiveSession = await startVoiceLiveSession({
          ...credentials,
          expectedEpoch,
          sessionState: serializedSessionState,
          stream,
          turnMode,
        });
        return Object.freeze({ state: "listening" });
      },
      () => {
        if (expectedEpoch === sessionEpoch) {
          activeLiveSession?.cancel(new Error("request_cancelled"));
          activeLiveSession = undefined;
          releaseMicrophone();
          sessionClock.reset();
        }
      },
    );
  } finally {
    beginGate.release(beginToken);
  }
}

async function waitForTurnEnd() {
  const recording = activeRecording;
  if (!recording) {
    fail("voice_turn_invalid");
  }
  let capture;
  try {
    capture = await recording.endPromise;
  } catch (error) {
    activeLiveSession?.cancel(
      error instanceof Error ? error : new Error("request_cancelled"),
    );
    activeLiveSession = undefined;
    stopVad(recording);
    recording.discard = true;
    requestRecordingStop(recording, "cancelled");
    if (activeRecording === recording) {
      activeRecording = undefined;
    }
    throw error;
  }
  if (activeRecording !== recording) {
    fail("request_cancelled");
  }
  if (!capture.hasSpeech) {
    activeLiveSession?.cancel(new Error("no_speech"));
    activeLiveSession = undefined;
    activeRecording = undefined;
  } else {
    sessionClock.markSpeech();
  }
  return Object.freeze({
    hasSpeech: capture.hasSpeech,
    manual: capture.reason === "manual",
  });
}

function endTurn() {
  const recording = activeRecording;
  if (!recording || recording.settled) {
    fail("voice_turn_invalid");
  }
  // A manual click may race the automatic silence boundary or another click.
  // The stop latch keeps all of those paths on the same single completion and
  // therefore the same single POST.
  requestRecordingStop(recording, "manual");
  return Object.freeze({ state: "ending" });
}

function arrayBufferToBase64(buffer) {
  const bytes = new Uint8Array(buffer);
  const chunks = [];
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    const slice = bytes.subarray(offset, offset + chunkSize);
    let binary = "";
    for (let index = 0; index < slice.length; index += 1) {
      binary += String.fromCharCode(slice[index]);
    }
    chunks.push(binary);
  }
  return btoa(chunks.join(""));
}

function base64ToArrayBuffer(base64) {
  let binary;
  try {
    binary = atob(base64);
  } catch {
    fail("voice_response_invalid");
  }
  const bytes = new Uint8Array(binary.length);
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }
  return bytes.buffer;
}

function isBase64(value) {
  return (
    typeof value === "string" &&
    value.length % 4 === 0 &&
    /^[A-Za-z0-9+/]*={0,2}$/.test(value)
  );
}

function boundedString(value, maxLength) {
  return typeof value === "string" && Array.from(value).length <= maxLength;
}

function clearPendingDocument(reason = "cleared") {
  const hadPendingDocument = pendingDocument !== undefined;
  pendingDocument = undefined;
  if (pendingDocumentTimer !== undefined) {
    clearTimeout(pendingDocumentTimer);
    pendingDocumentTimer = undefined;
  }
  const input = document.getElementById("paper-input");
  if (input instanceof HTMLInputElement) {
    input.value = "";
  }
  if (hadPendingDocument) {
    globalThis.dispatchEvent(
      new CustomEvent("kotae:document-cleared", {
        detail: Object.freeze({ reason }),
      }),
    );
  }
}

function armPendingDocumentExpiry(documentForExpiry, attachedAt) {
  if (pendingDocumentTimer !== undefined) {
    clearTimeout(pendingDocumentTimer);
  }
  pendingDocumentTimer = setTimeout(() => {
    pendingDocumentTimer = undefined;
    if (
      pendingDocument === documentForExpiry &&
      isPendingDocumentExpired(attachedAt, performance.now())
    ) {
      clearPendingDocument("expired");
    }
  }, VOICE_SESSION_LIMITS.pendingDocumentLimitMs);
}

function safeVoiceResponse(payload) {
  if (!isPlainRecord(payload)) {
    fail("voice_response_invalid");
  }
  const hasAudio = payload.audioBase64 !== "";
  if (
    !isBase64(payload.audioBase64) ||
    payload.audioBase64.length > RESPONSE_AUDIO_MAX_BASE64_CHARS ||
    (hasAudio &&
      (!boundedString(payload.audioMimeType, 100) ||
        !payload.audioMimeType.startsWith("audio/"))) ||
    (!hasAudio &&
      payload.audioMimeType !== "" &&
      (!boundedString(payload.audioMimeType, 100) ||
        !payload.audioMimeType.startsWith("audio/"))) ||
    typeof payload.sessionState !== "string" ||
    payload.sessionState.length > SESSION_STATE_MAX_CHARS ||
    !boundedString(payload.detectedDomain, 100) ||
    (payload.assistanceTarget !== "assistant" &&
      payload.assistanceTarget !== "respondent") ||
    !["none", "awaiting_answer", "restructure"].includes(
      payload.respondentStage,
    ) ||
    !boundedString(payload.route, 100) ||
    typeof payload.needsPaper !== "boolean" ||
    (payload.caption !== undefined &&
      payload.caption !== null &&
      !boundedString(payload.caption, 2_000))
  ) {
    fail("voice_response_invalid");
  }

  let research;
  try {
    research = normalizeResearchDiscovery(
      payload.researchStatus,
      payload.researchRecords,
    );
  } catch {
    fail("voice_response_invalid");
  }

  return Object.freeze({
    audioBase64: payload.audioBase64,
    audioMimeType: payload.audioMimeType,
    caption: typeof payload.caption === "string" ? payload.caption : null,
    detectedDomain: payload.detectedDomain,
    assistanceTarget: payload.assistanceTarget,
    respondentStage: payload.respondentStage,
    needsPaper: payload.needsPaper,
    researchStatus: research.status,
    researchRecords: research.records,
    route: payload.route,
    sessionState: payload.sessionState,
  });
}

function mapVoiceResponseError(status) {
  if (status === 401 || status === 403) return "authentication_failed";
  if (status === 413) return "voice_turn_too_large";
  if (status === 422) return "voice_turn_invalid";
  if (status === 429) return "rate_limited";
  if (status === 404 || status === 501 || status === 503) {
    return "voice_api_unavailable";
  }
  return "voice_api_unavailable";
}

function liveVoiceSupported(stream) {
  return Boolean(
    typeof globalThis.WebSocket === "function" &&
      typeof globalThis.AudioWorkletNode === "function" &&
      audioContext &&
      audioContext.state !== "closed" &&
      audioContext.audioWorklet &&
      typeof audioContext.audioWorklet.addModule === "function" &&
      hasVerifiedEchoCancellation(stream),
  );
}

function loadPcmCaptureWorklet(context) {
  let load = pcmCaptureWorkletLoads.get(context);
  if (!load) {
    load = context.audioWorklet.addModule(PCM_CAPTURE_WORKLET_URL);
    pcmCaptureWorkletLoads.set(context, load);
    void load.catch(() => {
      if (pcmCaptureWorkletLoads.get(context) === load) {
        pcmCaptureWorkletLoads.delete(context);
      }
    });
  }
  return load;
}

function liveCredential(value) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= 8_192 &&
    !/[\u0000-\u001f\u007f]/u.test(value)
  );
}

async function startVoiceLiveSession({
  appCheckToken,
  expectedEpoch,
  idToken,
  sessionState,
  stream,
  turnMode,
}) {
  if (
    pendingDocument ||
    !liveVoiceSupported(stream) ||
    !liveCredential(appCheckToken) ||
    !liveCredential(idToken) ||
    typeof sessionState !== "string" ||
    sessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode)
  ) {
    return undefined;
  }
  const liveStartedAt = performance.now();
  let socket;
  let socketOpenedAt;
  try {
    socket = new WebSocket(VOICE_LIVE_ENDPOINT);
    socket.binaryType = "arraybuffer";
  } catch {
    return undefined;
  }

  let clientTransport;
  let protocol;
  try {
    clientTransport = createVoiceLiveClientTransport(socket, {
      type: "start",
      version: 1,
      idToken,
      appCheckToken,
      sessionState,
      turnMode,
      sampleRateHz: VOICE_LIVE_LIMITS.inputSampleRateHz,
    });
    protocol = createVoiceLiveServerProtocol((result) =>
      safeVoiceResponse(result),
    );
  } catch {
    socket.close(1000, "http_fallback");
    return undefined;
  }

  let preflightAuthReadyMs = 0;
  let preflightError;
  let preflightState = "connecting";
  function failPreflight(error, close = true) {
    if (preflightError) return;
    preflightError =
      error instanceof Error
        ? error
        : new Error("voice_api_unavailable");
    preflightState = "failed";
    clientTransport.close();
    if (
      close &&
      (socket.readyState === WebSocket.CONNECTING ||
        socket.readyState === WebSocket.OPEN)
    ) {
      socket.close(4002, "voice_live_failed");
    }
  }
  const acceptPreflightOpen = () => {
    socketOpenedAt ??= performance.now();
    if (expectedEpoch !== sessionEpoch) {
      failPreflight(new Error("request_cancelled"));
      return;
    }
    try {
      clientTransport.open();
      preflightState = "awaiting-ready";
    } catch {
      failPreflight(new Error("voice_api_unavailable"));
    }
  };
  const acceptPreflightMessage = (event) => {
    if (preflightError) return;
    try {
      if (typeof event.data !== "string") {
        fail("voice_response_invalid");
      }
      const message = protocol.acceptText(event.data);
      if (message.type === "error") {
        failPreflight(new Error(message.code));
        return;
      }
      if (
        message.type !== "ready" ||
        preflightState !== "awaiting-ready"
      ) {
        fail("voice_response_invalid");
      }
      clientTransport.markReady();
      preflightAuthReadyMs = performance.now() - liveStartedAt;
      preflightState = "ready";
    } catch (error) {
      failPreflight(
        error instanceof Error
          ? error
          : new Error("voice_response_invalid"),
      );
    }
  };
  const acceptPreflightClose = () => {
    if (!preflightError) {
      failPreflight(new Error("voice_api_unavailable"), false);
    }
  };
  const acceptPreflightError = () =>
    failPreflight(new Error("voice_api_unavailable"));
  function detachPreflight() {
    socket.removeEventListener("open", acceptPreflightOpen);
    socket.removeEventListener("message", acceptPreflightMessage);
    socket.removeEventListener("close", acceptPreflightClose);
    socket.removeEventListener("error", acceptPreflightError);
  }
  socket.addEventListener("open", acceptPreflightOpen, { once: true });
  socket.addEventListener("message", acceptPreflightMessage);
  socket.addEventListener("close", acceptPreflightClose, {
    once: true,
  });
  socket.addEventListener("error", acceptPreflightError, { once: true });

  let workletTimeout;
  try {
    await Promise.race([
      loadPcmCaptureWorklet(audioContext),
      new Promise((_, reject) => {
        workletTimeout = setTimeout(
          () => reject(new Error("voice_api_unavailable")),
          Math.max(
            0,
            VOICE_LIVE_LIMITS.readyTimeoutMs -
              (performance.now() - liveStartedAt),
          ),
        );
      }),
    ]);
  } catch {
    detachPreflight();
    clientTransport.close();
    if (
      socket.readyState === WebSocket.CONNECTING ||
      socket.readyState === WebSocket.OPEN
    ) {
      socket.close(1000, "http_fallback");
    }
    return undefined;
  } finally {
    if (workletTimeout !== undefined) clearTimeout(workletTimeout);
  }
  detachPreflight();
  if (
    expectedEpoch !== sessionEpoch ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    if (
      socket.readyState === WebSocket.CONNECTING ||
      socket.readyState === WebSocket.OPEN
    ) {
      clientTransport.close();
      socket.close(1000, "stale");
    }
    fail("request_cancelled");
  }
  if (preflightError) {
    return undefined;
  }

  let captureNode;
  let captureSource;
  try {
    captureNode = new AudioWorkletNode(
      audioContext,
      "kotae-pcm-capture",
      {
        channelCount: 1,
        channelCountMode: "explicit",
        numberOfInputs: 1,
        numberOfOutputs: 0,
      },
    );
    captureSource = audioContext.createMediaStreamSource(stream);
  } catch {
    captureNode?.disconnect();
    captureSource?.disconnect();
    if (
      socket.readyState === WebSocket.CONNECTING ||
      socket.readyState === WebSocket.OPEN
    ) {
      clientTransport.close();
      socket.close(1000, "http_fallback");
    }
    return undefined;
  }

  let captureStopped = false;
  let authReadyMs = preflightAuthReadyMs;
  let authReadyTimer;
  let commitAt;
  let commitToFirstAudioMs = 0;
  let commitSent = false;
  let latencyDispatched = false;
  let finalResult;
  let readySettled = false;
  let rejectReady;
  let rejectResult;
  let resolveReady;
  let resolveResult;
  let resultSettled = false;
  let state = preflightState;
  let wsOpenMs =
    socketOpenedAt === undefined
      ? 0
      : socketOpenedAt - liveStartedAt;
  const readyPromise = new Promise((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const resultPromise = new Promise((resolve, reject) => {
    resolveResult = resolve;
    rejectResult = reject;
  });
  void readyPromise.catch(() => {});
  void resultPromise.catch(() => {});

  function stopCapture() {
    if (captureStopped) return;
    captureStopped = true;
    captureNode.port.onmessage = null;
    try {
      captureNode.port.postMessage(
        Object.freeze({ type: "stop", version: 1 }),
      );
    } catch {
      // A failed worklet is already being removed from the graph.
    }
    captureSource.disconnect();
    captureNode.disconnect();
  }

  function emitLatency(bargeHaltMs = 0) {
    if (latencyDispatched) return;
    latencyDispatched = true;
    dispatchVoiceLatency({
      authReadyMs,
      bargeHaltMs,
      commitToFirstAudioMs,
      turnTotalMs: performance.now() - liveStartedAt,
      wsOpenMs,
    });
  }

  function closeSocket(code, reason) {
    if (
      socket.readyState === WebSocket.CONNECTING ||
      socket.readyState === WebSocket.OPEN
    ) {
      try {
        socket.close(code, reason);
      } catch {
        // The close event will settle a socket that raced shutdown.
      }
    }
  }

  function settleReady(error) {
    if (readySettled) return;
    readySettled = true;
    if (authReadyTimer !== undefined) {
      clearTimeout(authReadyTimer);
      authReadyTimer = undefined;
    }
    if (error) {
      rejectReady(error);
    } else {
      resolveReady();
    }
  }

  function settleResult(error, result) {
    if (resultSettled) return;
    resultSettled = true;
    if (error) {
      rejectResult(error);
    } else {
      resolveResult(result);
    }
  }

  function failLive(error) {
    if (state === "failed" || state === "cancelled") return;
    state = "failed";
    clientTransport.close();
    stopCapture();
    if (session?.playback) {
      haltStreamingPlayback(session.playback, error);
    }
    settleReady(error);
    settleResult(error);
    closeSocket(4002, "voice_live_failed");
  }

  function acceptWorkletMessage(event) {
    if (
      expectedEpoch !== sessionEpoch ||
      state === "failed" ||
      state === "cancelled" ||
      state === "committed" ||
      state === "final"
    ) {
      return;
    }
    try {
      const frame = safeLiveCaptureFrame(event.data);
      clientTransport.pushFrame(frame);
    } catch (error) {
      failLive(
        error instanceof Error
          ? error
          : new Error("voice_api_unavailable"),
      );
    }
  }

  let session;
  function acceptSocketMessage(event) {
    if (
      expectedEpoch !== sessionEpoch ||
      state === "failed" ||
      state === "cancelled"
    ) {
      return;
    }
    try {
      if (typeof event.data === "string") {
        const message = protocol.acceptText(event.data);
        if (message.type === "error") {
          failLive(new Error(message.code));
          return;
        }
        if (message.type === "ready") {
          if (state !== "awaiting-ready") {
            fail("voice_response_invalid");
          }
          state = "ready";
          authReadyMs = performance.now() - liveStartedAt;
          clientTransport.markReady();
          settleReady();
          return;
        }
        if (message.type === "final") {
          if (state !== "committed" || !session.playback) {
            fail("voice_response_invalid");
          }
          state = "final";
          finalResult = message.result;
          session.playback.finalReceived = true;
          session.playback.seal();
          settleResult(undefined, finalResult);
          closeSocket(1000, "complete");
          return;
        }
        fail("voice_response_invalid");
      }
      if (!(event.data instanceof ArrayBuffer)) {
        fail("voice_response_invalid");
      }
      if (state !== "committed" || !session.playback) {
        fail("voice_response_invalid");
      }
      if (commitToFirstAudioMs === 0 && commitAt !== undefined) {
        commitToFirstAudioMs = performance.now() - commitAt;
      }
      session.playback.schedulePcm(protocol.acceptBinary(event.data));
    } catch (error) {
      failLive(
        error instanceof Error
          ? error
          : new Error("voice_response_invalid"),
      );
    }
  }

  session = {
    playback: undefined,
    canFallback() {
      return !commitSent;
    },
    matches(expectedSessionState, expectedTurnMode) {
      return (
        expectedSessionState === sessionState &&
        expectedTurnMode === turnMode
      );
    },
    cancel(error = new Error("request_cancelled")) {
      if (state === "cancelled") return;
      state = "cancelled";
      clientTransport.close();
      stopCapture();
      settleReady(error);
      settleResult(error);
      closeSocket(4001, "cancelled");
    },
    async commit(playback) {
      stopCapture();
      if (state !== "ready") {
        await readyPromise;
      }
      if (state !== "ready" || expectedEpoch !== sessionEpoch) {
        throw new Error(
          expectedEpoch === sessionEpoch
            ? "voice_api_unavailable"
            : "request_cancelled",
        );
      }
      session.playback = playback;
      protocol.markCommitted();
      clientTransport.commit();
      state = "committed";
      commitSent = true;
      commitAt = performance.now();

      const result = await resultPromise;
      try {
        await playback.completion;
      } catch (error) {
        if (!playback.interrupted) throw error;
      }
      const snapshot = protocol.snapshot();
      emitLatency();
      return Object.freeze({
        ...result,
        interrupted: playback.interrupted,
        streamedAudio: snapshot.audioEventCount > 0,
      });
    },
    interrupt(error = new Error("voice_interrupted")) {
      clientTransport.close();
      stopCapture();
      if (state !== "final") {
        settleReady(error);
        settleResult(error);
      }
      closeSocket(4000, "voice_interrupted");
    },
    recordBargeIn(bargeHaltMs) {
      emitLatency(bargeHaltMs);
    },
    state() {
      return state;
    },
  };

  captureNode.port.onmessage = acceptWorkletMessage;
  captureNode.addEventListener(
    "processorerror",
    () => failLive(new Error("voice_api_unavailable")),
    { once: true },
  );
  socket.addEventListener("message", acceptSocketMessage);
  const acceptSocketOpen = () => {
    socketOpenedAt ??= performance.now();
    if (
      expectedEpoch !== sessionEpoch ||
      state !== "connecting"
    ) {
      closeSocket(4001, "stale");
      return;
    }
    try {
      clientTransport.open();
      wsOpenMs =
        (socketOpenedAt ?? performance.now()) - liveStartedAt;
      state = "awaiting-ready";
    } catch {
      failLive(new Error("voice_api_unavailable"));
    }
  };
  socket.addEventListener(
    "open",
    acceptSocketOpen,
    { once: true },
  );
  socket.addEventListener(
    "close",
    () => {
      if (state === "final") {
        state = "complete";
        return;
      }
      if (state !== "failed" && state !== "cancelled") {
        failLive(new Error("voice_api_unavailable"));
      }
    },
    { once: true },
  );
  socket.addEventListener(
    "error",
    () => failLive(new Error("voice_api_unavailable")),
    { once: true },
  );
  authReadyTimer = setTimeout(
    () => failLive(new Error("voice_api_unavailable")),
    Math.max(
      0,
      VOICE_LIVE_LIMITS.readyTimeoutMs -
        (performance.now() - liveStartedAt),
    ),
  );
  if (state === "ready") {
    settleReady();
  } else if (state === "connecting") {
    if (socket.readyState === WebSocket.OPEN) {
      socket.removeEventListener("open", acceptSocketOpen);
      acceptSocketOpen();
    } else if (socket.readyState !== WebSocket.CONNECTING) {
      failLive(new Error("voice_api_unavailable"));
      return undefined;
    }
  } else if (
    state !== "awaiting-ready" ||
    socket.readyState !== WebSocket.OPEN
  ) {
    failLive(new Error("voice_api_unavailable"));
    return undefined;
  }
  captureSource.connect(captureNode);
  return session;
}

function isNdjsonContentType(value) {
  return (
    typeof value === "string" &&
    /^application\/x-ndjson(?:\s*;\s*charset\s*=\s*"?utf-8"?)?$/iu.test(
      value.trim(),
    )
  );
}

function pcm16BytesAudioBuffer(encodedBuffer, decodedBytes, sampleRateHz) {
  if (!(encodedBuffer instanceof ArrayBuffer)) {
    fail("voice_response_invalid");
  }
  const encoded = new Uint8Array(encodedBuffer);
  if (
    encoded.byteLength !== decodedBytes ||
    encoded.byteLength === 0 ||
    encoded.byteLength % 2 !== 0 ||
    sampleRateHz !== 24_000 ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    fail("voice_response_invalid");
  }

  const samples = new Float32Array(encoded.byteLength / 2);
  const pcm = new DataView(
    encoded.buffer,
    encoded.byteOffset,
    encoded.byteLength,
  );
  for (let index = 0; index < samples.length; index += 1) {
    samples[index] = pcm.getInt16(index * 2, true) / 32_768;
  }
  const buffer = audioContext.createBuffer(
    1,
    samples.length,
    sampleRateHz,
  );
  buffer.getChannelData(0).set(samples);
  return buffer;
}

function pcm16AudioBuffer(audioBase64, decodedBytes, sampleRateHz) {
  return pcm16BytesAudioBuffer(
    base64ToArrayBuffer(audioBase64),
    decodedBytes,
    sampleRateHz,
  );
}

function abandonInterruptRecording(recording) {
  if (!recording || recording.settled) return;
  recording.settled = true;
  stopVad(recording);
  discardCurrentCandidate(recording, "interrupt-abandoned");
  recording.resolveEnd(
    Object.freeze({
      blob: new Blob([], { type: "audio/webm" }),
      hasSpeech: false,
      mimeType: "audio/webm",
      reason: "interrupt-abandoned",
    }),
  );
}

function stopBargeInMonitoring(playback) {
  if (!playback || playback.interrupted) return;
  restorePlaybackGain(playback);
  if (playback.interruptRecording) {
    abandonInterruptRecording(playback.interruptRecording);
    playback.interruptRecording = undefined;
  }
  playback.resetInterruptGuard = undefined;
  setTracksEnabled(false);
}

function rampPlaybackGain(playback, target, seconds) {
  if (
    !playback?.gainNode ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    return;
  }
  const gain = playback.gainNode.gain;
  const now = audioContext.currentTime;
  gain.cancelScheduledValues(now);
  gain.setValueAtTime(gain.value, now);
  gain.linearRampToValueAtTime(target, now + seconds);
}

function softDuckPlayback(playback) {
  rampPlaybackGain(playback, 0.1, 0.008);
}

function restorePlaybackGain(playback) {
  rampPlaybackGain(playback, 1, 0.02);
}

function hasVerifiedEchoCancellation(stream) {
  const tracks = stream?.getAudioTracks?.() ?? [];
  if (tracks.length !== 1 || typeof tracks[0].getSettings !== "function") {
    return false;
  }
  try {
    return tracks[0].getSettings().echoCancellation === true;
  } catch {
    return false;
  }
}

function confirmBargeIn(playback, recording, candidate) {
  if (
    playback.interrupted ||
    playback !== activePlayback ||
    recording.settled ||
    !candidate ||
    !candidateEventIsCurrent(recording, candidate)
  ) {
    return;
  }
  candidate.confirmed = true;
  playback.interruptedBeforeFinal =
    shouldAbortVoiceTransportOnInterrupt(playback.finalReceived);
  playback.interrupted = true;
  activeRecording = recording;
  sessionClock.markSpeech();

  const interruption = new Error("voice_interrupted");
  const interruptionStartedAt = Number.isFinite(
    recording.interruptOnsetAt,
  )
    ? recording.interruptOnsetAt
    : performance.now();
  const interruptedLiveSession = activeLiveSession;
  interruptedLiveSession?.interrupt(interruption);
  // Once a final frame has been parsed, keep reading to a clean EOF so
  // trailing bytes cannot be hidden by the interruption. Before final, there
  // is no state that can be committed, so abort the transport immediately.
  if (!playback.finalReceived && activeRequestController) {
    activeRequestController.abort();
  }
  haltStreamingPlayback(playback, interruption);
  const bargeHaltMs = performance.now() - interruptionStartedAt;
  if (interruptedLiveSession) {
    interruptedLiveSession.recordBargeIn(bargeHaltMs);
  } else {
    dispatchVoiceLatency({ bargeHaltMs });
  }
  if (activeLiveSession === interruptedLiveSession) {
    activeLiveSession = undefined;
  }
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-interrupted", {
      detail: Object.freeze({
        finalReceived: playback.finalReceived,
        version: 1,
      }),
    }),
  );
}

function startBargeInMonitoring(playback, expectedEpoch) {
  if (
    playback.interruptRecording ||
    playback.interrupted ||
    expectedEpoch !== sessionEpoch ||
    !analyser ||
    !hasLiveAudioTrack(mediaStream) ||
    !hasVerifiedEchoCancellation(mediaStream)
  ) {
    return;
  }

  setTracksEnabled(true);
  const recording = createRecordingState(mediaStream);
  const pcm = new Float32Array(analyser.fftSize);
  let vadState = createInterruptVadState(performance.now());
  playback.interruptRecording = recording;
  playback.resetInterruptGuard = () => {
    if (
      !recording.settled &&
      !recording.candidate &&
      vadState.phase !== "confirmed"
    ) {
      vadState = createInterruptVadState(performance.now());
    }
  };

  recording.vadTimer = setInterval(() => {
    if (
      recording.settled ||
      expectedEpoch !== sessionEpoch ||
      !analyser
    ) {
      return;
    }
    analyser.getFloatTimeDomainData(pcm);
    let sumSquares = 0;
    let peak = 0;
    for (let index = 0; index < pcm.length; index += 1) {
      const magnitude = Math.abs(pcm[index]);
      sumSquares += magnitude * magnitude;
      if (magnitude > peak) peak = magnitude;
    }
    vadState = advanceInterruptVad(vadState, {
      now: performance.now(),
      outputActive: playback.sources.size > 0,
      peak,
      rms: Math.sqrt(sumSquares / pcm.length),
    });

    if (vadState.action === "start") {
      recording.interruptOnsetAt = vadState.candidateStartedAt;
      if (!startCandidateRecorder(recording, false)) return;
      softDuckPlayback(playback);
    } else if (vadState.action === "discard") {
      recording.interruptOnsetAt = undefined;
      restorePlaybackGain(playback);
      if (!discardCurrentCandidate(recording, "interrupt-rejected")) {
        abandonInterruptRecording(recording);
      }
    } else if (vadState.action === "confirm") {
      confirmBargeIn(playback, recording, recording.candidate);
    } else if (
      vadState.action === "end-of-turn" ||
      vadState.action === "duration-limit"
    ) {
      requestRecordingStop(recording, vadState.action);
    }
  }, INTERRUPT_VAD_LIMITS.intervalMs);
}

function createStreamingPlayback(expectedEpoch) {
  if (
    activePlayback ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    fail("audio_playback_blocked");
  }

  let nextStartAt = 0;
  let pendingSources = 0;
  let rejectCompletion;
  let resolveCompletion;
  let sealed = false;
  let settled = false;
  let streamedAudio = false;
  const sources = new Set();
  const gainNode = audioContext.createGain();
  gainNode.gain.setValueAtTime(1, audioContext.currentTime);
  gainNode.connect(audioContext.destination);
  const completion = new Promise((resolve, reject) => {
    resolveCompletion = resolve;
    rejectCompletion = reject;
  });
  // stopSession can reject playback before finishTurn reaches its await.
  void completion.catch(() => {});

  let playback;
  function scheduleBuffer(buffer, event) {
    if (
      settled ||
      sealed ||
      expectedEpoch !== sessionEpoch ||
      !audioContext ||
      audioContext.state === "closed"
    ) {
      fail("request_cancelled");
    }
    const source = audioContext.createBufferSource();
    source.buffer = buffer;
    source.connect(gainNode);

    const startAt = Math.max(
      nextStartAt,
      audioContext.currentTime + 0.015,
    );
    pendingSources += 1;
    sources.add(source);
    source.addEventListener(
      "ended",
      () => {
        sources.delete(source);
        source.disconnect();
        pendingSources -= 1;
        if (sealed && pendingSources === 0 && !settled) {
          settled = true;
          stopBargeInMonitoring(playback);
          if (activePlayback === playback) {
            activePlayback = undefined;
          }
          gainNode.disconnect();
          resolveCompletion();
        }
      },
      { once: true },
    );
    try {
      source.start(startAt);
    } catch {
      sources.delete(source);
      pendingSources -= 1;
      source.disconnect();
      fail("audio_playback_blocked");
    }
    nextStartAt = startAt + buffer.duration;

    if (!streamedAudio) {
      streamedAudio = true;
      if (playback.interruptRecording) {
        playback.resetInterruptGuard?.();
      } else {
        startBargeInMonitoring(playback, expectedEpoch);
      }
      globalThis.dispatchEvent(
        new CustomEvent("kotae:first-audio", {
          detail: Object.freeze({ sequence: event.sequence, version: 1 }),
        }),
      );
    }
  }

  playback = {
    completion,
    finalReceived: false,
    gainNode,
    hasStreamedAudio: () => streamedAudio,
    interruptRecording: undefined,
    interrupted: false,
    interruptedBeforeFinal: false,
    reject(error) {
      if (settled) return;
      settled = true;
      rejectCompletion(error);
    },
    schedule(event) {
      const buffer = pcm16AudioBuffer(
        event.audioBase64,
        event.decodedBytes,
        event.sampleRateHz,
      );
      scheduleBuffer(buffer, event);
    },
    schedulePcm(event) {
      const buffer = pcm16BytesAudioBuffer(
        event.pcm,
        event.pcm.byteLength,
        event.sampleRateHz,
      );
      scheduleBuffer(buffer, event);
    },
    seal() {
      if (settled || sealed) {
        fail("voice_response_invalid");
      }
      sealed = true;
      if (pendingSources === 0) {
        settled = true;
        stopBargeInMonitoring(playback);
        if (activePlayback === playback) {
          activePlayback = undefined;
        }
        gainNode.disconnect();
        resolveCompletion();
      }
    },
    sources,
  };
  activePlayback = playback;
  return playback;
}

function haltStreamingPlayback(playback, error) {
  if (!playback) return;
  if (activePlayback === playback) {
    activePlayback = undefined;
  }
  // Settle the owner before stopping sources: stopping dispatches "ended"
  // synchronously in some browser engines.
  playback.reject(error);
  stopBargeInMonitoring(playback);
  for (const source of playback.sources) {
    try {
      source.stop();
    } catch {
      // A source can have ended between validation and cancellation.
    }
    source.disconnect();
  }
  playback.sources.clear();
  playback.gainNode?.disconnect();
}

async function consumeVoiceStream(response, playback, expectedEpoch) {
  if (
    !isNdjsonContentType(response.headers.get("Content-Type")) ||
    !response.body ||
    typeof response.body.getReader !== "function"
  ) {
    fail("voice_response_invalid");
  }
  const contentLength = response.headers.get("Content-Length");
  if (
    contentLength !== null &&
    (!/^[0-9]+$/u.test(contentLength) ||
      Number(contentLength) > VOICE_STREAM_LIMITS.maximumResponseBytes)
  ) {
    fail("voice_response_invalid");
  }

  const parser = createVoiceStreamParser((result) =>
    safeVoiceResponse(result),
  );
  const decoder = new TextDecoder("utf-8", {
    fatal: true,
    ignoreBOM: true,
  });
  const reader = response.body.getReader();
  let responseBytes = 0;

  function acceptEvents(events) {
    for (const event of events) {
      if (event.type === "audio") {
        playback.schedule(event);
      } else if (event.type === "final") {
        // Latch at parse time rather than EOF. Barge-in after this point must
        // preserve the transport until parser.finish validates termination.
        playback.finalReceived = true;
      }
    }
  }

  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      if (
        !(value instanceof Uint8Array) ||
        value.byteLength === 0
      ) {
        fail("voice_response_invalid");
      }
      responseBytes += value.byteLength;
      if (responseBytes > VOICE_STREAM_LIMITS.maximumResponseBytes) {
        fail("voice_response_invalid");
      }
      if (expectedEpoch !== sessionEpoch) {
        fail("request_cancelled");
      }
      acceptEvents(parser.push(decoder.decode(value, { stream: true })));
    }
    acceptEvents(parser.push(decoder.decode()));
    const completed = parser.finish();
    acceptEvents(completed.events);
    if (expectedEpoch !== sessionEpoch) {
      fail("request_cancelled");
    }
    if (!playback.interrupted) {
      playback.seal();
      try {
        await playback.completion;
      } catch (error) {
        // A barge-in can race this await after terminal EOF. The stream is
        // already fully validated, so preserve its final state and report the
        // interruption with that result. Other cancellation still fails.
        if (!playback.interrupted) {
          throw error;
        }
      }
    }
    return Object.freeze({
      ...completed.finalResult,
      interrupted: playback.interrupted,
      streamedAudio: completed.audioEventCount > 0,
    });
  } catch (error) {
    void reader.cancel(error).catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

async function finishTurn(serializedSessionState, turnMode) {
  const recording = activeRecording;
  if (!recording || finishGate.isBusy()) {
    fail("voice_turn_invalid");
  }
  if (
    typeof serializedSessionState !== "string" ||
    serializedSessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode)
  ) {
    fail("voice_turn_invalid");
  }

  const finishToken = finishGate.acquire();
  if (finishToken === null) {
    fail("voice_turn_invalid");
  }
  const expectedEpoch = sessionEpoch;
  let audioBase64 = "";
  let documentForTurn;
  let liveSession = activeLiveSession;
  let playback;
  let requestController;
  try {
    if (!recording.stopLatch.isRequested()) {
      requestRecordingStop(recording, "manual");
    }
    const capture = await recording.endPromise;
    if (!capture.hasSpeech) {
      fail("no_speech");
    }
    if (capture.blob.size > AUDIO_MAX_BYTES) {
      fail("voice_turn_too_large");
    }

    documentForTurn = pendingDocument;
    if (documentForTurn) {
      clearPendingDocument("consumed");
    }
    if (
      liveSession &&
      !liveSession.matches(serializedSessionState, turnMode)
    ) {
      liveSession.cancel(new Error("voice_turn_invalid"));
      fail("voice_turn_invalid");
    }
    if (liveSession && documentForTurn) {
      liveSession.cancel(new Error("voice_live_pdf_fallback"));
      if (activeLiveSession === liveSession) {
        activeLiveSession = undefined;
      }
      liveSession = undefined;
    }
    if (liveSession) {
      setTracksEnabled(false);
      if (!audioContext || audioContext.state === "closed") {
        fail("audio_playback_blocked");
      }
      if (audioContext.state === "suspended") {
        await audioContext.resume();
      }
      if (expectedEpoch !== sessionEpoch) {
        fail("request_cancelled");
      }
      playback = createStreamingPlayback(expectedEpoch);
      startBargeInMonitoring(playback, expectedEpoch);
      try {
        return await liveSession.commit(playback);
      } catch (error) {
        if (
          !liveSession.canFallback() ||
          playback.interrupted ||
          (error instanceof Error &&
            error.message === "request_cancelled")
        ) {
          throw error;
        }
        liveSession.cancel(
          error instanceof Error
            ? error
            : new Error("voice_api_unavailable"),
        );
        haltStreamingPlayback(
          playback,
          error instanceof Error
            ? error
            : new Error("voice_api_unavailable"),
        );
        playback = undefined;
        if (activeLiveSession === liveSession) {
          activeLiveSession = undefined;
        }
        liveSession = undefined;
      }
    }
    const [audioBuffer, credentials] = await Promise.all([
      capture.blob.arrayBuffer(),
      secureCredentials(),
    ]);
    audioBase64 = arrayBufferToBase64(audioBuffer);
    const { appCheckToken, idToken } = credentials;
    if (expectedEpoch !== sessionEpoch) {
      fail("request_cancelled");
    }
    setTracksEnabled(false);
    if (!audioContext || audioContext.state === "closed") {
      fail("audio_playback_blocked");
    }
    if (audioContext.state === "suspended") {
      await audioContext.resume();
    }
    if (expectedEpoch !== sessionEpoch) {
      fail("request_cancelled");
    }
    playback = createStreamingPlayback(expectedEpoch);
    // Begin guarded cancellation while the model is thinking. The guard is
    // restarted when the first PCM frame begins unless a user-voice
    // candidate is already retaining its leading phoneme.
    startBargeInMonitoring(playback, expectedEpoch);

    const payload = {
      audioBase64,
      mimeType: capture.mimeType,
      sessionState: serializedSessionState,
      turnMode,
    };
    if (documentForTurn) {
      payload.document = {
        base64: documentForTurn.base64,
        mimeType: documentForTurn.mimeType,
      };
    }

    requestController = new AbortController();
    activeRequestController = requestController;
    const response = await fetch(VOICE_ENDPOINT, {
      method: "POST",
      cache: "no-store",
      credentials: "omit",
      mode: "cors",
      redirect: "error",
      referrerPolicy: "no-referrer",
      signal: requestController.signal,
      headers: {
        Authorization: `Bearer ${idToken}`,
        "Content-Type": "application/json",
        "X-Firebase-AppCheck": appCheckToken,
      },
      body: JSON.stringify(payload),
    });
    if (!response.ok) {
      fail(mapVoiceResponseError(response.status));
    }
    return await consumeVoiceStream(response, playback, expectedEpoch);
  } catch (error) {
    liveSession?.cancel(
      error instanceof Error ? error : new Error("voice_api_unavailable"),
    );
    haltStreamingPlayback(
      playback,
      error instanceof Error ? error : new Error("voice_response_invalid"),
    );
    if (playback?.interruptedBeforeFinal) {
      fail("voice_interrupted");
    }
    if (error && typeof error === "object" && error.name === "AbortError") {
      fail("request_cancelled");
    }
    throw error;
  } finally {
    finishGate.release(finishToken);
    audioBase64 = "";
    if (activeRequestController === requestController) {
      activeRequestController = undefined;
    }
    if (activeLiveSession === liveSession) {
      activeLiveSession = undefined;
    }
    if (activeRecording === recording) {
      activeRecording = undefined;
    }
  }
}

function safeDocumentName(name) {
  const cleaned = name
    .normalize("NFC")
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .slice(0, 180)
    .trim();
  return cleaned || "paper.pdf";
}

async function attachDocument(inputId) {
  if (typeof inputId !== "string" || inputId !== "paper-input") {
    fail("document_not_selected");
  }
  const input = document.getElementById(inputId);
  if (!(input instanceof HTMLInputElement) || input.files?.length !== 1) {
    fail("document_not_selected");
  }
  const file = input.files[0];
  if (
    file.type !== "application/pdf" ||
    !file.name.toLowerCase().endsWith(".pdf")
  ) {
    input.value = "";
    fail("document_type_invalid");
  }
  if (file.size === 0 || file.size > DOCUMENT_MAX_BYTES) {
    input.value = "";
    fail("document_too_large");
  }

  documentEpoch += 1;
  const expectedEpoch = documentEpoch;
  let base64;
  try {
    base64 = arrayBufferToBase64(await file.arrayBuffer());
  } catch {
    input.value = "";
    fail("document_read_failed");
  }
  if (expectedEpoch !== documentEpoch) {
    input.value = "";
    fail("request_cancelled");
  }

  const name = safeDocumentName(file.name);
  clearPendingDocument("replaced");
  const attachedAt = performance.now();
  pendingDocument = Object.freeze({
    base64,
    mimeType: "application/pdf",
    name,
  });
  if (activeLiveSession?.canFallback()) {
    activeLiveSession.cancel(new Error("voice_live_pdf_fallback"));
    activeLiveSession = undefined;
  }
  armPendingDocumentExpiry(pendingDocument, attachedAt);
  base64 = "";
  return Object.freeze({
    name,
    sizeBytes: file.size,
  });
}

function stopSession() {
  sessionEpoch += 1;
  documentEpoch += 1;
  finishGate.reset();

  if (activeRequestController) {
    activeRequestController.abort();
    activeRequestController = undefined;
  }
  if (activeLiveSession) {
    const liveSession = activeLiveSession;
    activeLiveSession = undefined;
    liveSession.cancel(new Error("request_cancelled"));
  }
  if (activePlayback) {
    const playback = activePlayback;
    activePlayback = undefined;
    playback.reject(new Error("request_cancelled"));
    stopBargeInMonitoring(playback);
    for (const source of playback.sources) {
      try {
        source.stop();
      } catch {
        // A source may already have ended.
      }
      source.disconnect();
    }
    playback.sources.clear();
    playback.gainNode?.disconnect();
  }
  releaseMicrophone();
  sessionClock.reset();

  clearPendingDocument("session-stopped");
}

function hasActiveVoiceSession() {
  return Boolean(
    sessionClock.isStarted() ||
    activeRecording ||
    beginGate.isBusy() ||
    activeRequestController ||
    activeLiveSession ||
    activePlayback ||
    finishGate.isBusy() ||
    pendingDocument ||
    hasLiveAudioTrack(mediaStream),
  );
}

document.addEventListener("visibilitychange", () => {
  if (
    shouldStopSessionForLifecycle(
      "visibilitychange",
      document.hidden,
      hasActiveVoiceSession(),
    )
  ) {
    stopSession();
  }
});
globalThis.addEventListener("pagehide", () => {
  if (
    shouldStopSessionForLifecycle(
      "pagehide",
      document.hidden,
      hasActiveVoiceSession(),
    )
  ) {
    stopSession();
  }
});

const publicBridge = Object.freeze({
  attachDocument,
  beginTurn,
  endTurn,
  finishTurn,
  getStatus,
  stopSession,
  waitForTurnEnd,
});

Object.defineProperty(globalThis, "kotaeCloud", {
  configurable: false,
  enumerable: false,
  value: publicBridge,
  writable: false,
});
