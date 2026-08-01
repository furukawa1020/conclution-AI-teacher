import { getApp, getApps, initializeApp } from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app.js";
import {
  browserPopupRedirectResolver,
  browserSessionPersistence,
  getIdToken,
  GoogleAuthProvider,
  initializeAuth,
  signInWithPopup,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-auth.js";
import {
  getToken as getAppCheckToken,
  initializeAppCheck,
  ReCaptchaEnterpriseProvider,
} from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app-check.js";
import {
  advanceCandidateCapture,
  advanceVad,
  classifyVoiceSessionStopReason,
  createCandidateCaptureState,
  createCaptureBuffer,
  createRetryableInitializer,
  createSessionClock,
  createSessionExpiryWatchdog,
  createStopLatch,
  createTurnGate,
  createVadState,
  initializeWithCleanup,
  isValidTurnMode,
  normalizeResearchDiscovery,
  shouldCommitHybridEndpoint,
  shouldStopSessionForLifecycle,
  VOICE_SESSION_LIMITS,
} from "./voice-session-policy.mjs";
import {
  advanceInterruptVad,
  BARGE_PCM_LIMITS,
  claimAmbientLiveHandoff,
  CONFIRMED_SPEECH_PCM_LIMITS,
  createInterruptVadState,
  createVoiceLiveClientTransport,
  createVoiceLiveServerProtocol,
  createVoiceStreamParser,
  estimateAudiblePerformanceTime,
  INTERRUPT_VAD_LIMITS,
  isCleanVoiceLiveTerminalClose,
  safeLiveCaptureFrame,
  safeLiveCaptureSignal,
  shouldStartAmbientLiveHandoff,
  validatedPlaybackDrainTimeoutMs,
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
const VOICE_WARMUP_ENDPOINT = `${VOICE_ORIGIN}/health`;

const AUDIO_MAX_BYTES = 2 * 1024 * 1024;
const RESPONSE_AUDIO_MAX_BASE64_CHARS = 4 * Math.ceil(AUDIO_MAX_BYTES / 3);
const SESSION_STATE_MAX_CHARS = 16 * 1024;
const VAD_INTERVAL_MS = VOICE_SESSION_LIMITS.vadIntervalMs;
const VOICE_TURN_CLIENT_TIMEOUT_MS = 60_000;

const ALLOWED_CONFIG_KEYS = Object.freeze([
  "apiKey",
  "appId",
  "authDomain",
  "messagingSenderId",
  "projectId",
]);

let authInstance;
let mediaStream;
let mediaStreamLossBinding;
let audioContext;
let analyser;
let analyserSource;
let analyserStream;
let activeRecording;
let activeRequestController;
let activePlayback;
let activeLiveSession;
let pendingLiveSession;
let voiceTransportPrimed = false;
let sessionEpoch = 0;
let pcmCaptureGeneration = 0;
const MAX_STOPPED_SESSION_CODES = 8;
const stoppedSessionCodes = new Map();
const beginGate = createTurnGate();
const finishGate = createTurnGate();
const sessionClock = createSessionClock({
  now: () => performance.now(),
});
const sessionExpiryWatchdog = createSessionExpiryWatchdog({
  check: () => sessionClock.check(),
  clearTimer: (timer) => clearTimeout(timer),
  expire: (reason) => stopSession(reason),
  millisecondsUntilExpiry: () => sessionClock.millisecondsUntilExpiry(),
  setTimer: (callback, delay) => setTimeout(callback, delay),
});
const pcmCaptureWorkletLoads = new WeakMap();

function nextPcmCaptureGeneration() {
  pcmCaptureGeneration =
    pcmCaptureGeneration >= Number.MAX_SAFE_INTEGER
      ? 1
      : pcmCaptureGeneration + 1;
  return pcmCaptureGeneration;
}

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
  commitToEstimatedAudibleMs = 0,
  commitToFirstAudioMs = 0,
  firstBinaryMs = 0,
  speechEndToEstimatedAudibleMs = 0,
  turnTotalMs = 0,
  wsOpenMs = 0,
}) {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-latency", {
      detail: Object.freeze({
        auth_ready_ms: boundedLatency(authReadyMs),
        barge_halt_ms: boundedLatency(bargeHaltMs),
        commit_to_estimated_audible_ms: boundedLatency(
          commitToEstimatedAudibleMs,
        ),
        commit_to_first_audio_ms: boundedLatency(
          commitToFirstAudioMs,
        ),
        first_binary_ms: boundedLatency(firstBinaryMs),
        speech_end_to_estimated_audible_ms: boundedLatency(
          speechEndToEstimatedAudibleMs,
        ),
        turn_total_ms: boundedLatency(turnTotalMs),
        version: 2,
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

async function initializeFirebaseAuth() {
  const { app, appCheck } = await appServices();
  // Authentication has App Check enforcement enabled in production. Prime the
  // attestation token before Auth exchanges so a fresh browser never races the
  // identity provider with the reCAPTCHA Enterprise exchange.
  await getAppCheckToken(appCheck, false);
  authInstance ??= initializeAuth(app, {
    persistence: browserSessionPersistence,
    popupRedirectResolver: browserPopupRedirectResolver,
  });
  const auth = authInstance;
  await auth.authStateReady();
  return Object.freeze({ auth });
}

const firebaseAuth = createRetryableInitializer(initializeFirebaseAuth);

function verifiedAccountUser(user) {
  return Boolean(
    user &&
      !user.isAnonymous &&
      user.emailVerified === true &&
      Array.isArray(user.providerData) &&
      user.providerData.some((profile) => profile?.providerId === "google.com"),
  );
}

async function accountUser(interactive) {
  const { auth } = await firebaseAuth();
  if (verifiedAccountUser(auth.currentUser)) {
    return auth.currentUser;
  }
  if (!interactive) {
    fail("identity_required");
  }

  const provider = new GoogleAuthProvider();
  provider.setCustomParameters({ prompt: "select_account" });
  let credential;
  try {
    credential = await signInWithPopup(auth, provider);
  } catch {
    fail("identity_required");
  }
  if (!verifiedAccountUser(credential?.user)) {
    fail("identity_verification_failed");
  }
  return credential.user;
}

async function secureCredentials(interactive = false) {
  try {
    const [{ appCheck }, user] = await Promise.all([
      appServices(),
      accountUser(interactive),
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
    if (error instanceof Error) {
      switch (error.message) {
        case "app_check_not_configured":
        case "identity_required":
        case "identity_verification_failed":
          throw error;
        default:
          break;
      }
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
  } catch (error) {
    if (error instanceof Error && error.message === "identity_required") {
      return Object.freeze({ state: "identity-required" });
    }
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

function detachMediaStreamLossListener() {
  const binding = mediaStreamLossBinding;
  mediaStreamLossBinding = undefined;
  if (!binding) return;
  binding.track.removeEventListener("ended", binding.onEnded);
}

function installMediaStreamLossListener(stream, expectedEpoch) {
  detachMediaStreamLossListener();
  const tracks = stream.getAudioTracks();
  if (tracks.length !== 1) {
    fail("microphone_unavailable");
  }
  const track = tracks[0];
  const onEnded = () => {
    if (
      mediaStream === stream &&
      expectedEpoch === sessionEpoch &&
      sessionClock.isStarted()
    ) {
      stopSession("microphone_lost");
    }
  };
  track.addEventListener("ended", onEnded, { once: true });
  mediaStreamLossBinding = Object.freeze({ onEnded, track });
}

function releaseMicrophone(code = "request_cancelled") {
  const recording = activeRecording;
  activeRecording = undefined;
  if (recording) {
    recording.discard = true;
    recording.totalBytes = 0;
    // Rejecting the owned recording is the single cancellation path: it
    // stops VAD, clears and detaches the current candidate recorder, and
    // settles any Rust task waiting on endPromise.
    rejectRecording(recording, code);
  }
  detachMediaStreamLossListener();
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

function stoppedSessionCode(expectedEpoch) {
  return stoppedSessionCodes.get(expectedEpoch) ?? "request_cancelled";
}

function rememberStoppedSession(expectedEpoch, code) {
  stoppedSessionCodes.set(expectedEpoch, code);
  while (stoppedSessionCodes.size > MAX_STOPPED_SESSION_CODES) {
    const oldestEpoch = stoppedSessionCodes.keys().next().value;
    stoppedSessionCodes.delete(oldestEpoch);
  }
}

function ensureActiveSession(expectedEpoch) {
  if (expectedEpoch !== sessionEpoch || !sessionClock.isStarted()) {
    return false;
  }
  const status = sessionClock.check();
  if (!status.ok) {
    stopSession(status.expiry);
    return false;
  }
  return expectedEpoch === sessionEpoch && sessionClock.isStarted();
}

function markSessionSpeech(expectedEpoch) {
  if (!ensureActiveSession(expectedEpoch)) {
    return false;
  }
  const status = sessionClock.markSpeech();
  if (!status.ok) {
    stopSession(status.expiry);
    return false;
  }
  if (!sessionExpiryWatchdog.arm()) {
    return false;
  }
  return ensureActiveSession(expectedEpoch);
}

function beginSessionResponse(expectedEpoch) {
  if (!ensureActiveSession(expectedEpoch)) {
    return false;
  }
  const status = sessionClock.beginResponse();
  if (!status.ok) {
    stopSession(status.expiry ?? "maximum");
    return false;
  }
  if (!sessionExpiryWatchdog.arm()) {
    return false;
  }
  return expectedEpoch === sessionEpoch && sessionClock.isStarted();
}

function completeSessionResponse(expectedEpoch) {
  if (expectedEpoch !== sessionEpoch || !sessionClock.isStarted()) {
    return false;
  }
  const status = sessionClock.completeResponse();
  if (!status.ok) {
    stopSession(status.expiry ?? "maximum");
    return false;
  }
  if (!sessionExpiryWatchdog.arm()) {
    return false;
  }
  return ensureActiveSession(expectedEpoch);
}

function cancelSessionResponse(expectedEpoch) {
  if (expectedEpoch !== sessionEpoch || !sessionClock.isStarted()) {
    return false;
  }
  const status = sessionClock.cancelResponse();
  if (!status.ok) {
    stopSession(status.expiry ?? "maximum");
    return false;
  }
  if (!sessionExpiryWatchdog.arm()) {
    return false;
  }
  return ensureActiveSession(expectedEpoch);
}

function hasLiveAudioTrack(stream) {
  return Boolean(
    stream &&
      stream
        .getAudioTracks()
        .some((track) => track.readyState === "live"),
  );
}

async function ensureMediaStream(expectedEpoch, allowAcquisition) {
  if (hasLiveAudioTrack(mediaStream)) {
    return mediaStream;
  }
  if (expectedEpoch !== sessionEpoch) {
    fail(stoppedSessionCode(expectedEpoch));
  }
  if (allowAcquisition !== true) {
    // Foreground continuation may reuse the microphone selected by an
    // explicit gesture, but it may never acquire a replacement device on its
    // own. Losing that track pauses the encrypted session until the user taps
    // Resume, which creates a new Intentional gesture epoch.
    stopSession("microphone_lost");
    fail("microphone_unavailable");
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
    fail(stoppedSessionCode(expectedEpoch));
  }
  if (!hasLiveAudioTrack(stream) || stream.getVideoTracks().length !== 0) {
    stopTracks(stream);
    fail("microphone_unavailable");
  }
  mediaStream = stream;
  installMediaStreamLossListener(stream, expectedEpoch);
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
    });
  } catch {
    return new AudioContextConstructor();
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
    fail(stoppedSessionCode(expectedEpoch));
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
  if (recording.vadPcm instanceof Float32Array) {
    recording.vadPcm.fill(0);
    recording.vadPcm = undefined;
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

function clearCandidateDeadline(candidate) {
  if (candidate.deadlineTimer !== undefined) {
    clearTimeout(candidate.deadlineTimer);
    candidate.deadlineTimer = undefined;
  }
}

function armCandidateDeadline(recording, candidate, captureLimitMs) {
  const maximumCaptureLimitMs = Math.max(
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
    INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs,
  );
  if (
    candidate.confirmed ||
    !Number.isFinite(candidate.startedAt) ||
    candidate.startedAt < 0 ||
    !Number.isFinite(captureLimitMs) ||
    captureLimitMs <= 0 ||
    captureLimitMs > maximumCaptureLimitMs
  ) {
    return candidate.confirmed;
  }
  if (
    Number.isFinite(candidate.captureLimitMs) &&
    captureLimitMs <= candidate.captureLimitMs
  ) {
    return true;
  }

  clearCandidateDeadline(candidate);
  candidate.captureLimitMs = captureLimitMs;
  const remainingMs = Math.max(
    0,
    candidate.startedAt + captureLimitMs - performance.now(),
  );
  // This is an independent best-effort wall-clock stop for the local
  // MediaRecorder. Browser tasks cannot execute while the main thread is
  // stalled. The AudioWorklet separately enforces a hard fixed-memory and
  // pre-confirm cloud-egress boundary for raw PCM; it cannot make the local
  // MediaRecorder callback deadline hard.
  candidate.deadlineTimer = setTimeout(() => {
    candidate.deadlineTimer = undefined;
    if (
      !candidateEventIsCurrent(recording, candidate) ||
      candidate.confirmed
    ) {
      return;
    }
    if (!discardCurrentCandidate(recording, "candidate-deadline")) {
      rejectRecording(recording, "voice_turn_invalid");
    }
  }, remainingMs);
  return true;
}

function stopDetachedCandidate(candidate) {
  clearCandidateDeadline(candidate);
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
    candidate === undefined || !recording.fallbackAudioComplete
      ? Object.freeze({ chunks: [], totalBytes: 0 })
      : candidate.captureBuffer.take();
  const mimeType =
    candidate?.recorder.mimeType ||
    captured.chunks[0]?.type ||
    "audio/webm";
  const confirmedSpeech =
    candidate !== undefined &&
    candidate.confirmed &&
    (captured.totalBytes > 0 || !recording.fallbackAudioComplete);
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
      fallbackAudioComplete: recording.fallbackAudioComplete,
      hasSpeech:
        confirmedSpeech &&
        (blob.size > 0 || !recording.fallbackAudioComplete),
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

function currentAudioContextFrame(context = audioContext) {
  if (
    !context ||
    context.state === "closed" ||
    !Number.isFinite(context.currentTime) ||
    context.currentTime < 0 ||
    !Number.isFinite(context.sampleRate) ||
    context.sampleRate <= 0
  ) {
    return null;
  }
  const frame = Math.floor(context.currentTime * context.sampleRate);
  return Number.isSafeInteger(frame) && frame >= 0 ? frame : null;
}

function recordingHasLivePrimary(recording) {
  return (
    (activeRecording === recording && activeLiveSession !== undefined) ||
    pendingLiveSession?.recording === recording
  );
}

function startCandidateRecorder(
  recording,
  confirmed,
  candidateContextFrame,
  candidateStartedAt,
  captureLimitMs,
) {
  if (
    recording.candidate ||
    recording.settled ||
    recording.discard ||
    recording.stopLatch.isRequested() ||
    (!confirmed &&
      (!Number.isFinite(candidateStartedAt) ||
        candidateStartedAt < 0 ||
        !Number.isFinite(captureLimitMs) ||
        captureLimitMs <= 0))
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
    contextFrame:
      Number.isSafeInteger(candidateContextFrame) &&
      candidateContextFrame >= 0
        ? candidateContextFrame
        : null,
    captureBuffer: createCaptureBuffer({ maximumBytes: AUDIO_MAX_BYTES }),
    captureLimitMs: undefined,
    confirmed,
    deadlineTimer: undefined,
    discarded: false,
    recorder,
    startedAt: confirmed ? null : candidateStartedAt,
    stopReason: "",
  };
  recording.fallbackAudioComplete = true;
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
    if (!recording.fallbackAudioComplete) {
      return;
    }
    const captureState = candidate.captureBuffer.append(event.data);
    recording.totalBytes = captureState.totalBytes;
    if (captureState.tooLarge) {
      if (recordingHasLivePrimary(recording)) {
        // The WebSocket PCM stream is the primary path. Once this bounded
        // MediaRecorder copy overflows it is no longer a complete utterance,
        // so erase and permanently disable only the HTTP fallback. Keeping
        // the recorder alive preserves VAD/end-of-turn while later chunks are
        // dropped instead of extending local audio retention.
        candidate.captureBuffer.clear();
        recording.fallbackAudioComplete = false;
        recording.totalBytes = 0;
        return;
      }
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
    if (
      !candidate.confirmed &&
      !armCandidateDeadline(recording, candidate, captureLimitMs)
    ) {
      rejectRecording(recording, "voice_turn_invalid");
      return false;
    }
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

function confirmLiveSpeech(
  recording,
  candidateStartedAt,
  candidateContextFrame,
) {
  if (!recording.sessionSpeechMarked) {
    if (!markSessionSpeech(recording.expectedEpoch)) {
      return false;
    }
    recording.sessionSpeechMarked = true;
  }
  recording.liveSpeechConfirmed = true;
  recording.liveSpeechStartedAt = candidateStartedAt;
  const liveSession = activeLiveSession;
  if (
    liveSession &&
    !liveSession.confirmSpeech(
      candidateStartedAt,
      candidateContextFrame,
    ) &&
    activeLiveSession === liveSession
  ) {
    activeLiveSession = undefined;
  }
  return true;
}

function maybeCommitHybridEndpoint(recording, now) {
  if (
    recording.stopLatch.isRequested() ||
    recording.discard ||
    recording.settled
  ) {
    return false;
  }
  if (
    !shouldCommitHybridEndpoint({
      firstVoiceAt: recording.firstVoiceAt,
      hasSpeech: recording.vadHasSpeech,
      lastVoiceAt: recording.lastVoiceAt,
      now,
      providerEndpointAt: recording.providerEndpointAt,
      softVoiceConfirmed: recording.softVoiceConfirmed,
    })
  ) {
    return false;
  }
  requestRecordingStop(recording, "hybrid-endpoint");
  return true;
}

function armVad(recording) {
  const pcm = new Float32Array(analyser.fftSize);
  recording.vadPcm = pcm;
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
    recording.firstVoiceAt = vadState.firstVoiceAt;
    recording.vadHasSpeech = vadState.hasSpeech;
    recording.softVoiceConfirmed = vadState.softVoiceConfirmed;
    if (Number.isFinite(vadState.lastVoiceAt)) {
      recording.lastVoiceAt = vadState.lastVoiceAt;
    }
    if (
      vadState.softVoiceCandidate &&
      recording.candidate &&
      !recording.candidate.confirmed &&
      !armCandidateDeadline(
        recording,
        recording.candidate,
        VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
      )
    ) {
      rejectRecording(recording, "voice_turn_invalid");
      return;
    }
    if (maybeCommitHybridEndpoint(recording, now)) {
      return;
    }
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
          currentAudioContextFrame(),
          candidateCapture.candidateStartedAt,
          candidateCapture.captureLimitMs,
        )
      ) {
        return;
      }
      if (candidateCapture.phase === "confirmed") {
        if (
          !confirmLiveSpeech(
            recording,
            candidateCapture.candidateStartedAt,
            recording.candidate?.contextFrame,
          )
        ) {
          return;
        }
      }
    }
    if (candidateCapture.action === "confirm") {
      const candidate = recording.candidate;
      if (!candidate || !candidateEventIsCurrent(recording, candidate)) {
        rejectRecording(recording, "voice_turn_invalid");
        return;
      }
      clearCandidateDeadline(candidate);
      candidate.confirmed = true;
      if (
        !confirmLiveSpeech(
          recording,
          candidateCapture.candidateStartedAt,
          candidate.contextFrame,
        )
      ) {
        return;
      }
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
    expectedEpoch: sessionEpoch,
    fallbackAudioComplete: true,
    firstVoiceAt: null,
    lastVoiceAt: null,
    liveSpeechConfirmed: false,
    liveSpeechStartedAt: null,
    providerEndpointAt: null,
    resolveEnd,
    rejectEnd,
    settled: false,
    sessionSpeechMarked: false,
    startedAt: performance.now(),
    stopLatch: createStopLatch(),
    stopReason: "",
    stream,
    softVoiceConfirmed: false,
    totalBytes: 0,
    vadHasSpeech: false,
    vadPcm: undefined,
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
    stopSession("hidden");
    fail("request_cancelled");
  }
  if (
    typeof serializedSessionState !== "string" ||
    serializedSessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode) ||
    activeRecording ||
    activeLiveSession ||
    pendingLiveSession ||
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
      stopSession(sessionStatus.expiry);
      fail("session_expired");
    }
    primeVoiceTransportConnection();

    const expectedEpoch = sessionEpoch;
    if (!sessionExpiryWatchdog.arm() || !ensureActiveSession(expectedEpoch)) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    return await initializeWithCleanup(
      async () => {
        const credentials = await secureCredentials(true);
        if (expectedEpoch !== sessionEpoch) {
          fail(stoppedSessionCode(expectedEpoch));
        }
        const stream = await ensureMediaStream(
          expectedEpoch,
          turnMode === "intentional",
        );
        await ensureAudioGraph(stream, expectedEpoch);
        if (expectedEpoch !== sessionEpoch) {
          fail(stoppedSessionCode(expectedEpoch));
        }

        setStreamTracksEnabled(stream, true);
        const liveSession = await startVoiceLiveSession({
          ...credentials,
          expectedEpoch,
          sessionState: serializedSessionState,
          stream,
          turnMode,
        });
        if (expectedEpoch !== sessionEpoch) {
          const stopCode = stoppedSessionCode(expectedEpoch);
          liveSession?.cancel(new Error(stopCode));
          fail(stopCode);
        }
        // Attach the privacy-gated PCM capture before arming VAD. A user may
        // start talking immediately after pressing the button; arming VAD
        // first could confirm speech while the AudioWorklet was still loading
        // and force the live turn to cancel after its first PCM frame.
        activeLiveSession = liveSession;
        const recording = createRecording(stream);
        activeRecording = recording;
        return Object.freeze({ state: "listening" });
      },
      () => {
        if (expectedEpoch === sessionEpoch) {
          activeLiveSession?.cancel(new Error("request_cancelled"));
          activeLiveSession = undefined;
          releaseMicrophone();
          sessionExpiryWatchdog.disarm();
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
    if (pendingLiveSession?.recording === recording) {
      retirePendingLiveSession(
        error instanceof Error ? error : new Error("request_cancelled"),
      );
    }
    stopVad(recording);
    recording.discard = true;
    requestRecordingStop(recording, "cancelled");
    if (activeRecording === recording) {
      activeRecording = undefined;
    }
    throw error;
  }
  if (activeRecording !== recording) {
    fail(stoppedSessionCode(recording.expectedEpoch));
  }
  if (!capture.hasSpeech) {
    activeLiveSession?.cancel(new Error("no_speech"));
    activeLiveSession = undefined;
    if (pendingLiveSession?.recording === recording) {
      retirePendingLiveSession(new Error("no_speech"));
    }
    activeRecording = undefined;
  } else {
    if (!markSessionSpeech(recording.expectedEpoch)) {
      fail("session_expired");
    }
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

function hasValidCoachMetadata(assistanceTarget, phase, action) {
  if (assistanceTarget === "assistant") {
    return phase === "none" && action === "none";
  }
  if (assistanceTarget !== "respondent") return false;
  return (
    (phase === "awaiting_answer" && action === "elicit") ||
    (phase === "awaiting_restatement" && action === "restate") ||
    (phase === "expanding" && action === "expand") ||
    (phase === "complete" && action === "complete") ||
    (phase === "blocked" &&
      (action === "retry" || action === "release"))
  );
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
    !hasValidCoachMetadata(
      payload.assistanceTarget,
      payload.coachPhase,
      payload.coachAction,
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
    coachPhase: payload.coachPhase,
    coachAction: payload.coachAction,
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
  captureHandoff,
  expectedEpoch,
  idToken,
  sessionState,
  stream,
  turnMode,
}) {
  if (
    !liveVoiceSupported(stream) ||
    !liveCredential(appCheckToken) ||
    !liveCredential(idToken) ||
    typeof sessionState !== "string" ||
    sessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode) ||
    (captureHandoff !== undefined &&
      (captureHandoff === null ||
         typeof captureHandoff !== "object" ||
         typeof captureHandoff.adopt !== "function" ||
         typeof captureHandoff.seal !== "function" ||
         typeof captureHandoff.stop !== "function"))
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
      failPreflight(new Error(stoppedSessionCode(expectedEpoch)));
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
  if (captureHandoff === undefined) {
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
    fail(stoppedSessionCode(expectedEpoch));
  }
  if (preflightError) {
    return undefined;
  }

  let captureNode;
  let captureSource;
  const captureGeneration =
    captureHandoff === undefined
      ? nextPcmCaptureGeneration()
      : undefined;
  if (captureHandoff === undefined) {
    try {
      captureNode = new AudioWorkletNode(
        audioContext,
        "kotae-pcm-capture",
        {
          channelCount: 1,
          channelCountMode: "explicit",
          numberOfInputs: 1,
          numberOfOutputs: 0,
          processorOptions: {
            generation: captureGeneration,
            maximumPreConfirmFrames:
              CONFIRMED_SPEECH_PCM_LIMITS.maximumFrames,
            maximumQueuedFrames:
              VOICE_LIVE_LIMITS.maximumQueuedInputFrames,
          },
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
  }

  let adoptedCapture;
  let captureStopped = false;
  let captureCutoffContextFrame;
  let captureExpectedSequence = 0;
  let captureSealReject;
  let captureSealResolve;
  let captureSealTimer;
  let captureSealing = false;
  let authReadyMs = preflightAuthReadyMs;
  let authReadyTimer;
  let commitAt;
  let commitToEstimatedAudibleMs;
  let commitToFirstAudioMs;
  let firstBinaryMs;
  let speechEndedAt;
  let speechEndToEstimatedAudibleMs;
  let speechConfirmed = captureHandoff !== undefined;
  let commitSent = false;
  let latencyDispatched = false;
  let terminalCloseTimer;
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

  function zeroizeCaptureFrame(frame) {
    if (frame instanceof ArrayBuffer && frame.byteLength > 0) {
      new Uint8Array(frame).fill(0);
    }
  }

  function discardCaptureMessage(event) {
    zeroizeCaptureFrame(event?.data?.pcm);
  }

  function settleCaptureSeal(error) {
    if (!captureSealResolve && !captureSealReject) return;
    if (captureSealTimer !== undefined) {
      clearTimeout(captureSealTimer);
      captureSealTimer = undefined;
    }
    const resolve = captureSealResolve;
    const reject = captureSealReject;
    captureSealResolve = undefined;
    captureSealReject = undefined;
    if (error) {
      reject?.(error);
    } else {
      resolve?.();
    }
  }

  function stopCapture(
    error = new Error("request_cancelled"),
  ) {
    if (captureStopped) return;
    captureStopped = true;
    settleCaptureSeal(error);
    if (adoptedCapture) {
      const ownedCapture = adoptedCapture;
      adoptedCapture = undefined;
      ownedCapture.stop();
      return;
    }
    if (!captureNode) return;
    captureNode.port.onmessage = discardCaptureMessage;
    try {
      captureNode.port.postMessage(
        Object.freeze({
          generation: captureGeneration,
          type: "stop",
          version: 1,
        }),
      );
    } catch {
      // A failed worklet is already being removed from the graph.
    }
    captureSource?.disconnect();
    captureNode.disconnect();
  }

  async function sealCapture() {
    if (captureStopped || !speechConfirmed) {
      throw new Error("voice_api_unavailable");
    }
    if (adoptedCapture) {
      const ownedCapture = adoptedCapture;
      await ownedCapture.seal();
      ownedCapture.stop();
      if (adoptedCapture === ownedCapture) {
        adoptedCapture = undefined;
      }
      captureStopped = true;
      return;
    }
    if (
      !captureNode ||
      !captureSource ||
      captureSealing ||
      !Number.isSafeInteger(captureGeneration)
    ) {
      throw new Error("voice_api_unavailable");
    }

    captureSealing = true;
    captureSource.disconnect();
    const sealed = new Promise((resolve, reject) => {
      captureSealResolve = resolve;
      captureSealReject = reject;
      captureSealTimer = setTimeout(
        () => reject(new Error("voice_api_unavailable")),
        VOICE_LIVE_LIMITS.workletSealTimeoutMs,
      );
    });
    void sealed.catch(() => {});
    try {
      captureNode.port.postMessage(
        Object.freeze({
          generation: captureGeneration,
          type: "seal",
          version: 1,
        }),
      );
      await sealed;
    } finally {
      settleCaptureSeal();
    }
    captureStopped = true;
    captureNode.port.onmessage = discardCaptureMessage;
    try {
      captureNode.port.postMessage(
        Object.freeze({
          generation: captureGeneration,
          type: "stop",
          version: 1,
        }),
      );
    } catch {
      // The sealed worklet may already have ended.
    }
    captureNode.disconnect();
  }

  function emitLatency(bargeHaltMs = 0) {
    if (latencyDispatched) return;
    latencyDispatched = true;
    dispatchVoiceLatency({
      authReadyMs,
      bargeHaltMs,
      commitToEstimatedAudibleMs,
      commitToFirstAudioMs,
      firstBinaryMs,
      speechEndToEstimatedAudibleMs,
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
    if (terminalCloseTimer !== undefined) {
      clearTimeout(terminalCloseTimer);
      terminalCloseTimer = undefined;
    }
    if (error) {
      rejectResult(error);
    } else {
      resolveResult(result);
    }
  }

  function failLive(error) {
    if (
      state === "failed" ||
      state === "cancelled" ||
      state === "complete"
    ) {
      return;
    }
    state = "failed";
    clientTransport.close();
    stopCapture(error);
    if (session?.playback) {
      haltStreamingPlayback(session.playback, error);
    }
    settleReady(error);
    settleResult(error);
    closeSocket(4002, "voice_live_failed");
  }

  function acceptCaptureFrame(frame) {
    if (
      expectedEpoch !== sessionEpoch ||
      state === "failed" ||
      state === "cancelled" ||
      state === "committed" ||
      state === "final" ||
      state === "complete"
    ) {
      zeroizeCaptureFrame(frame);
      return;
    }
    if (!speechConfirmed) {
      zeroizeCaptureFrame(frame);
      failLive(new Error("voice_api_unavailable"));
      return;
    }
    clientTransport.pushFrame(frame);
  }

  function acceptWorkletMessage(event) {
    try {
      if (event?.data?.type === "frame") {
        if (
          !speechConfirmed ||
          !Number.isSafeInteger(captureCutoffContextFrame)
        ) {
          zeroizeCaptureFrame(event?.data?.pcm);
          throw new Error("voice_live_frame_invalid");
        }
        const frame = safeLiveCaptureFrame(event.data, {
          cutoffContextFrame: captureCutoffContextFrame,
          generation: captureGeneration,
          sequence: captureExpectedSequence,
        });
        acceptCaptureFrame(frame);
        captureExpectedSequence += 1;
        captureNode.port.postMessage(
          Object.freeze({
            frames: 1,
            generation: captureGeneration,
            type: "credit",
            version: 1,
          }),
        );
        return;
      }
      const signal = safeLiveCaptureSignal(event?.data, {
        generation: captureGeneration,
        lastSequence: captureExpectedSequence - 1,
        sealing: captureSealing,
      });
      if (signal === "capture_overflow") {
        throw new Error("voice_api_unavailable");
      }
      settleCaptureSeal();
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
        if (message.type === "endpoint") {
          // Local VAD may send commit while the provider endpoint advisory is
          // already in flight in the opposite WebSocket direction.
          if (state === "committed") {
            return;
          }
          if (state !== "ready") {
            fail("voice_response_invalid");
          }
          const recording = activeRecording;
          if (
            recording &&
            recording.stream === stream &&
            expectedEpoch === sessionEpoch &&
            !recording.settled
          ) {
            const now = performance.now();
            recording.providerEndpointAt = now;
            maybeCommitHybridEndpoint(recording, now);
          }
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
          terminalCloseTimer = setTimeout(
            () =>
              failLive(new Error("voice_response_invalid")),
            VOICE_LIVE_LIMITS.terminalCloseTimeoutMs,
          );
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
      if (
        commitToFirstAudioMs === undefined &&
        commitAt !== undefined
      ) {
        const firstBinaryAt = performance.now();
        commitToFirstAudioMs = firstBinaryAt - commitAt;
        firstBinaryMs = commitToFirstAudioMs;
      }
      const audibleAt = session.playback.schedulePcm(
        protocol.acceptBinary(event.data),
      );
      if (
        commitToEstimatedAudibleMs === undefined &&
        commitAt !== undefined &&
        Number.isFinite(audibleAt)
      ) {
        commitToEstimatedAudibleMs = audibleAt - commitAt;
        if (Number.isFinite(speechEndedAt)) {
          speechEndToEstimatedAudibleMs =
            audibleAt - speechEndedAt;
        }
      }
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
    handoffAmbient({
      candidateStartedAt,
      captureHandoff: nextCapture,
      interruption,
      stream: nextStream,
    }) {
      if (
        !session.playback ||
        !shouldStartAmbientLiveHandoff({
          captureAvailable: Boolean(nextCapture),
          finalReceived: session.playback.finalReceived,
          liveState: state,
        }) ||
        expectedEpoch !== sessionEpoch ||
        nextStream !== stream ||
        !Number.isFinite(candidateStartedAt) ||
        candidateStartedAt < 0 ||
          !nextCapture ||
          typeof nextCapture.adopt !== "function" ||
          typeof nextCapture.seal !== "function" ||
          typeof nextCapture.stop !== "function"
      ) {
        return undefined;
      }
      session.interrupt(interruption);
      return startVoiceLiveSession({
        appCheckToken,
        captureHandoff: nextCapture,
        expectedEpoch,
        idToken,
        sessionState,
        stream: nextStream,
        turnMode: "foreground",
      });
    },
    matches(expectedSessionState, expectedTurnMode) {
      return (
        expectedSessionState === sessionState &&
        expectedTurnMode === turnMode
      );
    },
    cancel(error = new Error("request_cancelled")) {
      if (
        state === "cancelled" ||
        state === "failed" ||
        state === "complete"
      ) {
        return;
      }
      state = "cancelled";
      clientTransport.close();
      stopCapture(error);
      settleReady(error);
      settleResult(error);
      closeSocket(4001, "cancelled");
    },
    confirmSpeech(candidateStartedAt, candidateContextFrame) {
      if (
        !Number.isFinite(candidateStartedAt) ||
        candidateStartedAt < 0 ||
        !Number.isSafeInteger(candidateContextFrame) ||
        candidateContextFrame < 0 ||
        state === "failed" ||
        state === "cancelled" ||
        state === "committed" ||
        state === "final" ||
        state === "complete"
      ) {
        return false;
      }
      if (speechConfirmed) return true;
      if (
        !captureNode ||
        !audioContext ||
        audioContext.state === "closed" ||
        !Number.isFinite(audioContext.sampleRate) ||
        audioContext.sampleRate <= 0
      ) {
        failLive(new Error("voice_api_unavailable"));
        return false;
      }
      const leadInFrames =
        VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs /
        CONFIRMED_SPEECH_PCM_LIMITS.frameDurationMs;
      const samplesPerPcmFrame =
        audioContext.sampleRate /
        (1_000 / CONFIRMED_SPEECH_PCM_LIMITS.frameDurationMs);
      captureCutoffContextFrame = Math.max(
        0,
        Math.floor(
          candidateContextFrame -
            leadInFrames * samplesPerPcmFrame,
        ),
      );
      try {
        captureNode.port.postMessage(
          Object.freeze({
            candidateContextFrame,
            generation: captureGeneration,
            initialCredit:
              VOICE_LIVE_LIMITS.workletCreditWindowFrames,
            leadInFrames,
            type: "confirm",
            version: 1,
          }),
        );
      } catch (error) {
        failLive(
          error instanceof Error
            ? error
            : new Error("voice_api_unavailable"),
        );
        return false;
      }
      speechConfirmed = true;
      return true;
    },
    async commit(playback, lastVoiceAt) {
      if (state !== "ready") {
        await readyPromise;
      }
      if (state !== "ready" || expectedEpoch !== sessionEpoch) {
        throw new Error(
          expectedEpoch === sessionEpoch
            ? "voice_api_unavailable"
            : stoppedSessionCode(expectedEpoch),
        );
      }
      if (!speechConfirmed) {
        throw new Error("no_speech");
      }
      try {
        await sealCapture();
      } catch (error) {
        const failure =
          error instanceof Error
            ? error
            : new Error("voice_api_unavailable");
        failLive(failure);
        throw failure;
      }
      if (state !== "ready" || expectedEpoch !== sessionEpoch) {
        throw new Error(
          expectedEpoch === sessionEpoch
            ? "voice_api_unavailable"
            : stoppedSessionCode(expectedEpoch),
        );
      }
      speechEndedAt = Number.isFinite(lastVoiceAt)
        ? lastVoiceAt
        : undefined;
      session.playback = playback;
      protocol.markCommitted();
      clientTransport.commit();
      state = "committed";
      commitSent = true;
      commitAt = performance.now();

      const result = await resultPromise;
      const snapshot = protocol.snapshot();
      return Object.freeze({
        finalResult: result,
        streamedAudio: snapshot.audioEventCount > 0,
      });
    },
    interrupt(error = new Error("voice_interrupted")) {
      const finalized = state === "final" || state === "complete";
      if (!finalized) {
        state = "cancelled";
      }
      clientTransport.close();
      stopCapture();
      if (!finalized) {
        settleReady(error);
        settleResult(error);
        closeSocket(4000, "voice_interrupted");
      }
    },
    recordBargeIn(bargeHaltMs) {
      emitLatency(bargeHaltMs);
    },
    recordCompletion() {
      emitLatency();
    },
    state() {
      return state;
    },
  };

  if (captureNode) {
    captureNode.port.onmessage = acceptWorkletMessage;
    captureNode.addEventListener(
      "processorerror",
      () => failLive(new Error("voice_api_unavailable")),
      { once: true },
    );
  }
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
    (event) => {
      if (
        state === "final" &&
        isCleanVoiceLiveTerminalClose({
          code: event.code,
          reason: event.reason,
          wasClean: event.wasClean,
        })
      ) {
        state = "complete";
        settleResult(undefined, finalResult);
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
  if (captureHandoff) {
    try {
      captureHandoff.adopt({
        onError: failLive,
        onFrame: acceptCaptureFrame,
      });
      adoptedCapture = captureHandoff;
    } catch {
      captureHandoff.stop();
      failLive(new Error("voice_api_unavailable"));
      return undefined;
    }
  } else {
    captureSource.connect(captureNode);
  }
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
  if (!playback) return;
  if (playback.bargePcmMonitor) {
    const monitor = playback.bargePcmMonitor;
    playback.bargePcmMonitor = undefined;
    monitor.stop();
  }
  if (playback.interrupted) return;
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

async function startBargePcmMonitoring(
  playback,
  recording,
  expectedEpoch,
) {
  const context = audioContext;
  const stream = mediaStream;
  if (
    playback.bargePcmMonitor ||
    playback.interrupted ||
    playback !== activePlayback ||
    recording.settled ||
    playback.interruptRecording !== recording ||
    expectedEpoch !== sessionEpoch ||
    !context ||
    context.state === "closed" ||
    !stream ||
    !hasLiveAudioTrack(stream) ||
    !hasVerifiedEchoCancellation(stream) ||
    typeof globalThis.AudioWorkletNode !== "function" ||
    !context.audioWorklet ||
    typeof context.audioWorklet.addModule !== "function"
  ) {
    return;
  }

  try {
    await loadPcmCaptureWorklet(context);
  } catch {
    return;
  }
  if (
    playback.bargePcmMonitor ||
    playback.interrupted ||
    playback !== activePlayback ||
    recording.settled ||
    playback.interruptRecording !== recording ||
    expectedEpoch !== sessionEpoch ||
    audioContext !== context ||
    mediaStream !== stream ||
    context.state === "closed" ||
    !hasLiveAudioTrack(stream) ||
    !hasVerifiedEchoCancellation(stream)
  ) {
    return;
  }

  const generation = nextPcmCaptureGeneration();
  let adopted = false;
  let confirmed = false;
  let cutoffContextFrame;
  let errorSink;
  let expectedSequence = 0;
  let frameSink;
  let node;
  let sealReject;
  let sealResolve;
  let sealTimer;
  let sealing = false;
  let source;
  let stopped = false;
  try {
    node = new AudioWorkletNode(context, "kotae-pcm-capture", {
      channelCount: 1,
      channelCountMode: "explicit",
      numberOfInputs: 1,
      numberOfOutputs: 0,
      processorOptions: {
        generation,
        maximumPreConfirmFrames: BARGE_PCM_LIMITS.maximumFrames,
        maximumQueuedFrames:
          VOICE_LIVE_LIMITS.maximumQueuedInputFrames,
      },
    });
    source = context.createMediaStreamSource(stream);
  } catch {
    node?.disconnect();
    source?.disconnect();
    return;
  }

  let monitor;
  function zeroizeMessage(event) {
    if (
      event?.data?.pcm instanceof ArrayBuffer &&
      event.data.pcm.byteLength > 0
    ) {
      new Uint8Array(event.data.pcm).fill(0);
    }
  }
  function settleSeal(error) {
    if (!sealResolve && !sealReject) return;
    if (sealTimer !== undefined) {
      clearTimeout(sealTimer);
      sealTimer = undefined;
    }
    const resolve = sealResolve;
    const reject = sealReject;
    sealResolve = undefined;
    sealReject = undefined;
    if (error) {
      reject?.(error);
    } else {
      resolve?.();
    }
  }
  function stop(error = new Error("request_cancelled")) {
    if (stopped) return;
    stopped = true;
    settleSeal(error);
    if (playback.bargePcmMonitor === monitor) {
      playback.bargePcmMonitor = undefined;
    }
    node.port.onmessage = zeroizeMessage;
    try {
      node.port.postMessage(
        Object.freeze({ generation, type: "stop", version: 1 }),
      );
    } catch {
      // A failed worklet is already leaving the graph.
    }
    source.disconnect();
    node.disconnect();
    errorSink = undefined;
    frameSink = undefined;
  }
  function failMonitor(error) {
    const notify = adopted ? errorSink : undefined;
    stop(
      error instanceof Error
        ? error
        : new Error("voice_api_unavailable"),
    );
    notify?.(
      error instanceof Error
        ? error
        : new Error("voice_api_unavailable"),
    );
  }
  monitor = Object.freeze({
    adopt({ onError, onFrame }) {
      if (
        stopped ||
        adopted ||
        !confirmed ||
        !Number.isSafeInteger(cutoffContextFrame) ||
        expectedEpoch !== sessionEpoch ||
        audioContext !== context ||
        mediaStream !== stream ||
        context.state === "closed" ||
        !hasLiveAudioTrack(stream) ||
        !hasVerifiedEchoCancellation(stream) ||
        recording.settled ||
        playback.interruptRecording !== recording ||
        typeof onError !== "function" ||
        typeof onFrame !== "function"
      ) {
        throw new Error("voice_api_unavailable");
      }
      adopted = true;
      frameSink = onFrame;
      errorSink = onError;
      playback.bargePcmMonitor = undefined;
      node.port.postMessage(
        Object.freeze({
          frames: VOICE_LIVE_LIMITS.workletCreditWindowFrames,
          generation,
          type: "credit",
          version: 1,
        }),
      );
      return true;
    },
    confirm({ candidateContextFrame, leadInFrames }) {
      if (
        stopped ||
        confirmed ||
        playback.bargePcmMonitor !== monitor ||
        expectedEpoch !== sessionEpoch ||
        !Number.isSafeInteger(candidateContextFrame) ||
        candidateContextFrame < 0 ||
        !Number.isSafeInteger(leadInFrames) ||
        leadInFrames < 0
      ) {
        return false;
      }
      const samplesPerPcmFrame =
        context.sampleRate /
        (1_000 / BARGE_PCM_LIMITS.frameDurationMs);
      cutoffContextFrame = Math.max(
        0,
        Math.floor(
          candidateContextFrame -
            leadInFrames * samplesPerPcmFrame,
        ),
      );
      try {
        node.port.postMessage(
          Object.freeze({
            candidateContextFrame,
            generation,
            initialCredit: 0,
            leadInFrames,
            type: "confirm",
            version: 1,
          }),
        );
      } catch {
        stop(new Error("voice_api_unavailable"));
        return false;
      }
      confirmed = true;
      return true;
    },
    async seal() {
      if (stopped || !adopted || !confirmed || sealing) {
        throw new Error("voice_api_unavailable");
      }
      sealing = true;
      source.disconnect();
      const sealed = new Promise((resolve, reject) => {
        sealResolve = resolve;
        sealReject = reject;
        sealTimer = setTimeout(
          () => reject(new Error("voice_api_unavailable")),
          VOICE_LIVE_LIMITS.workletSealTimeoutMs,
        );
      });
      void sealed.catch(() => {});
      try {
        node.port.postMessage(
          Object.freeze({ generation, type: "seal", version: 1 }),
        );
        await sealed;
      } finally {
        settleSeal();
      }
    },
    snapshot() {
      return Object.freeze({
        adopted,
        confirmed,
        cutoffContextFrame:
          cutoffContextFrame ?? null,
        expectedSequence,
        sealing,
        stopped,
      });
    },
    stop,
  });
  node.port.onmessage = (event) => {
    try {
      if (event?.data?.type === "frame") {
        if (
          !confirmed ||
          !adopted ||
          !Number.isSafeInteger(cutoffContextFrame)
        ) {
          zeroizeMessage(event);
          throw new Error("voice_live_frame_invalid");
        }
        const frame = safeLiveCaptureFrame(event.data, {
          cutoffContextFrame,
          generation,
          sequence: expectedSequence,
        });
        frameSink(frame);
        expectedSequence += 1;
        node.port.postMessage(
          Object.freeze({
            frames: 1,
            generation,
            type: "credit",
            version: 1,
          }),
        );
        return;
      }
      const signal = safeLiveCaptureSignal(event?.data, {
        generation,
        lastSequence: expectedSequence - 1,
        sealing,
      });
      if (signal === "capture_overflow") {
        throw new Error("voice_api_unavailable");
      }
      settleSeal();
    } catch (error) {
      failMonitor(error);
    }
  };
  node.addEventListener(
    "processorerror",
    () => failMonitor(new Error("voice_api_unavailable")),
    { once: true },
  );

  playback.bargePcmMonitor = monitor;
  try {
    source.connect(node);
  } catch {
    stop();
  }
}

function retirePendingLiveSession(
  error = new Error("request_cancelled"),
  expectedPending = pendingLiveSession,
) {
  const pending = expectedPending;
  if (!pending) return;
  if (pendingLiveSession === pending) {
    pendingLiveSession = undefined;
  }
  if (pending.retired) return;
  pending.retired = true;
  pending.captureHandoff?.stop();
  void pending.promise
    .then((liveSession) => liveSession?.cancel(error))
    .catch(() => {});
}

async function takePendingLiveSession(recording, expectedEpoch) {
  const pending = pendingLiveSession;
  if (
    !pending ||
    pending.recording !== recording ||
    pending.expectedEpoch !== expectedEpoch
  ) {
    return undefined;
  }

  let timeout;
  const timedOut = Symbol("voice_live_handoff_timeout");
  let nextLiveSession;
  try {
    nextLiveSession = await Promise.race([
      pending.promise,
      new Promise((resolve) => {
        timeout = setTimeout(
          () => resolve(timedOut),
          VOICE_LIVE_LIMITS.handoffReadyTimeoutMs,
        );
      }),
    ]);
  } catch {
    nextLiveSession = undefined;
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }

  if (nextLiveSession === timedOut) {
    retirePendingLiveSession(
      new Error("voice_live_handoff_timeout"),
      pending,
    );
    return undefined;
  }
  if (pendingLiveSession !== pending) {
    if (
      nextLiveSession &&
      activeLiveSession === nextLiveSession &&
      expectedEpoch === sessionEpoch &&
      activeRecording === recording
    ) {
      return nextLiveSession;
    }
    return undefined;
  }
  pendingLiveSession = undefined;
  if (
    !nextLiveSession ||
    pending.retired ||
    expectedEpoch !== sessionEpoch ||
    activeRecording !== recording ||
    (activeLiveSession && activeLiveSession !== nextLiveSession)
  ) {
    nextLiveSession?.cancel(
      new Error(stoppedSessionCode(expectedEpoch)),
    );
    return undefined;
  }
  activeLiveSession = nextLiveSession;
  return nextLiveSession;
}

function confirmBargeIn(
  playback,
  recording,
  candidate,
  expectedEpoch,
) {
  if (
    playback.hasStreamedAudio?.() !== true ||
    playback.interrupted ||
    playback !== activePlayback ||
    recording.settled ||
    !candidate ||
    !candidateEventIsCurrent(recording, candidate)
  ) {
    return;
  }
  if (
    !markSessionSpeech(expectedEpoch) ||
    expectedEpoch !== sessionEpoch ||
    playback !== activePlayback ||
    playback.interrupted ||
    recording.settled ||
    !candidateEventIsCurrent(recording, candidate)
  ) {
    return;
  }
  let captureHandoff = playback.bargePcmMonitor;
  if (captureHandoff) {
    const leadInFrames =
      BARGE_PCM_LIMITS.leadInMs /
      BARGE_PCM_LIMITS.frameDurationMs;
    if (
      !captureHandoff.confirm({
        candidateContextFrame: candidate.contextFrame,
        leadInFrames,
      })
    ) {
      captureHandoff.stop();
      captureHandoff = undefined;
    } else if (playback.bargePcmMonitor === captureHandoff) {
      // The confirmed capture is now owned by the bounded handoff. Detaching
      // it keeps playback shutdown from discarding the user's interruption.
      playback.bargePcmMonitor = undefined;
    }
  }
  clearCandidateDeadline(candidate);
  candidate.confirmed = true;
  playback.interruptedBeforeFinal =
    shouldAbortVoiceTransportOnInterrupt(playback.finalReceived);
  playback.interrupted = true;
  activeRecording = recording;

  const interruption = new Error("voice_interrupted");
  const interruptionStartedAt = Number.isFinite(
    recording.interruptOnsetAt,
  )
    ? recording.interruptOnsetAt
    : performance.now();
  const interruptedLiveSession = activeLiveSession;
  const handoffEpoch = sessionEpoch;
  if (activeLiveSession === interruptedLiveSession) {
    activeLiveSession = undefined;
  }
  const handoffPromise =
    !playback.finalReceived &&
    interruptedLiveSession &&
    captureHandoff
      ? interruptedLiveSession.handoffAmbient({
          candidateStartedAt: interruptionStartedAt,
          captureHandoff,
          interruption,
          stream: mediaStream,
        })
      : undefined;
  if (!handoffPromise) {
    captureHandoff?.stop();
    interruptedLiveSession?.interrupt(interruption);
  }
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
  if (handoffPromise) {
    retirePendingLiveSession(new Error("voice_interrupted"));
    const pending = {
      expectedEpoch: handoffEpoch,
      captureHandoff,
      promise: Promise.resolve(handoffPromise),
      recording,
      retired: false,
    };
    pendingLiveSession = pending;
    void pending.promise
      .then((nextLiveSession) => {
        if (!nextLiveSession) {
          captureHandoff?.stop();
          if (pendingLiveSession === pending) {
            pendingLiveSession = undefined;
          }
          return;
        }
        if (pendingLiveSession !== pending || pending.retired) {
          nextLiveSession.cancel(new Error("request_cancelled"));
          return;
        }
        if (recording.settled) {
          nextLiveSession.cancel(new Error("request_cancelled"));
          return;
        }
        const claimed = claimAmbientLiveHandoff(
          nextLiveSession,
          {
            activeRecordingMatches: activeRecording === recording,
            activeSlotEmpty: activeLiveSession === undefined,
            currentEpoch: sessionEpoch,
            expectedEpoch: handoffEpoch,
            recordingSettled: recording.settled,
          },
        );
        if (claimed) {
          activeLiveSession = claimed;
          if (pendingLiveSession === pending) {
            pendingLiveSession = undefined;
          }
        }
      })
      .catch(() => {
        captureHandoff?.stop();
        if (pendingLiveSession === pending) {
          pendingLiveSession = undefined;
        }
        // The MediaRecorder candidate remains the ambient HTTP fallback.
      });
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
  const sessionStatus = sessionClock.check();
  if (!sessionClock.isStarted() || !sessionStatus.ok) {
    stopSession(sessionStatus.expiry ?? "maximum");
    fail("session_expired");
  }

  setTracksEnabled(true);
  const recording = createRecordingState(mediaStream);
  const pcm = new Float32Array(analyser.fftSize);
  recording.vadPcm = pcm;
  let vadState = createInterruptVadState(performance.now());
  playback.interruptRecording = recording;
  void startBargePcmMonitoring(playback, recording, expectedEpoch);
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
    recording.firstVoiceAt = vadState.firstVoiceAt;
    recording.vadHasSpeech = vadState.phase === "confirmed";
    if (Number.isFinite(vadState.lastVoiceAt)) {
      recording.lastVoiceAt = vadState.lastVoiceAt;
    }
    if (maybeCommitHybridEndpoint(recording, performance.now())) {
      return;
    }

    if (vadState.action === "start") {
      recording.interruptOnsetAt = vadState.candidateStartedAt;
      if (
        !startCandidateRecorder(
          recording,
          false,
          currentAudioContextFrame(),
          vadState.candidateStartedAt,
          INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs,
        )
      ) {
        return;
      }
      softDuckPlayback(playback);
    } else if (vadState.action === "discard") {
      recording.interruptOnsetAt = undefined;
      restorePlaybackGain(playback);
      if (!discardCurrentCandidate(recording, "interrupt-rejected")) {
        abandonInterruptRecording(recording);
      }
    } else if (vadState.action === "confirm") {
      confirmBargeIn(
        playback,
        recording,
        recording.candidate,
        expectedEpoch,
      );
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
  const playbackContext = audioContext;
  const sources = new Set();
  const gainNode = playbackContext.createGain();
  gainNode.gain.setValueAtTime(1, playbackContext.currentTime);
  gainNode.connect(playbackContext.destination);
  const completion = new Promise((resolve, reject) => {
    resolveCompletion = resolve;
    rejectCompletion = reject;
  });
  // stopSession can reject playback before finishTurn reaches its await.
  void completion.catch(() => {});

  let playback;
  function firstMeaningfulOffsetSeconds(buffer) {
    if (
      !buffer ||
      !Number.isFinite(buffer.sampleRate) ||
      buffer.sampleRate <= 0 ||
      buffer.numberOfChannels < 1
    ) {
      return undefined;
    }
    const samples = buffer.getChannelData(0);
    for (let index = 0; index < samples.length; index += 1) {
      if (Math.abs(samples[index]) >= 0.001) {
        return index / buffer.sampleRate;
      }
    }
    return undefined;
  }

  function scheduleBuffer(buffer, event) {
    if (
      settled ||
      sealed ||
      expectedEpoch !== sessionEpoch ||
      !audioContext ||
      audioContext.state === "closed"
    ) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    const source = audioContext.createBufferSource();
    source.buffer = buffer;
    source.connect(gainNode);

    const startAt = Math.max(
      nextStartAt,
      audioContext.currentTime + 0.015,
    );
    const meaningfulOffset = firstMeaningfulOffsetSeconds(buffer);
    let outputTimestamp;
    if (typeof audioContext.getOutputTimestamp === "function") {
      try {
        outputTimestamp = audioContext.getOutputTimestamp();
      } catch {
        // outputLatency remains the standards-based fallback below.
      }
    }
    const scheduledAudibleAt = estimateAudiblePerformanceTime({
      baseLatencySeconds: audioContext.baseLatency,
      currentContextTime: audioContext.currentTime,
      outputLatencySeconds: audioContext.outputLatency,
      outputTimestamp,
      performanceNow: performance.now(),
      targetContextTime: startAt,
    });
    const audibleAt = Number.isFinite(meaningfulOffset)
      ? scheduledAudibleAt + meaningfulOffset * 1_000
      : undefined;
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
    return audibleAt;
  }

  playback = {
    bargePcmMonitor: undefined,
    completion,
    drainTimeoutMs() {
      return validatedPlaybackDrainTimeoutMs({
        currentContextTime: playbackContext.currentTime,
        scheduledEndContextTime: nextStartAt,
      });
    },
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
      return scheduleBuffer(buffer, event);
    },
    schedulePcm(event) {
      const buffer = pcm16BytesAudioBuffer(
        event.pcm,
        event.pcm.byteLength,
        event.sampleRateHz,
      );
      return scheduleBuffer(buffer, event);
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

async function awaitValidatedPlaybackCompletion(
  playback,
  expectedEpoch,
) {
  if (
    !playback ||
    typeof playback.drainTimeoutMs !== "function" ||
    expectedEpoch !== sessionEpoch
  ) {
    fail(
      expectedEpoch === sessionEpoch
        ? "voice_response_invalid"
        : stoppedSessionCode(expectedEpoch),
    );
  }
  const drainTimeoutMs = playback.drainTimeoutMs();
  let timeout;
  const deadline = new Promise((_, reject) => {
    timeout = setTimeout(
      () => reject(new Error("audio_playback_blocked")),
      drainTimeoutMs,
    );
  });
  try {
    await Promise.race([playback.completion, deadline]);
  } catch (error) {
    if (!playback.interrupted) throw error;
  } finally {
    clearTimeout(timeout);
  }
  if (expectedEpoch !== sessionEpoch) {
    fail(stoppedSessionCode(expectedEpoch));
  }
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
        fail(stoppedSessionCode(expectedEpoch));
      }
      acceptEvents(parser.push(decoder.decode(value, { stream: true })));
    }
    acceptEvents(parser.push(decoder.decode()));
    const completed = parser.finish();
    acceptEvents(completed.events);
    if (expectedEpoch !== sessionEpoch) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    if (!playback.interrupted) {
      playback.seal();
    }
    return Object.freeze({
      finalResult: completed.finalResult,
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
  let liveSession;
  let playback;
  let requestController;
  let responseClockActive = false;
  let turnTimedOut = false;
  const turnDeadlineAt =
    performance.now() + VOICE_TURN_CLIENT_TIMEOUT_MS;
  async function awaitVoiceTurnResult(promise, cancel) {
    const remainingMs = Math.max(0, turnDeadlineAt - performance.now());
    let timeout;
    const deadline = new Promise((_, reject) => {
      timeout = setTimeout(() => {
        turnTimedOut = true;
        try {
          cancel?.();
        } finally {
          reject(new Error("voice_turn_timeout"));
        }
      }, Math.ceil(remainingMs));
    });
    try {
      return await Promise.race([promise, deadline]);
    } finally {
      clearTimeout(timeout);
    }
  }
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
    if (!beginSessionResponse(expectedEpoch)) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    responseClockActive = true;

    liveSession = activeLiveSession;
    if (!liveSession) {
      liveSession = await takePendingLiveSession(
        recording,
        expectedEpoch,
      );
    }
    if (
      liveSession &&
      !liveSession.matches(serializedSessionState, turnMode)
    ) {
      liveSession.cancel(new Error("voice_turn_invalid"));
      fail("voice_turn_invalid");
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
        fail(stoppedSessionCode(expectedEpoch));
      }
      playback = createStreamingPlayback(expectedEpoch);
      try {
        const completed = await awaitVoiceTurnResult(
          liveSession.commit(
            playback,
            recording.lastVoiceAt,
          ),
          () => liveSession.cancel(new Error("voice_turn_timeout")),
        );
        await awaitValidatedPlaybackCompletion(
          playback,
          expectedEpoch,
        );
        liveSession.recordCompletion();
        if (!completeSessionResponse(expectedEpoch)) {
          fail(stoppedSessionCode(expectedEpoch));
        }
        responseClockActive = false;
        return Object.freeze({
          ...completed.finalResult,
          interrupted: playback.interrupted,
          streamedAudio: completed.streamedAudio,
        });
      } catch (error) {
        if (
          turnTimedOut ||
          !liveSession.canFallback() ||
          playback.interrupted ||
          expectedEpoch !== sessionEpoch ||
          (error instanceof Error &&
            (error.message === "request_cancelled" ||
              error.message === "session_expired"))
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
    if (capture.fallbackAudioComplete !== true) {
      // Never upload an empty suffix or prefix after the bounded recorder
      // fallback was invalidated. The live primary already had its chance to
      // finish above; a failed primary now ends explicitly.
      fail("voice_turn_too_large");
    }
    const [audioBuffer, credentials] = await awaitVoiceTurnResult(
      Promise.all([
        capture.blob.arrayBuffer(),
        secureCredentials(),
      ]),
    );
    audioBase64 = arrayBufferToBase64(audioBuffer);
    const { appCheckToken, idToken } = credentials;
    if (expectedEpoch !== sessionEpoch) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    setTracksEnabled(false);
    if (!audioContext || audioContext.state === "closed") {
      fail("audio_playback_blocked");
    }
    if (audioContext.state === "suspended") {
      await audioContext.resume();
    }
    if (expectedEpoch !== sessionEpoch) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    playback = createStreamingPlayback(expectedEpoch);
    // Barge-in starts only when the first response audio frame is scheduled.
    // Keeping the track disabled while the model is still thinking prevents
    // a resumed phrase from aborting an answer that has not begun.

    const payload = {
      audioBase64,
      mimeType: capture.mimeType,
      sessionState: serializedSessionState,
      turnMode,
    };
    requestController = new AbortController();
    activeRequestController = requestController;
    const response = await awaitVoiceTurnResult(
      fetch(VOICE_ENDPOINT, {
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
      }),
      () => requestController.abort(),
    );
    if (!response.ok) {
      fail(mapVoiceResponseError(response.status));
    }
    const completed = await awaitVoiceTurnResult(
      consumeVoiceStream(response, playback, expectedEpoch),
      () => requestController.abort(),
    );
    await awaitValidatedPlaybackCompletion(playback, expectedEpoch);
    if (!completeSessionResponse(expectedEpoch)) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    responseClockActive = false;
    return Object.freeze({
      ...completed.finalResult,
      interrupted: playback.interrupted,
      streamedAudio: completed.streamedAudio,
    });
  } catch (error) {
    liveSession?.cancel(
      error instanceof Error ? error : new Error("voice_api_unavailable"),
    );
    haltStreamingPlayback(
      playback,
      error instanceof Error ? error : new Error("voice_response_invalid"),
    );
    const stopCode = stoppedSessionCode(expectedEpoch);
    if (stopCode === "session_expired") {
      fail(stopCode);
    }
    if (turnTimedOut) {
      fail("voice_turn_timeout");
    }
    if (playback?.interruptedBeforeFinal) {
      fail("voice_interrupted");
    }
    if (error && typeof error === "object" && error.name === "AbortError") {
      fail(stopCode);
    }
    throw error;
  } finally {
    if (responseClockActive) {
      // Provider/network failures must not leave idle expiry suspended or
      // grant a fresh idle lease. Only validated playback completes a
      // response and refreshes the conversation clock.
      cancelSessionResponse(expectedEpoch);
    }
    finishGate.release(finishToken);
    audioBase64 = "";
    if (activeRequestController === requestController) {
      activeRequestController = undefined;
    }
    if (activeLiveSession === liveSession) {
      activeLiveSession = undefined;
    }
    if (pendingLiveSession?.recording === recording) {
      retirePendingLiveSession(new Error("request_cancelled"));
    }
    if (activeRecording === recording) {
      activeRecording = undefined;
    }
  }
}

function stopSession(reason = "request_cancelled") {
  const { pauseReason, stopCode } =
    classifyVoiceSessionStopReason(reason);
  const stoppedEpoch = sessionEpoch;
  rememberStoppedSession(stoppedEpoch, stopCode);
  sessionEpoch += 1;
  finishGate.reset();
  sessionExpiryWatchdog.disarm();

  if (activeRequestController) {
    activeRequestController.abort();
    activeRequestController = undefined;
  }
  if (activeLiveSession) {
    const liveSession = activeLiveSession;
    activeLiveSession = undefined;
    liveSession.cancel(new Error(stopCode));
  }
  retirePendingLiveSession(new Error(stopCode));
  if (activePlayback) {
    const playback = activePlayback;
    activePlayback = undefined;
    playback.reject(new Error(stopCode));
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
  releaseMicrophone(stopCode);
  sessionClock.reset();

  if (pauseReason !== null) {
    globalThis.dispatchEvent(
      new CustomEvent("kotae:voice-session-paused", {
        detail: Object.freeze({ reason: pauseReason, version: 1 }),
      }),
    );
  }
}

function hasActiveVoiceSession() {
  return Boolean(
    sessionClock.isStarted() ||
    activeRecording ||
    beginGate.isBusy() ||
    activeRequestController ||
    activeLiveSession ||
    pendingLiveSession ||
    activePlayback ||
    finishGate.isBusy() ||
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
    stopSession("hidden");
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
    stopSession("pagehide");
  }
});

const publicBridge = Object.freeze({
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
