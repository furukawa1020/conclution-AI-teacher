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
  advanceVad,
  createCaptureBuffer,
  createSessionClock,
  createTurnGate,
  createVadState,
  initializeWithCleanup,
  isPendingDocumentExpired,
  isValidTurnMode,
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
const RESPONSE_AUDIO_MAX_BASE64_CHARS = 20 * 1024 * 1024;
const SESSION_STATE_MAX_CHARS = 16 * 1024;
const VAD_INTERVAL_MS = VOICE_SESSION_LIMITS.vadIntervalMs;

const ALLOWED_CONFIG_KEYS = Object.freeze([
  "apiKey",
  "appId",
  "authDomain",
  "messagingSenderId",
  "projectId",
]);

let appServicesPromise;
let authenticatedUserPromise;
let mediaStream;
let audioContext;
let analyser;
let analyserSource;
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

function appServices() {
  appServicesPromise ??= initializeAppServices();
  return appServicesPromise;
}

async function initializeAuthenticatedUser() {
  const { app } = await appServices();
  const auth = initializeAuth(app, {
    persistence: browserSessionPersistence,
  });
  const credential = auth.currentUser
    ? { user: auth.currentUser }
    : await signInAnonymously(auth);
  return Object.freeze({ user: credential.user });
}

function authenticatedUser() {
  authenticatedUserPromise ??= initializeAuthenticatedUser();
  return authenticatedUserPromise;
}

async function getStatus() {
  if (!siteKeyConfigured()) {
    return Object.freeze({ state: "configuration-required" });
  }
  try {
    const [{ appCheck }, { user }] = await Promise.all([
      appServices(),
      authenticatedUser(),
    ]);
    const [idToken, appCheckResult] = await Promise.all([
      getIdToken(user, false),
      getAppCheckToken(appCheck, false),
    ]);
    const response = await fetch("/api/v1/me", {
      method: "GET",
      cache: "no-store",
      credentials: "same-origin",
      redirect: "error",
      referrerPolicy: "no-referrer",
      headers: {
        Authorization: `Bearer ${idToken}`,
        "X-Firebase-AppCheck": appCheckResult.token,
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
  if (activeRecording) {
    activeRecording.discard = true;
    requestRecordingStop(activeRecording, "cancelled");
    activeRecording = undefined;
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
  }
  if (context.state === "suspended") {
    await context.resume();
  }
  if (expectedEpoch !== sessionEpoch || audioContext !== context) {
    if (audioContext === context) {
      audioContext = undefined;
      analyser = undefined;
      analyserSource = undefined;
    }
    if (context.state !== "closed") {
      void context.close();
    }
    fail("request_cancelled");
  }
  if (!analyser || !analyserSource) {
    const nextAnalyser = context.createAnalyser();
    nextAnalyser.fftSize = 1024;
    nextAnalyser.smoothingTimeConstant = 0.18;
    const nextSource = context.createMediaStreamSource(stream);
    nextSource.connect(nextAnalyser);
    analyser = nextAnalyser;
    analyserSource = nextSource;
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

function requestRecordingStop(recording, reason) {
  if (!recording || recording.recorder.state === "inactive") {
    return;
  }
  recording.stopReason = reason;
  stopVad(recording);
  recording.recorder.stop();
}

function armVad(recording) {
  const pcm = new Float32Array(analyser.fftSize);
  let vadState = createVadState(recording.startedAt);

  recording.vadTimer = setInterval(() => {
    if (
      recording.discard ||
      recording.recorder.state !== "recording" ||
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
    recording.hasSpeech = vadState.hasSpeech;
    recording.lastVoiceAt = vadState.lastVoiceAt ?? 0;
    if (vadState.action !== null) {
      requestRecordingStop(recording, vadState.action);
    }
  }, VAD_INTERVAL_MS);
}

function createRecording(stream) {
  let recorder;
  try {
    recorder = new MediaRecorder(stream, recorderOptions());
  } catch {
    fail("microphone_unsupported");
  }

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
    captureBuffer: createCaptureBuffer({ maximumBytes: AUDIO_MAX_BYTES }),
    discard: false,
    endPromise,
    hasSpeech: false,
    lastVoiceAt: 0,
    recorder,
    resolveEnd,
    rejectEnd,
    startedAt: performance.now(),
    stopReason: "",
    totalBytes: 0,
    vadTimer: undefined,
  };

  recorder.addEventListener("dataavailable", (event) => {
    if (recording.discard || !event.data || event.data.size === 0) {
      return;
    }
    const captureState = recording.captureBuffer.append(
      event.data,
      recording.hasSpeech,
    );
    recording.totalBytes = captureState.totalBytes;
    if (captureState.tooLarge) {
      recording.discard = true;
      requestRecordingStop(recording, "too-large");
    }
  });

  recorder.addEventListener(
    "error",
    () => {
      stopVad(recording);
      setStreamTracksEnabled(stream, false);
      recording.captureBuffer.clear();
      recording.rejectEnd(new Error("voice_turn_invalid"));
    },
    { once: true },
  );

  recorder.addEventListener(
    "stop",
    () => {
      stopVad(recording);
      setStreamTracksEnabled(stream, false);
      if (recording.discard) {
        recording.captureBuffer.clear();
        recording.rejectEnd(
          new Error(
            recording.stopReason === "too-large"
              ? "voice_turn_too_large"
              : "request_cancelled",
          ),
        );
        return;
      }

      const captured = recording.captureBuffer.take();
      const mimeType =
        recorder.mimeType ||
        captured.chunks[0]?.type ||
        "audio/webm";
      const blob = new Blob(captured.chunks, {
        type: mimeType,
      });
      recording.resolveEnd(
        Object.freeze({
          blob,
          hasSpeech: recording.hasSpeech && blob.size > 0,
          mimeType,
          reason: recording.stopReason,
        }),
      );
    },
    { once: true },
  );

  try {
    recorder.start(250);
  } catch {
    fail("microphone_unsupported");
  }
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
        const stream = await ensureMediaStream(expectedEpoch);
      await ensureAudioGraph(stream, expectedEpoch);
        if (expectedEpoch !== sessionEpoch) {
          fail("request_cancelled");
        }

        setStreamTracksEnabled(stream, true);
        activeRecording = createRecording(stream);
        void authenticatedUser().catch(() => {});
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
    if (recording.recorder.state !== "inactive") {
      recording.discard = true;
      requestRecordingStop(recording, "cancelled");
    }
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
    reason: capture.reason,
  });
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
    !boundedString(payload.route, 100) ||
    typeof payload.needsPaper !== "boolean" ||
    (payload.caption !== undefined &&
      payload.caption !== null &&
      !boundedString(payload.caption, 2_000))
  ) {
    fail("voice_response_invalid");
  }

  return Object.freeze({
    audioBase64: payload.audioBase64,
    audioMimeType: hasAudio ? payload.audioMimeType : "",
    caption: typeof payload.caption === "string" ? payload.caption : null,
    detectedDomain: payload.detectedDomain,
    needsPaper: payload.needsPaper,
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
    if (recording.recorder.state === "recording") {
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
    const [{ appCheck }, { user }] = await Promise.all([
      appServices(),
      authenticatedUser(),
    ]);
    const [idToken, appCheckResult] = await Promise.all([
      getIdToken(user, false),
      getAppCheckToken(appCheck, false),
    ]);
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
        "X-Firebase-AppCheck": appCheckResult.token,
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
    try {
      activePlayback.source.stop();
    } catch {
      // The source may already have ended.
    }
    activePlayback = undefined;
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
