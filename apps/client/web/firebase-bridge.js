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

const EXPECTED_PROJECT_ID = "kotae-ai-u22-2026";
const EXPECTED_APP_ID = "1:551920539470:web:6518baf6d84d7ab89eb01f";
const EXPECTED_AUTH_DOMAIN = "kotae-ai-u22-2026.firebaseapp.com";
const EXPECTED_MESSAGING_SENDER_ID = "551920539470";
// reCAPTCHA Enterprise site keys are public identifiers. The matching secret
// configuration and verification remain in Firebase App Check.
const RECAPTCHA_SITE_KEY = "6Le4EmotAAAAAPEp5sfcmDtCAeaKd4y9er6KA71U";
const VOICE_ENDPOINT = "/api/v1/voice/turns";

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
let pendingDocument;
let pendingDocumentTimer;
let sessionEpoch = 0;
let documentEpoch = 0;
const beginGate = createTurnGate();
const finishGate = createTurnGate();
const sessionClock = createSessionClock({
  now: () => performance.now(),
});

function fail(code) {
  throw new Error(code);
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
    return new AudioContextConstructor({ latencyHint: "interactive" });
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

function createRecording(stream) {
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

  armVad(recording);
  return recording;
}

async function beginTurn() {
  if (document.hidden) {
    stopSession();
    fail("request_cancelled");
  }
  if (activeRecording || beginGate.isBusy() || finishGate.isBusy()) {
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

    const expectedEpoch = sessionEpoch;
    return await initializeWithCleanup(
      async () => {
        await secureCredentials();
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
        return Object.freeze({ state: "listening" });
      },
      () => {
        if (expectedEpoch === sessionEpoch) {
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
    audioMimeType: hasAudio ? payload.audioMimeType : "",
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
    audioBase64 = arrayBufferToBase64(await capture.blob.arrayBuffer());
    const { appCheckToken, idToken } = await secureCredentials();
    if (expectedEpoch !== sessionEpoch) {
      fail("request_cancelled");
    }

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
      credentials: "same-origin",
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
    return safeVoiceResponse(await response.json());
  } catch (error) {
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
    if (activeRecording === recording) {
      activeRecording = undefined;
    }
  }
}

async function playResponse(audioBase64, audioMimeType) {
  setTracksEnabled(false);
  if (audioBase64 === "") {
    return Object.freeze({ state: "silent" });
  }
  if (
    !isBase64(audioBase64) ||
    audioBase64.length > RESPONSE_AUDIO_MAX_BASE64_CHARS ||
    typeof audioMimeType !== "string" ||
    !audioMimeType.startsWith("audio/")
  ) {
    fail("voice_response_invalid");
  }
  if (!audioContext || audioContext.state === "closed") {
    fail("audio_playback_blocked");
  }

  const expectedEpoch = sessionEpoch;
  try {
    if (audioContext.state === "suspended") {
      await audioContext.resume();
    }
    const encoded = base64ToArrayBuffer(audioBase64);
    const decoded = await audioContext.decodeAudioData(encoded.slice(0));
    if (expectedEpoch !== sessionEpoch) {
      fail("request_cancelled");
    }

    await new Promise((resolve, reject) => {
      const source = audioContext.createBufferSource();
      source.buffer = decoded;
      source.connect(audioContext.destination);
      source.addEventListener(
        "ended",
        () => {
          if (activePlayback?.source === source) {
            activePlayback = undefined;
          }
          source.disconnect();
          resolve();
        },
        { once: true },
      );
      activePlayback = { reject, source };
      try {
        source.start();
      } catch {
        activePlayback = undefined;
        source.disconnect();
        reject(new Error("audio_playback_blocked"));
      }
    });
  } catch (error) {
    if (error instanceof Error && error.message === "request_cancelled") {
      throw error;
    }
    fail("audio_playback_blocked");
  }
  return Object.freeze({ state: "played" });
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
  if (activePlayback) {
    const playback = activePlayback;
    activePlayback = undefined;
    playback.reject(new Error("request_cancelled"));
    try {
      playback.source.stop();
    } catch {
      // The source may already have ended.
    }
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
  playResponse,
  stopSession,
  waitForTurnEnd,
});

Object.defineProperty(globalThis, "kotaeCloud", {
  configurable: false,
  enumerable: false,
  value: publicBridge,
  writable: false,
});
