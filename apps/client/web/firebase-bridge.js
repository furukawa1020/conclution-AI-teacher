import { getApp, getApps, initializeApp } from "https://www.gstatic.com/firebasejs/12.16.0/firebase-app.js";
import {
  browserSessionPersistence,
  getIdToken,
  getIdTokenResult,
  initializeAuth,
  signInWithCustomToken,
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
  isPendingDocumentExpired,
  isValidTurnMode,
  normalizeResearchDiscovery,
  shouldCommitHybridEndpoint,
  shouldShowVoiceReceipt,
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
  INTERRUPT_ECHO_PROBE_LIMITS,
  INTERRUPT_VAD_LIMITS,
  isCleanVoiceLiveTerminalClose,
  safeLiveCaptureFrame,
  safeLiveCaptureSignal,
  shouldReplayCommittedNativeTurn,
  shouldStartAmbientLiveHandoff,
  validatedPlaybackDrainTimeoutMs,
  VOICE_LIVE_LIMITS,
  shouldAbortVoiceTransportOnInterrupt,
  VOICE_STREAM_LIMITS,
} from "./voice-stream-policy.mjs";
import {
  advanceVoiceStartSlo,
  beginVoiceStartSlo,
  cancelVoiceStartSlo,
  classifyVoiceStartSloLatency,
  classifyVoiceStartSloRoute,
  createVoiceStartSloState,
  updateVoiceStartSloRoute,
  VOICE_START_SLO_ACTIONS,
  VOICE_START_SLO_BUDGETS,
} from "./voice-start-slo-policy.mjs";
import {
  beginVoicePrepareSlo,
  cancelVoicePrepareSlo,
  completeVoicePrepareSlo,
  createVoicePrepareSloState,
  toVoicePrepareSloWireDetail,
  VOICE_PREPARE_SLO_RESULTS,
  VOICE_PREPARE_SLO_ROUTES,
} from "./voice-prepare-slo-policy.mjs";
import {
  decidePasskeyAction,
  createPasskeyRegistrationRecoveryLatch,
  decidePasskeyRegistrationRecoveryAction,
  decodeAuthenticationBegin,
  decodeRegistrationBegin,
  encodeAuthenticationCredential,
  encodeRegistrationCredential,
  isPasskeyCancellation,
  parsePasskeyFinish,
} from "./passkey-policy.mjs";

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
const PASSKEY_REGISTRATION_BEGIN_ENDPOINT =
  "/api/v1/passkeys/registration:begin";
const PASSKEY_REGISTRATION_FINISH_ENDPOINT =
  "/api/v1/passkeys/registration:finish";
const PASSKEY_AUTHENTICATION_BEGIN_ENDPOINT =
  "/api/v1/passkeys/authentication:begin";
const PASSKEY_AUTHENTICATION_FINISH_ENDPOINT =
  "/api/v1/passkeys/authentication:finish";
// The server caps finish bodies at 256 KiB. JSON produced here is ASCII-only,
// so a character limit is also a byte-safe upper bound before fetch.
const PASSKEY_JSON_MAX_CHARS = 255 * 1024;
const passkeyRegistrationRecovery = createPasskeyRegistrationRecoveryLatch(
  () => globalThis.sessionStorage,
);

const DOCUMENT_MAX_BYTES = 7 * 1024 * 1024;
const AUDIO_MAX_BYTES = 2 * 1024 * 1024;
const RESPONSE_AUDIO_MAX_BASE64_CHARS = 4 * Math.ceil(AUDIO_MAX_BYTES / 3);
const SESSION_STATE_MAX_CHARS =
  VOICE_LIVE_LIMITS.maximumSessionStateCharacters;
const VAD_INTERVAL_MS = VOICE_SESSION_LIMITS.vadIntervalMs;
const VOICE_TURN_CLIENT_TIMEOUT_MS = 60_000;
const VOICE_START_AUDIBLE_COMMIT_LOOKAHEAD_MS = 250;

const ALLOWED_CONFIG_KEYS = Object.freeze([
  "apiKey",
  "appId",
  "authDomain",
  "messagingSenderId",
  "projectId",
]);

let authInstance;
let verifiedAccountUid;
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
let preparingLiveSession;
let pendingLiveSession;
let pendingDocument;
let pendingDocumentTimer;
let voiceTransportPrimed = false;
let voiceReceiptVisible = false;
let voicePrepareSloGeneration = 0;
let voicePrepareSloState = createVoicePrepareSloState();
let voiceStartSloGeneration = 0;
let voiceStartSloDeferredMissTimer;
let voiceStartSloHandlers;
let voiceStartSloMissTimer;
let voiceStartSloStallTimer;
let voiceStartSloState = createVoiceStartSloState();
let sessionEpoch = 0;
let documentEpoch = 0;
let pcmCaptureGeneration = 0;
const MAX_STOPPED_SESSION_CODES = 8;
const stoppedSessionCodes = new Map();
const beginGate = createTurnGate();
const finishGate = createTurnGate();
const passkeyGate = createTurnGate();
let activePasskeyController;
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

function setVoiceReceiptVisible(visible) {
  if (typeof visible !== "boolean" || visible === voiceReceiptVisible) {
    return;
  }
  voiceReceiptVisible = visible;
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-receipt", {
      detail: Object.freeze({
        phase: visible ? "received" : "clear",
        version: 1,
      }),
    }),
  );
}

function updateVoiceReceipt(recording, now) {
  setVoiceReceiptVisible(
    shouldShowVoiceReceipt({
      hasSpeech: recording.vadHasSpeech,
      lastVoiceAt: recording.lastVoiceAt,
      now,
    }),
  );
}

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

function nullableLatency(value) {
  return Number.isFinite(value) && value >= 0
    ? boundedLatency(value)
    : null;
}

function dispatchVoiceLatency({
  authReadyMs,
  bargeHaltMs,
  commitToEstimatedAudibleMs,
  commitToFirstAudioMs,
  firstBinaryMs,
  substantiveAudio = false,
  speechEndToEstimatedAudibleMs,
  turnTotalMs,
  wsOpenMs,
}) {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-latency", {
      detail: Object.freeze({
        auth_ready_ms: nullableLatency(authReadyMs),
        barge_halt_ms: nullableLatency(bargeHaltMs),
        commit_to_estimated_audible_ms:
          substantiveAudio
            ? nullableLatency(commitToEstimatedAudibleMs)
            : null,
        // Transport arrival remains useful even when the decoded response is
        // silent. Only audible metrics below require a meaningful sample.
        commit_to_first_audio_ms: nullableLatency(commitToFirstAudioMs),
        first_binary_ms: nullableLatency(firstBinaryMs),
        speech_end_to_estimated_audible_ms:
          substantiveAudio &&
          Number.isFinite(speechEndToEstimatedAudibleMs)
          ? nullableLatency(speechEndToEstimatedAudibleMs)
          : null,
        // A content-free receipt never flips this bit. It becomes true only
        // after decoded PCM contains a meaningful sample and is scheduled on
        // the output device timeline.
        substantive_audio: substantiveAudio,
        turn_total_ms: nullableLatency(turnTotalMs),
        version: 4,
        ws_open_ms: nullableLatency(wsOpenMs),
      }),
    }),
  );
}

function dispatchVoiceStartLatency(estimatedAudibleMs) {
  const milliseconds =
    Number.isFinite(estimatedAudibleMs) && estimatedAudibleMs >= 0
      ? boundedLatency(estimatedAudibleMs)
      : null;
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-start-latency", {
      detail: Object.freeze({ milliseconds, version: 1 }),
    }),
  );
}

function nextVoicePrepareSloGeneration() {
  if (voicePrepareSloGeneration >= Number.MAX_SAFE_INTEGER) {
    fail("voice_turn_invalid");
  }
  voicePrepareSloGeneration += 1;
  return voicePrepareSloGeneration;
}

function dispatchVoicePrepareSloObservation(observation) {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-prepare-slo", {
      detail: Object.freeze(
        toVoicePrepareSloWireDetail(observation),
      ),
    }),
  );
}

function dispatchVoicePrepareSloClear() {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-prepare-slo-clear", {
      detail: Object.freeze({ version: 1 }),
    }),
  );
}

function beginCurrentVoicePrepareSlo(route, startedAt) {
  const generation = nextVoicePrepareSloGeneration();
  const transition = beginVoicePrepareSlo(voicePrepareSloState, {
    generation,
    route,
    startedAt,
  });
  voicePrepareSloState = transition.state;
  return generation;
}

function completeCurrentVoicePrepareSlo(
  generation,
  result,
  route,
  endedAt = performance.now(),
) {
  const transition = completeVoicePrepareSlo(voicePrepareSloState, {
    endedAt,
    generation,
    result,
    route,
  });
  voicePrepareSloState = transition.state;
  if (transition.observation !== null) {
    dispatchVoicePrepareSloObservation(transition.observation);
  }
}

function cancelCurrentVoicePrepareSlo(
  generation,
  endedAt = performance.now(),
) {
  const transition = cancelVoicePrepareSlo(voicePrepareSloState, {
    endedAt,
    generation,
  });
  voicePrepareSloState = transition.state;
  if (transition.observation !== null) {
    dispatchVoicePrepareSloObservation(transition.observation);
  }
}

function nextVoiceStartSloGeneration() {
  if (voiceStartSloGeneration >= Number.MAX_SAFE_INTEGER) {
    fail("voice_response_invalid");
  }
  voiceStartSloGeneration += 1;
  return voiceStartSloGeneration;
}

function clearVoiceStartSloTimer(timer, expectedGeneration) {
  if (!timer || timer.generation !== expectedGeneration) return timer;
  clearTimeout(timer.id);
  return undefined;
}

function clearVoiceStartSloTimers(expectedGeneration) {
  voiceStartSloDeferredMissTimer = clearVoiceStartSloTimer(
    voiceStartSloDeferredMissTimer,
    expectedGeneration,
  );
  voiceStartSloMissTimer = clearVoiceStartSloTimer(
    voiceStartSloMissTimer,
    expectedGeneration,
  );
  voiceStartSloStallTimer = clearVoiceStartSloTimer(
    voiceStartSloStallTimer,
    expectedGeneration,
  );
}

function invokeVoiceStartSloMissHandler(handlers, state) {
  if (typeof handlers.onMiss !== "function") return;
  const remainingMs = handlers.missNotBefore - performance.now();
  if (remainingMs <= 0) {
    handlers.onMiss();
    return;
  }
  if (
    voiceStartSloDeferredMissTimer?.generation === state.generation
  ) {
    return;
  }
  const timer = {
    generation: state.generation,
    id: undefined,
  };
  timer.id = setTimeout(() => {
    if (voiceStartSloDeferredMissTimer === timer) {
      voiceStartSloDeferredMissTimer = undefined;
    }
    if (
      voiceStartSloState.active !== true ||
      voiceStartSloState.generation !== state.generation ||
      voiceStartSloHandlers !== handlers
    ) {
      return;
    }
    handlers.onMiss();
  }, remainingMs);
  voiceStartSloDeferredMissTimer = timer;
}

function dispatchVoiceStartSloMilestone(action, state) {
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-start-slo-milestone", {
      detail: Object.freeze({
        generation: state.generation,
        milestone: action,
        route: state.route,
        version: 1,
      }),
    }),
  );
}

function dispatchVoiceStartSloObservation(measurement) {
  const latencyMs = boundedLatency(measurement.latencyMs);
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-start-slo", {
      detail: Object.freeze({
        generation: measurement.generation,
        latency_ms: latencyMs,
        outcome: classifyVoiceStartSloLatency(latencyMs),
        route: measurement.route,
        version: 1,
      }),
    }),
  );
}

function applyVoiceStartSloTransition(transition) {
  const previousState = voiceStartSloState;
  voiceStartSloState = transition.state;
  const measurement = transition.measurement;
  const handlers = voiceStartSloHandlers;
  const cancelGeneration = previousState.active
    ? previousState.generation
    : transition.state.generation;
  // Timer ownership is revoked before any synchronous CustomEvent listener can
  // re-enter the bridge and create a replacement generation.
  for (const action of transition.actions) {
    if (action === VOICE_START_SLO_ACTIONS.CANCEL_TIMERS) {
      clearVoiceStartSloTimers(cancelGeneration);
    }
  }
  for (const action of transition.actions) {
    if (action === VOICE_START_SLO_ACTIONS.CANCEL_TIMERS) {
      continue;
    }
    if (
      voiceStartSloState.generation !== transition.state.generation
    ) {
      break;
    }
    dispatchVoiceStartSloMilestone(action, transition.state);
    // A delayed timer and meaningful audio can enter the same transition.
    // Publish the measured miss, but never cancel or replay audio that has
    // already won the single playback owner.
    if (
      measurement !== null ||
      handlers?.generation !== transition.state.generation ||
      voiceStartSloState.active !== true ||
      voiceStartSloState.generation !== transition.state.generation
    ) {
      continue;
    }
    if (action === VOICE_START_SLO_ACTIONS.THREE_SECOND_MISS) {
      invokeVoiceStartSloMissHandler(handlers, transition.state);
    } else if (action === VOICE_START_SLO_ACTIONS.TEN_SECOND_STALL) {
      handlers.onStall?.();
    }
  }
  if (
    measurement !== null &&
    voiceStartSloState.generation === transition.state.generation
  ) {
    dispatchVoiceStartSloObservation(measurement);
    if (
      voiceStartSloState.generation === transition.state.generation
    ) {
      dispatchVoiceStartLatency(measurement.latencyMs);
    }
  }
  if (
    !voiceStartSloState.active &&
    voiceStartSloState.generation === transition.state.generation &&
    voiceStartSloHandlers?.generation === transition.state.generation
  ) {
    voiceStartSloHandlers = undefined;
  }
}

function advanceCurrentVoiceStartSlo(
  generation,
  meaningfulAudio,
  now = performance.now(),
) {
  applyVoiceStartSloTransition(
    advanceVoiceStartSlo(voiceStartSloState, {
      generation,
      meaningfulAudio,
      now,
    }),
  );
}

function beginCurrentVoiceStartSlo({
  generation,
  onMiss,
  onStall,
  operationalStartedAt,
  route,
  startedAt,
}) {
  if (
    !Number.isFinite(operationalStartedAt) ||
    operationalStartedAt < startedAt
  ) {
    fail("voice_response_invalid");
  }
  const transition = beginVoiceStartSlo(voiceStartSloState, {
    generation,
    route,
    startedAt,
  });
  applyVoiceStartSloTransition(transition);
  voiceStartSloHandlers = Object.freeze({
    generation,
    missNotBefore:
      operationalStartedAt + VOICE_START_SLO_BUDGETS.missedMs,
    onMiss: typeof onMiss === "function" ? onMiss : undefined,
    onStall: typeof onStall === "function" ? onStall : undefined,
  });
  const now = performance.now();
  const missTimer = { generation, id: undefined };
  missTimer.id = setTimeout(
    () => {
      if (voiceStartSloMissTimer === missTimer) {
        voiceStartSloMissTimer = undefined;
      }
      advanceCurrentVoiceStartSlo(generation, false);
    },
    Math.max(
      0,
      startedAt + VOICE_START_SLO_BUDGETS.missedMs - now,
    ),
  );
  voiceStartSloMissTimer = missTimer;
  const stallTimer = { generation, id: undefined };
  stallTimer.id = setTimeout(
    () => {
      if (voiceStartSloStallTimer === stallTimer) {
        voiceStartSloStallTimer = undefined;
      }
      advanceCurrentVoiceStartSlo(generation, false);
    },
    Math.max(
      0,
      startedAt + VOICE_START_SLO_BUDGETS.stalledMs - now,
    ),
  );
  voiceStartSloStallTimer = stallTimer;
}

function updateCurrentVoiceStartSloRoute(generation, route) {
  voiceStartSloState = updateVoiceStartSloRoute(
    voiceStartSloState,
    { generation, route },
  );
}

function cancelCurrentVoiceStartSlo(generation) {
  applyVoiceStartSloTransition(
    cancelVoiceStartSlo(voiceStartSloState, { generation }),
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

function customTokenCandidateUser(user) {
  return Boolean(
    user &&
      !user.isAnonymous &&
      Array.isArray(user.providerData) &&
      user.providerData.length === 0,
  );
}

function currentAccountUser(auth) {
  if (
    verifiedAccountUser(auth.currentUser) ||
    customTokenCandidateUser(auth.currentUser)
  ) {
    return auth.currentUser;
  }
  if (auth.currentUser) {
    fail("identity_verification_failed");
  }
  return null;
}

function passkeyAccountUid(user) {
  const uid = user?.uid;
  if (typeof uid !== "string" || !/^pk_[A-Za-z0-9_-]{32}$/u.test(uid)) {
    fail("identity_verification_failed");
  }
  return uid;
}

function passkeySessionSnapshot(user, tokenResult) {
  const provider =
    tokenResult?.signInProvider === "google.com"
      ? "google.com"
      : tokenResult?.signInProvider === "custom"
        ? "custom"
        : "";
  return Object.freeze({
    accountVerified:
      provider === "google.com"
        ? verifiedAccountUser(user)
        : tokenResult?.claims?.kotae_account_verified === true,
    authMethod: tokenResult?.claims?.kotae_authn,
    passkeyAtSeconds: tokenResult?.claims?.kotae_passkey_at,
    provider,
  });
}

function requirePasskeySupport(operation) {
  const method = operation === "registration" ? "create" : "get";
  if (
    globalThis.isSecureContext !== true ||
    typeof globalThis.PublicKeyCredential !== "function" ||
    !navigator.credentials ||
    typeof navigator.credentials[method] !== "function"
  ) {
    fail("passkey_unsupported");
  }
}

function passkeyRequestToken(value) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > 32 * 1024 ||
    /\s/u.test(value)
  ) {
    fail("authentication_failed");
  }
  return value;
}

async function passkeyJSON(
  endpoint,
  { appCheckToken, beforeRequest, body, failureCode, signal },
) {
  const headers = {
    Accept: "application/json",
    "X-Firebase-AppCheck": passkeyRequestToken(appCheckToken),
  };
  let serializedBody;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    serializedBody = JSON.stringify(body);
    if (
      serializedBody.length === 0 ||
      serializedBody.length > PASSKEY_JSON_MAX_CHARS
    ) {
      fail(failureCode);
    }
  }
  if (beforeRequest !== undefined) {
    if (typeof beforeRequest !== "function") fail(failureCode);
    beforeRequest();
  }

  let response;
  try {
    response = await fetch(endpoint, {
      method: "POST",
      body: serializedBody,
      cache: "no-store",
      credentials: "omit",
      headers,
      redirect: "error",
      referrerPolicy: "no-referrer",
      signal,
    });
  } catch {
    if (signal?.aborted) fail("passkey_cancelled");
    fail(failureCode);
  }
  if (!response.ok) fail(failureCode);
  const contentType = response.headers.get("Content-Type") ?? "";
  if (!/^application\/json(?:\s*;|$)/iu.test(contentType)) fail(failureCode);
  const encoded = await response.text();
  if (encoded.length === 0 || encoded.length > PASSKEY_JSON_MAX_CHARS) {
    fail(failureCode);
  }
  try {
    return JSON.parse(encoded);
  } catch {
    fail(failureCode);
  }
}

async function runPasskeyOperation(failureCode, operation) {
  const passkeyToken = passkeyGate.acquire();
  if (passkeyToken === null) fail(failureCode);
  const controller = new AbortController();
  activePasskeyController = controller;
  try {
    return await operation(controller.signal);
  } finally {
    if (activePasskeyController === controller) {
      activePasskeyController = undefined;
    }
    passkeyGate.release(passkeyToken);
  }
}

function finishEndpoint(endpoint, ceremonyId) {
  return `${endpoint}?ceremonyId=${encodeURIComponent(ceremonyId)}`;
}

function preservePasskeyBoundaryError(error) {
  return Boolean(
    error instanceof Error &&
      (error.message === "passkey_cancelled" ||
        error.message === "passkey_registration_cancelled" ||
        error.message === "passkey_registration_recovery_required" ||
        error.message === "passkey_unsupported"),
  );
}

async function verifyFreshCustomPasskeyUser(user, failureCode) {
  const tokenResult = await getIdTokenResult(user, true);
  const action = decidePasskeyAction(
    passkeySessionSnapshot(user, tokenResult),
    Math.floor(Date.now() / 1000),
  );
  if (action !== "reuse") fail(failureCode);
  return user;
}

async function registerPasskey(auth, appCheckToken) {
  requirePasskeySupport("registration");
  return runPasskeyOperation("passkey_registration_failed", async (signal) => {
    let credential;
    let encodedCredential;
    let registrationFinishStarted = false;
    try {
      const begin = decodeRegistrationBegin(
        await passkeyJSON(PASSKEY_REGISTRATION_BEGIN_ENDPOINT, {
          appCheckToken,
          failureCode: "passkey_registration_failed",
          signal,
        }),
      );
      try {
        credential = await navigator.credentials.create({
          ...begin.options,
          signal,
        });
      } catch (error) {
        if (isPasskeyCancellation(error)) {
          fail("passkey_registration_cancelled");
        }
        throw error;
      }
      if (!credential) fail("passkey_registration_failed");
      encodedCredential = encodeRegistrationCredential(credential);
      const finish = parsePasskeyFinish(
        await passkeyJSON(
          finishEndpoint(PASSKEY_REGISTRATION_FINISH_ENDPOINT, begin.ceremonyId),
          {
            appCheckToken,
            beforeRequest: () => {
              if (
                !passkeyRegistrationRecovery.mark(
                  begin.options.publicKey.user.name,
                )
              ) {
                fail("passkey_registration_failed");
              }
              registrationFinishStarted = true;
            },
            body: encodedCredential,
            failureCode: "passkey_registration_recovery_required",
            signal,
          },
        ),
      );
      const signedIn = await signInWithCustomToken(auth, finish.customToken);
      const verified = await verifyFreshCustomPasskeyUser(
        signedIn.user,
        "passkey_registration_recovery_required",
      );
      if (!passkeyRegistrationRecovery.clear(passkeyAccountUid(verified))) {
        fail("passkey_registration_recovery_required");
      }
      return verified;
    } catch (error) {
      if (registrationFinishStarted) {
        fail("passkey_registration_recovery_required");
      }
      if (error instanceof Error && error.message === "passkey_cancelled") {
        fail("passkey_registration_cancelled");
      }
      if (preservePasskeyBoundaryError(error)) throw error;
      fail("passkey_registration_failed");
    } finally {
      credential = undefined;
      encodedCredential = undefined;
    }
  });
}

async function authenticatePasskey(auth, appCheckToken) {
  requirePasskeySupport("authentication");
  return runPasskeyOperation("passkey_authentication_failed", async (signal) => {
    let credential;
    let encodedCredential;
    try {
      const begin = decodeAuthenticationBegin(
        await passkeyJSON(PASSKEY_AUTHENTICATION_BEGIN_ENDPOINT, {
          appCheckToken,
          failureCode: "passkey_authentication_failed",
          signal,
        }),
      );
      try {
        credential = await navigator.credentials.get({
          ...begin.options,
          signal,
        });
      } catch (error) {
        if (isPasskeyCancellation(error)) fail("passkey_cancelled");
        throw error;
      }
      if (!credential) fail("passkey_authentication_failed");
      encodedCredential = encodeAuthenticationCredential(credential);
      const finish = parsePasskeyFinish(
        await passkeyJSON(
          finishEndpoint(PASSKEY_AUTHENTICATION_FINISH_ENDPOINT, begin.ceremonyId),
          {
            appCheckToken,
            body: encodedCredential,
            failureCode: "passkey_authentication_failed",
            signal,
          },
        ),
      );
      const signedIn = await signInWithCustomToken(auth, finish.customToken);
      const verified = await verifyFreshCustomPasskeyUser(
        signedIn.user,
        "passkey_authentication_failed",
      );
      return verified;
    } catch (error) {
      if (preservePasskeyBoundaryError(error)) throw error;
      fail("passkey_authentication_failed");
    } finally {
      credential = undefined;
      encodedCredential = undefined;
    }
  });
}

async function freshPasskeyUser(auth, user, appCheckToken, interactive) {
  if (!user) {
    if (!interactive) fail("passkey_required");
    return authenticatePasskey(auth, appCheckToken);
  }
  const tokenResult = await getIdTokenResult(user, false);
  const action = decidePasskeyAction(
    passkeySessionSnapshot(user, tokenResult),
    Math.floor(Date.now() / 1000),
  );
  if (action === "reuse") return user;
  if (action === "reject") fail("identity_verification_failed");
  if (!interactive) fail("passkey_required");
  if (action === "registration") {
    fail("passkey_required");
  }
  return authenticatePasskey(auth, appCheckToken);
}

async function registerPasskeyAccount() {
  if (passkeyRegistrationRecovery.isPending()) {
    fail("passkey_registration_recovery_required");
  }
  if (document.hidden || hasActiveVoiceSession() || passkeyGate.isBusy()) {
    fail("passkey_registration_failed");
  }
  try {
    const { appCheck } = await appServices();
    const { auth } = await firebaseAuth();
    const appCheckResult = await getAppCheckToken(appCheck, false);
    let user;
    try {
      user = currentAccountUser(auth);
      if (user) {
        const tokenResult = await getIdTokenResult(user, false);
        const action = decidePasskeyAction(
          passkeySessionSnapshot(user, tokenResult),
          Math.floor(Date.now() / 1000),
        );
        if (action === "reject") {
          fail("identity_verification_failed");
        }
      }
    } catch (error) {
      if (
        !(error instanceof Error) ||
        error.message !== "identity_verification_failed"
      ) {
        throw error;
      }
      user = null;
    }
    if (user) {
      fail("passkey_account_exists");
    }
    const registeredUser = await registerPasskey(auth, appCheckResult.token);
    verifiedAccountUid = passkeyAccountUid(registeredUser);
    return Object.freeze({ state: "ready" });
  } catch (error) {
    if (
      error instanceof Error &&
      (error.message === "passkey_account_exists" ||
        preservePasskeyBoundaryError(error))
    ) {
      throw error;
    }
    fail("passkey_registration_failed");
  }
}

async function secureCredentials(interactive = false) {
  try {
    const { appCheck } = await appServices();
    const { auth } = await firebaseAuth();
    const appCheckResult = await getAppCheckToken(appCheck, false);
    const registrationRecoveryPending =
      passkeyRegistrationRecovery.isPending();
    let user;
    try {
      user = currentAccountUser(auth);
    } catch (error) {
      if (
        !interactive ||
        !(error instanceof Error) ||
        error.message !== "identity_verification_failed"
      ) {
        throw error;
      }
      user = null;
    }
    const currentUserMatchesRecovery =
      user !== null && passkeyRegistrationRecovery.matches(user.uid);
    const registrationRecoveryAction =
      decidePasskeyRegistrationRecoveryAction({
        currentAccountMatches: currentUserMatchesRecovery,
        interactive,
        pending: registrationRecoveryPending,
      });
    if (registrationRecoveryAction === "block") {
      fail("passkey_registration_recovery_required");
    }
    let authorizedUser;
    try {
      authorizedUser =
        registrationRecoveryAction === "authenticate"
          ? await authenticatePasskey(auth, appCheckResult.token)
          : await freshPasskeyUser(
              auth,
              user,
              appCheckResult.token,
              interactive,
            );
    } catch (error) {
      if (
        !interactive ||
        !(error instanceof Error) ||
        error.message !== "identity_verification_failed"
      ) {
        throw error;
      }
      authorizedUser = await authenticatePasskey(auth, appCheckResult.token);
    }
    const idToken = await getIdToken(authorizedUser, false);
    const accountUid = passkeyAccountUid(authorizedUser);
    const accountBoundaryChanged =
      verifiedAccountUid !== undefined && verifiedAccountUid !== accountUid;
    verifiedAccountUid = accountUid;
    const registrationRecoveryConfirmed =
      !registrationRecoveryPending ||
      passkeyRegistrationRecovery.clear(accountUid);
    if (accountBoundaryChanged) {
      globalThis.dispatchEvent(new Event("kotae:account-boundary-changed"));
      fail("account_boundary_changed");
    }
    if (!registrationRecoveryConfirmed) {
      fail("passkey_registration_recovery_required");
    }
    if (interactive) {
      // This event requests a fresh status read in Rust. It carries no
      // authorization and is never itself accepted as account proof.
      globalThis.dispatchEvent(new Event("kotae:account-access-confirmed"));
    }
    return Object.freeze({
      appCheckToken: appCheckResult.token,
      idToken,
    });
  } catch (error) {
    if (error instanceof Error) {
      switch (error.message) {
        case "app_check_not_configured":
        case "account_boundary_changed":
        case "identity_required":
        case "identity_verification_failed":
        case "passkey_authentication_failed":
        case "passkey_cancelled":
        case "passkey_registration_cancelled":
        case "passkey_registration_failed":
        case "passkey_registration_recovery_required":
        case "passkey_required":
        case "passkey_unsupported":
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
    if (passkeyRegistrationRecovery.isPending()) {
      return Object.freeze({
        state: "passkey-registration-recovery-required",
      });
    }
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
    if (passkeyRegistrationRecovery.isPending()) {
      return Object.freeze({
        state: "passkey-registration-recovery-required",
      });
    }
    if (
      error instanceof Error &&
      (error.message === "identity_required" ||
        error.message === "identity_verification_failed")
    ) {
      return Object.freeze({ state: "identity-required" });
    }
    if (error instanceof Error && error.message === "passkey_required") {
      return Object.freeze({ state: "passkey-required" });
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
  setVoiceReceiptVisible(false);
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
  if (activeRecording === recording && activePlayback === undefined) {
    setStreamTracksEnabled(recording.stream, false);
  }
  discardCurrentCandidate(recording);
  if (!recording.turnEnded) {
    recording.turnEnded = true;
    recording.rejectTurnEnded(new Error(code));
  }
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
  // MediaRecorder may publish its final Blob after live response playback has
  // already re-enabled this shared stream for barge-in. A stale fallback
  // completion must never revoke the newer microphone owner.
  if (activeRecording === recording && activePlayback === undefined) {
    setStreamTracksEnabled(recording.stream, false);
  }
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
  if (!recording.turnEnded) {
    recording.turnEnded = true;
    recording.resolveTurnEnded(
      Object.freeze({
        hasSpeech: Boolean(candidate?.confirmed),
        reason,
      }),
    );
  }
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
      coachActive: recording.coachActive,
      continuationEvidence: recording.continuationEvidence,
      firstVoiceAt: recording.firstVoiceAt,
      hasSpeech: recording.vadHasSpeech,
      lastVoiceAt: recording.lastVoiceAt,
      nativeAudio: recording.nativeAudio,
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
    vadState = advanceVad(
      vadState,
      { now, peak, rms },
      {
        coachActive: recording.coachActive,
        nativeAudio: recording.nativeAudio,
        ...(recording.coachActive
          ? {
              endOfTurnSilenceMs:
                VOICE_SESSION_LIMITS.coachEndOfTurnSilenceMs,
            }
          : recording.nativeAudio
            ? {
              endOfTurnSilenceMs:
                VOICE_SESSION_LIMITS.nativeAudioEndOfTurnSilenceMs,
            }
            : {}),
      },
    );
    recording.firstVoiceAt = vadState.firstVoiceAt;
    recording.continuationEvidence = vadState.continuationEvidence;
    const hadConfirmedSpeech = recording.vadHasSpeech;
    recording.vadHasSpeech = vadState.hasSpeech;
    if (!hadConfirmedSpeech && recording.vadHasSpeech) {
      globalThis.dispatchEvent(
        new CustomEvent("kotae:voice-input-confirmed", {
          detail: Object.freeze({ version: 1 }),
        }),
      );
    }
    recording.softVoiceConfirmed = vadState.softVoiceConfirmed;
    if (Number.isFinite(vadState.lastVoiceAt)) {
      recording.lastVoiceAt = vadState.lastVoiceAt;
    }
    updateVoiceReceipt(recording, now);
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

function createRecordingState(
  stream,
  nativeAudio = false,
  coachActive = false,
) {
  if (
    typeof nativeAudio !== "boolean" ||
    typeof coachActive !== "boolean"
  ) {
    fail("voice_turn_invalid");
  }
  let resolveEnd;
  let rejectEnd;
  let resolveTurnEnded;
  let rejectTurnEnded;
  const endPromise = new Promise((resolve, reject) => {
    resolveEnd = resolve;
    rejectEnd = reject;
  });
  const turnEndedPromise = new Promise((resolve, reject) => {
    resolveTurnEnded = resolve;
    rejectTurnEnded = reject;
  });
  // A stop can race the Rust caller before it starts awaiting the turn.
  // Mark the rejection handled without changing what later awaiters observe.
  void endPromise.catch(() => {});
  void turnEndedPromise.catch(() => {});
  const recording = {
    candidate: undefined,
    continuationEvidence: false,
    discard: false,
    endPromise,
    expectedEpoch: sessionEpoch,
    fallbackAudioComplete: true,
    firstVoiceAt: null,
    lastVoiceAt: null,
    liveSpeechConfirmed: false,
    liveSpeechStartedAt: null,
    nativeAudio,
    coachActive,
    providerEndpointAt: null,
    resolveEnd,
    resolveTurnEnded,
    rejectEnd,
    rejectTurnEnded,
    settled: false,
    sessionSpeechMarked: false,
    startedAt: performance.now(),
    stopLatch: createStopLatch(),
    stopReason: "",
    stream,
    softVoiceConfirmed: false,
    totalBytes: 0,
    turnEnded: false,
    turnEndedPromise,
    vadHasSpeech: false,
    vadPcm: undefined,
    vadTimer: undefined,
  };
  return recording;
}

function createRecording(
  stream,
  nativeAudio = false,
  coachActive = false,
) {
  setVoiceReceiptVisible(false);
  const recording = createRecordingState(
    stream,
    nativeAudio,
    coachActive,
  );
  armVad(recording);
  return recording;
}

async function beginTurn(
  serializedSessionState,
  turnMode,
  strictCloudMinimization,
  coachActive = false,
) {
  if (document.hidden) {
    stopSession("hidden");
    fail("request_cancelled");
  }
  if (
    typeof serializedSessionState !== "string" ||
    serializedSessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode) ||
    typeof strictCloudMinimization !== "boolean" ||
    typeof coachActive !== "boolean" ||
    activeRecording ||
    activeLiveSession ||
    preparingLiveSession ||
    pendingLiveSession ||
    beginGate.isBusy() ||
    finishGate.isBusy() ||
    passkeyGate.isBusy()
  ) {
    fail("voice_turn_invalid");
  }
  if (
    strictCloudMinimization &&
    (serializedSessionState !== "" || pendingDocument)
  ) {
    fail("strict_privacy_blocked");
  }
  const beginToken = beginGate.acquire();
  if (beginToken === null) {
    fail("voice_turn_invalid");
  }
  const prepareStartedAt = performance.now();
  // A preparation observation is current-turn only. Clear the previous route
  // even when this turn deliberately uses strict or document HTTP handling and
  // therefore emits no Native preparation terminal event.
  dispatchVoicePrepareSloClear();
  const prepareGeneration =
    !strictCloudMinimization && !pendingDocument
      ? beginCurrentVoicePrepareSlo(
          VOICE_PREPARE_SLO_ROUTES.NATIVE_READY,
          prepareStartedAt,
        )
      : undefined;

  try {
    const sessionStatus = sessionClock.begin();
    if (!sessionStatus.ok) {
      stopSession(sessionStatus.expiry);
      fail("session_expired");
    }
    primeVoiceTransportConnection();
    // A new intentional/foreground turn owns a new content-free measurement.
    // Clear the previous value before any asynchronous permission or network
    // work so a stale fast result can never be shown for the current turn.
    dispatchVoiceStartLatency(null);

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
        // Provider preparation is content-free. A newly granted microphone
        // starts enabled in browsers, so mute it immediately while the exact
        // Native provider turn proves SetupComplete + StartActivity.
        setStreamTracksEnabled(stream, false);
        await ensureAudioGraph(stream, expectedEpoch);
        if (expectedEpoch !== sessionEpoch) {
          fail(stoppedSessionCode(expectedEpoch));
        }

        // The live capture graph is also attached only after the strong ready
        // boundary inside startVoiceLiveSession.
        // A continuing Respondent Coach can keep the low-latency Native input
        // path. Its caption is handed to the bounded coach controller; the
        // server must publish the exact authenticated checkpoint before response PCM.
        const nativeAudio =
          !strictCloudMinimization && !pendingDocument;
        const liveSession = await startVoiceLiveSession({
          ...credentials,
          coachActive,
          expectedEpoch,
          nativeAudio,
          sessionState: serializedSessionState,
          stream,
          strictCloudMinimization,
          turnMode,
        });
        if (expectedEpoch !== sessionEpoch) {
          const stopCode = stoppedSessionCode(expectedEpoch);
          liveSession?.cancel(new Error(stopCode));
          fail(stopCode);
        }
        // Listening starts here: strong Native ready has succeeded, or the
        // bounded preparation attempt has already selected HTTP fallback.
        setStreamTracksEnabled(stream, true);
        // Attach the privacy-gated PCM capture before arming VAD. A user may
        // start talking immediately after pressing the button; arming VAD
        // first could confirm speech while the AudioWorklet was still loading
        // and force the live turn to cancel after its first PCM frame.
        activeLiveSession = liveSession;
        const recording = createRecording(
          stream,
          nativeAudio,
          coachActive,
        );
        activeRecording = recording;
        if (prepareGeneration !== undefined) {
          completeCurrentVoicePrepareSlo(
            prepareGeneration,
            liveSession
              ? VOICE_PREPARE_SLO_RESULTS.READY
              : VOICE_PREPARE_SLO_RESULTS.FALLBACK,
            liveSession
              ? VOICE_PREPARE_SLO_ROUTES.NATIVE_READY
              : VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
          );
        }
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
  } catch (error) {
    if (prepareGeneration !== undefined) {
      cancelCurrentVoicePrepareSlo(prepareGeneration);
    }
    throw error;
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
    capture = await recording.turnEndedPromise;
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
    if (pendingLiveSession?.recording === recording) {
      // An interrupted turn may keep speaking into a bounded local handoff
      // while its exact Native provider turn prepares. Before Rust leaves the
      // preparation state, resolve that handoff or select HTTP fallback after
      // the existing 450 ms post-speech budget.
      await takePendingLiveSession(
        recording,
        recording.expectedEpoch,
      );
      if (
        recording.expectedEpoch !== sessionEpoch ||
        activeRecording !== recording
      ) {
        fail(stoppedSessionCode(recording.expectedEpoch));
      }
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

function hasValidAnswerProofMetadata(
  proof,
  assistanceTarget,
  respondentStage,
  coachPhase,
  coachAction,
) {
  if (proof === "none") return true;
  if (proof !== "question_bound_input_answer_first") return false;
  return (
    assistanceTarget === "respondent" &&
    respondentStage === "restructure" &&
    ((coachPhase === "complete" && coachAction === "complete") ||
      (coachPhase === "expanding" && coachAction === "expand"))
  );
}

function hasValidAnswerTransitionProofMetadata(
  proof,
  answerProof,
  assistanceTarget,
  respondentStage,
  coachPhase,
  coachAction,
) {
  if (proof === "none") return true;
  return (
    proof === "question_bound_input_clause_later_to_first" &&
    answerProof === "question_bound_input_answer_first" &&
    assistanceTarget === "respondent" &&
    respondentStage === "restructure" &&
    coachPhase === "complete" &&
    coachAction === "complete"
  );
}

const VOICE_RESPONSE_REQUIRED_KEYS = Object.freeze([
  "answerProof",
  "assistanceTarget",
  "audioBase64",
  "audioMimeType",
  "caption",
  "coachAction",
  "coachPhase",
  "detectedDomain",
  "needsPaper",
  "privacyStatus",
  "researchRecords",
  "researchStatus",
  "respondentStage",
  "route",
  "sessionState",
]);

function hasExactVoiceResponseKeys(payload) {
  const keys = Object.keys(payload).sort();
  const withTransition = keys.includes("answerTransitionProof");
  const expected = withTransition
    ? [...VOICE_RESPONSE_REQUIRED_KEYS, "answerTransitionProof"].sort()
    : VOICE_RESPONSE_REQUIRED_KEYS;
  return (
    keys.length === expected.length &&
    keys.every((key, index) => key === expected[index])
  );
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

function safeVoiceResponse(payload, expectedStrictCloudMinimization) {
  if (
    !isPlainRecord(payload) ||
    !hasExactVoiceResponseKeys(payload) ||
    typeof expectedStrictCloudMinimization !== "boolean"
  ) {
    fail("voice_response_invalid");
  }
  const hasAudio = payload.audioBase64 !== "";
  const answerProof =
    payload.answerProof === undefined ? "none" : payload.answerProof;
  const answerTransitionProof =
    payload.answerTransitionProof === undefined
      ? "none"
      : payload.answerTransitionProof;
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
    !hasValidAnswerProofMetadata(
      answerProof,
      payload.assistanceTarget,
      payload.respondentStage,
      payload.coachPhase,
      payload.coachAction,
    ) ||
    !hasValidAnswerTransitionProofMetadata(
      answerTransitionProof,
      answerProof,
      payload.assistanceTarget,
      payload.respondentStage,
      payload.coachPhase,
      payload.coachAction,
    ) ||
    (expectedStrictCloudMinimization && answerProof !== "none") ||
    (expectedStrictCloudMinimization && answerTransitionProof !== "none") ||
    (answerTransitionProof !== "none" &&
      (hasAudio ||
        payload.caption !== null ||
        (payload.audioMimeType !== "" && payload.audioMimeType !== undefined))) ||
    !boundedString(payload.route, 100) ||
    typeof payload.needsPaper !== "boolean" ||
    !["", "blocked", "clear"].includes(payload.privacyStatus) ||
    (expectedStrictCloudMinimization &&
      payload.privacyStatus !== "blocked" &&
      payload.privacyStatus !== "clear") ||
    (!expectedStrictCloudMinimization && payload.privacyStatus !== "") ||
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
  if (
    (payload.privacyStatus === "blocked" &&
      (hasAudio ||
        payload.audioMimeType !== "" ||
        payload.caption !== null ||
        payload.sessionState !== "" ||
        payload.detectedDomain !== "unknown" ||
        payload.assistanceTarget !== "assistant" ||
        payload.respondentStage !== "none" ||
        payload.coachPhase !== "none" ||
        payload.coachAction !== "none" ||
        answerProof !== "none" ||
        answerTransitionProof !== "none" ||
        research.status !== "none" ||
        research.records.length !== 0 ||
        payload.route !== "strict-privacy-blocked" ||
        payload.needsPaper)) ||
    (payload.privacyStatus === "clear" &&
      (payload.sessionState !== "" ||
        research.status !== "none" ||
        research.records.length !== 0))
  ) {
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
    answerProof,
    answerTransitionProof,
    needsPaper: payload.needsPaper,
    privacyStatus: payload.privacyStatus,
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
  coachActive = false,
  expectedEpoch,
  idToken,
  nativeAudio,
  sessionState,
  stream,
  strictCloudMinimization,
  turnMode,
}) {
  if (
    pendingDocument ||
    !liveVoiceSupported(stream) ||
    !liveCredential(appCheckToken) ||
    !liveCredential(idToken) ||
    typeof sessionState !== "string" ||
    sessionState.length > SESSION_STATE_MAX_CHARS ||
    typeof strictCloudMinimization !== "boolean" ||
    typeof nativeAudio !== "boolean" ||
    typeof coachActive !== "boolean" ||
    (nativeAudio && strictCloudMinimization) ||
    (coachActive && !nativeAudio) ||
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
      latencyProofVersion: 1,
      idToken,
      appCheckToken,
      nativeAudio,
      ...(nativeAudio ? { nativeCoachControl: true } : {}),
      sessionState,
      strictCloudMinimization,
      turnMode,
      sampleRateHz: VOICE_LIVE_LIMITS.inputSampleRateHz,
    });
    protocol = createVoiceLiveServerProtocol(
      (result) => safeVoiceResponse(result, strictCloudMinimization),
      { coachActive, nativeAudio },
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
      if (
        performance.now() - liveStartedAt >=
        VOICE_LIVE_LIMITS.readyTimeoutMs
      ) {
        failPreflight(new Error("voice_api_unavailable"));
        return;
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
  if (
    expectedEpoch !== sessionEpoch ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    detachPreflight();
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
    detachPreflight();
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
      detachPreflight();
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
  let speechEndToCommitSendMs;
  let speechEndToCommitAckMs;
  let commitToEstimatedAudibleMs;
  let commitToFirstAudioMs;
  let firstBinaryMs;
  let speechEndedAt;
  let speechEndToEstimatedAudibleMs;
  let speechConfirmed = captureHandoff !== undefined;
  let commitSent = false;
  let nativeFallbackAllowed = false;
  let nativeFallbackRequiresStatefulHTTP = false;
  let latencyDispatched = false;
  let slowReplayCandidateTimer;
  let slowStartStalled = false;
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
      substantiveAudio:
        Number.isFinite(commitToEstimatedAudibleMs),
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

  function clearSlowReplayCandidateTimer() {
    if (slowReplayCandidateTimer !== undefined) {
      clearTimeout(slowReplayCandidateTimer);
      slowReplayCandidateTimer = undefined;
    }
  }

  function failLive(error) {
    clearSlowReplayCandidateTimer();
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
          const snapshot = protocol.snapshot();
          nativeFallbackAllowed = shouldReplayCommittedNativeTurn({
            audioEventCount: snapshot.audioEventCount,
            coachActivated: snapshot.coachActivated,
            code: message.code,
            committed: state === "committed",
            interrupted: session?.playback?.interrupted === true,
            nativeAudio,
          });
          nativeFallbackRequiresStatefulHTTP =
            nativeFallbackAllowed && message.code === "voice_native_fallback";
          failLive(new Error(message.code));
          return;
        }
        if (message.type === "ready") {
          if (state !== "awaiting-ready") {
            fail("voice_response_invalid");
          }
          if (
            performance.now() - liveStartedAt >=
            VOICE_LIVE_LIMITS.readyTimeoutMs
          ) {
            failLive(new Error("voice_api_unavailable"));
            return;
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
        if (message.type === "coach") {
          if (state !== "committed" || !session.playback) {
            fail("voice_response_invalid");
          }
          session.playback.activateCoach();
          globalThis.dispatchEvent(
            new CustomEvent("kotae:coach-checkpoint", {
              detail: Object.freeze({
                assistanceTarget: message.assistanceTarget,
                coachAction: message.coachAction,
                coachPhase: message.coachPhase,
                respondentStage: message.respondentStage,
                route: message.route,
                sessionState: message.sessionState,
                version: 1,
              }),
            }),
          );
          return;
        }
        if (message.type === "committed") {
          if (state !== "committed" || !Number.isFinite(speechEndedAt) ||
            Number.isFinite(speechEndToCommitAckMs)) fail("voice_response_invalid");
          speechEndToCommitAckMs = Math.round(performance.now() - speechEndedAt);
          clientTransport.acknowledgeCommitted();
          return;
        }
        if (message.type === "final") {
          if (state !== "committed" || !session.playback) {
            fail("voice_response_invalid");
          }
          state = "final";
          finalResult = message.result;
          session.playback.finalReceived = true;
          if (!session.playback.interrupted) {
            session.playback.seal();
          }
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
      const audioEvent = protocol.acceptBinary(event.data);
      if (
        session.playback.interrupted &&
        session.playback.coachActive
      ) {
        // A confirmed interruption stops audible output immediately, but a
        // Native coach turn still needs its signed final state. Validate each
        // binary frame above, then wipe it instead of decoding or scheduling
        // it while the WebSocket drains to final + clean close.
        new Uint8Array(audioEvent.pcm).fill(0);
        return;
      }
      const audibleAt = session.playback.schedulePcm(audioEvent);
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

  function tryReplaySlowZeroAudioNativeStart() {
    const snapshot = protocol.snapshot();
    const replay = shouldReplayCommittedNativeTurn({
      audioEventCount: snapshot.audioEventCount,
      coachActivated: snapshot.coachActivated,
      code: "voice_api_unavailable",
      committed: state === "committed",
      interrupted: session?.playback?.interrupted === true,
      nativeAudio,
    });
    if (!replay) return false;
    nativeFallbackAllowed = true;
    nativeFallbackRequiresStatefulHTTP = false;
    failLive(new Error("voice_api_unavailable"));
    return true;
  }

  function recoverSlowVoiceStart(stalled = false) {
    slowStartStalled ||= stalled;
    const interruptRecording = session?.playback?.interruptRecording;
    if (
      interruptRecording?.candidate &&
      !interruptRecording.settled
    ) {
      if (slowReplayCandidateTimer === undefined) {
        slowReplayCandidateTimer = setTimeout(() => {
          slowReplayCandidateTimer = undefined;
          recoverSlowVoiceStart();
        }, VAD_INTERVAL_MS);
      }
      return;
    }
    if (tryReplaySlowZeroAudioNativeStart()) return;
    if (
      slowStartStalled &&
      state === "committed" &&
      session?.playback &&
      !session.playback.hasStreamedAudio() &&
      session?.playback?.interrupted !== true
    ) {
      // Silent/sub-threshold PCM and an already bound Coach checkpoint both
      // forbid replay, but neither may masquerade as an audible response.
      failLive(new Error("voice_turn_timeout"));
    }
  }

  session = {
    nativeAudio,
    playback: undefined,
    canFallback() {
      return !commitSent || nativeFallbackAllowed;
    },
    requiresStatefulHTTPFallback() {
      // A deterministic Native routing sentinel can carry the turn into the
      // staged coach path. A zero-audio provider outage may replay once too,
      // but it must not pre-activate coach semantics on the HTTP response.
      return nativeFallbackRequiresStatefulHTTP;
    },
    requiresStatefulLiveDrain() {
      return Boolean(
        state === "committed" &&
          session.playback?.coachActive === true &&
          session.playback.finalReceived === false,
      );
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
        coachActive: session.playback.coachActive,
        expectedEpoch,
        idToken,
        nativeAudio,
        sessionState,
        stream: nextStream,
        strictCloudMinimization,
        turnMode: "foreground",
      });
    },
    matches(
      expectedSessionState,
      expectedTurnMode,
      expectedStrictCloudMinimization,
    ) {
      return (
        expectedSessionState === sessionState &&
        expectedTurnMode === turnMode &&
        expectedStrictCloudMinimization === strictCloudMinimization
      );
    },
    cancel(error = new Error("request_cancelled")) {
      clearSlowReplayCandidateTimer();
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
      speechEndToCommitSendMs = Number.isFinite(speechEndedAt)
        ? Math.round(commitAt - speechEndedAt)
        : undefined;
      playback.armResponseInterruption(commitAt, {
        onMiss: recoverSlowVoiceStart,
        onStall: () => recoverSlowVoiceStart(true),
      });

      const result = await resultPromise;
      const snapshot = protocol.snapshot();
      return finalizeMeaningfulVoiceStream(
        playback,
        result,
        snapshot.audioEventCount,
      );
    },
    interrupt(error = new Error("voice_interrupted")) {
      clearSlowReplayCandidateTimer();
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
      clearSlowReplayCandidateTimer();
      emitLatency();
    },
    publishFirstAudible(audibleAt) {
      if ((state !== "committed" && state !== "final") ||
        !Number.isFinite(audibleAt) || !Number.isFinite(speechEndedAt) ||
        !Number.isSafeInteger(speechEndToCommitSendMs) ||
        !Number.isSafeInteger(speechEndToCommitAckMs)) return;
      const total = Math.round(audibleAt - speechEndedAt);
      if (total < speechEndToCommitAckMs || total > 10_000) return;
      try {
        clientTransport.publishLatencyProof({
          speechEndToCommitAckMs,
          speechEndToCommitSendMs,
          speechEndToEstimatedAudibleMs: total,
        });
      } catch {
        // Content-free diagnostics never change answer delivery or fallback.
      }
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
  // Keep the preflight listeners attached until the complete session listener
  // set is installed. Provider ready may arrive in the synchronous gap between
  // worklet loading and session construction; swapping listeners in this order
  // makes that frame impossible to lose.
  detachPreflight();
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
  if (preparingLiveSession) {
    session.cancel(new Error("voice_turn_invalid"));
    return undefined;
  }
  preparingLiveSession = session;
  try {
    // For a Native turn the server's ready frame is the strong input gate:
    // authentication, quota, UID lease, provider SetupComplete, and
    // StartActivity have all succeeded. Do not connect the capture graph or
    // let Rust publish Listening while any of those boundaries are pending.
    await readyPromise;
  } catch {
    if (preparingLiveSession === session) {
      preparingLiveSession = undefined;
    }
    return undefined;
  }
  if (preparingLiveSession === session) {
    preparingLiveSession = undefined;
  }
  if (
    expectedEpoch !== sessionEpoch ||
    state !== "ready" ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    session.cancel(new Error(stoppedSessionCode(expectedEpoch)));
    fail(stoppedSessionCode(expectedEpoch));
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
  if (!recording.turnEnded) {
    recording.turnEnded = true;
    recording.rejectTurnEnded?.(new Error("request_cancelled"));
  }
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

function discardInterruptedPlaybackRecording(playback) {
  if (!playback?.interrupted) return;
  const recording = playback.interruptRecording;
  playback.interruptRecording = undefined;
  playback.resetInterruptGuard = undefined;
  if (activeRecording === recording) {
    activeRecording = undefined;
  }
  if (recording && !recording.settled) {
    abandonInterruptRecording(recording);
  }
  setTracksEnabled(false);
}

function rampPlaybackGain(playback, target, seconds) {
  if (
    !playback?.gainNode ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    return false;
  }
  const gain = playback.gainNode.gain;
  const now = audioContext.currentTime;
  try {
    gain.cancelScheduledValues(now);
    gain.setValueAtTime(gain.value, now);
    gain.linearRampToValueAtTime(target, now + seconds);
    return true;
  } catch {
    try {
      gain.value = target;
      return gain.value === target;
    } catch {
      return false;
    }
  }
}

function restorePlaybackGain(playback) {
  return rampPlaybackGain(playback, 1, 0.02);
}

function scheduleEchoProbeGain(playback) {
  if (
    !playback?.gainNode ||
    !audioContext ||
    audioContext.state === "closed"
  ) {
    return false;
  }
  const gain = playback.gainNode.gain;
  const now = audioContext.currentTime;
  const muteAt =
    now + INTERRUPT_ECHO_PROBE_LIMITS.muteRampMs / 1_000;
  const restoreAt =
    now + INTERRUPT_ECHO_PROBE_LIMITS.probeTimeoutMs / 1_000;
  try {
    // Reserve the recovery on the audio rendering timeline before muting.
    // It still runs if the main thread stalls and cannot service the VAD tick.
    gain.cancelScheduledValues(now);
    gain.setValueAtTime(gain.value, now);
    gain.linearRampToValueAtTime(0, muteAt);
    gain.setValueAtTime(0, restoreAt);
    gain.linearRampToValueAtTime(
      1,
      restoreAt + INTERRUPT_ECHO_PROBE_LIMITS.muteRampMs / 1_000,
    );
    return true;
  } catch {
    restorePlaybackGain(playback);
    return false;
  }
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
  verificationStillValid,
) {
  const verificationRemainsValid = () => {
    if (typeof verificationStillValid !== "function") return false;
    try {
      return verificationStillValid() === true;
    } catch {
      return false;
    }
  };
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
    !verificationRemainsValid() ||
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
    !hasVerifiedEchoCancellation(stream) ||
    !verificationRemainsValid()
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
        !verificationRemainsValid() ||
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
        mediaStream !== stream ||
        !hasVerifiedEchoCancellation(stream) ||
        !verificationRemainsValid() ||
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
          !Number.isSafeInteger(cutoffContextFrame) ||
          !hasVerifiedEchoCancellation(stream) ||
          !verificationRemainsValid()
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

function dispatchVoiceInterruptionReady(route) {
  if (
    route !== VOICE_PREPARE_SLO_ROUTES.NATIVE_READY &&
    route !== VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK
  ) {
    fail("voice_response_invalid");
  }
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-interruption-ready", {
      detail: Object.freeze({ route, version: 1 }),
    }),
  );
}

function publishPendingInterruptionReady(pending, route) {
  if (
    !pending ||
    pending.readyPublished ||
    pending.expectedEpoch !== sessionEpoch ||
    pending.recording !== activeRecording
  ) {
    return false;
  }
  pending.readyPublished = true;
  dispatchVoiceInterruptionReady(route);
  return true;
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
  if (
    pending.preparingSession &&
    preparingLiveSession === pending.preparingSession
  ) {
    const preparing = pending.preparingSession;
    preparingLiveSession = undefined;
    preparing.cancel(error);
  }
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
    publishPendingInterruptionReady(
      pending,
      VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
    );
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
    if (!nextLiveSession && !pending.retired) {
      publishPendingInterruptionReady(
        pending,
        VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
      );
    }
    nextLiveSession?.cancel(
      new Error(stoppedSessionCode(expectedEpoch)),
    );
    return undefined;
  }
  activeLiveSession = nextLiveSession;
  publishPendingInterruptionReady(
    pending,
    VOICE_PREPARE_SLO_ROUTES.NATIVE_READY,
  );
  return nextLiveSession;
}

function shouldAbortPlaybackTransportOnInterrupt(playback) {
  if (
    !playback ||
    typeof playback.finalReceived !== "boolean" ||
    typeof playback.coachActive !== "boolean" ||
    (playback.transportKind !== "http" &&
      playback.transportKind !== "live")
  ) {
    fail("voice_response_invalid");
  }
  // Only an explicitly scoped Respondent Coach response may need the newly
  // signed state in its final event. Other HTTP turns abort immediately so a
  // PDF, strict, or ordinary fallback response cannot delay the interruption.
  if (playback.transportKind === "http") {
    return (
      shouldAbortVoiceTransportOnInterrupt(playback.finalReceived) &&
      !playback.coachActive
    );
  }
  // A first-turn Native response can become Respondent Coach only after the
  // server has inspected its authenticated input caption. Once the exact
  // coach control arrives, preserve this WebSocket through final + clean
  // close so its newly signed state cannot be lost on barge-in.
  return (
    shouldAbortVoiceTransportOnInterrupt(playback.finalReceived) &&
    !playback.coachActive
  );
}

function maybeAbortPlaybackTransportOnInterrupt(
  playback,
  requestController,
) {
  const shouldAbort = shouldAbortPlaybackTransportOnInterrupt(playback);
  if (shouldAbort && requestController) {
    requestController.abort();
  }
  return shouldAbort;
}

function shouldDiscardInterruptedPlaybackRecording(playback) {
  if (
    !playback?.interrupted ||
    (playback.transportKind !== "http" &&
      playback.transportKind !== "live")
  ) {
    return false;
  }
  // A prompt, non-stateful pre-final abort owns the held recording and hands
  // it back to Rust. Any failure on a state-preserving HTTP or live coach
  // drain must discard it because no validated final state exists to bind the
  // next turn.
  return !(
    playback.interruptedBeforeFinal &&
    shouldAbortPlaybackTransportOnInterrupt(playback)
  );
}

function confirmBargeIn(
  playback,
  recording,
  candidate,
  expectedEpoch,
) {
  if (
    playback.hasCommittedResponse?.() !== true ||
    playback.interrupted ||
    playback !== activePlayback ||
    playback.interruptRecording !== recording ||
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
    playback.interruptRecording !== recording ||
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
  playback.interruptedBeforeFinal = !playback.finalReceived;
  playback.interrupted = true;
  activeRecording = recording;

  const interruption = new Error("voice_interrupted");
  const interruptionStartedAt = Number.isFinite(
    recording.interruptOnsetAt,
  )
    ? recording.interruptOnsetAt
    : performance.now();
  const interruptedLiveSession = activeLiveSession;
  const preserveLiveCoachState = Boolean(
    !playback.finalReceived &&
      interruptedLiveSession?.requiresStatefulLiveDrain() === true,
  );
  const handoffEpoch = sessionEpoch;
  if (
    !preserveLiveCoachState &&
    activeLiveSession === interruptedLiveSession
  ) {
    activeLiveSession = undefined;
  }
  const handoffPromise =
    !preserveLiveCoachState &&
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
  const handoffPreparingSession = handoffPromise
    ? preparingLiveSession
    : undefined;
  if (!handoffPromise) {
    captureHandoff?.stop();
    if (!preserveLiveCoachState) {
      interruptedLiveSession?.interrupt(interruption);
    }
  }
  // Normal live playback retains its low-latency handoff. Stateful HTTP and
  // dynamically activated Native coach playback halt audio now but keep their
  // response transport alive through a validated final + clean termination.
  maybeAbortPlaybackTransportOnInterrupt(
    playback,
    activeRequestController,
  );
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
      preparingSession: handoffPreparingSession,
      promise: Promise.resolve(handoffPromise),
      readyPublished: false,
      recording,
      retired: false,
    };
    pendingLiveSession = pending;
    void pending.promise
      .then((nextLiveSession) => {
        if (!nextLiveSession) {
          captureHandoff?.stop();
          if (pendingLiveSession === pending) {
            publishPendingInterruptionReady(
              pending,
              VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
            );
            pendingLiveSession = undefined;
          }
          return;
        }
        if (pendingLiveSession !== pending || pending.retired) {
          nextLiveSession.cancel(new Error("request_cancelled"));
          return;
        }
        if (recording.settled) {
          publishPendingInterruptionReady(
            pending,
            VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
          );
          if (pendingLiveSession === pending) {
            pendingLiveSession = undefined;
          }
          captureHandoff?.stop();
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
          publishPendingInterruptionReady(
            pending,
            VOICE_PREPARE_SLO_ROUTES.NATIVE_READY,
          );
          if (pendingLiveSession === pending) {
            pendingLiveSession = undefined;
          }
        } else {
          publishPendingInterruptionReady(
            pending,
            VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
          );
          if (pendingLiveSession === pending) {
            pendingLiveSession = undefined;
          }
          captureHandoff?.stop();
          nextLiveSession.cancel(new Error("request_cancelled"));
        }
      })
      .catch(() => {
        captureHandoff?.stop();
        if (pendingLiveSession === pending) {
          publishPendingInterruptionReady(
            pending,
            VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK,
          );
          pendingLiveSession = undefined;
        }
        // The MediaRecorder candidate remains the ambient HTTP fallback.
      });
  }
  globalThis.dispatchEvent(
    new CustomEvent("kotae:voice-interrupted", {
      detail: Object.freeze({
        finalReceived: playback.finalReceived,
        preparing: Boolean(handoffPromise),
        version: 1,
      }),
    }),
  );
}

function startBargeInMonitoring(playback, expectedEpoch, guardStartedAt) {
  if (
    playback.interruptRecording ||
    playback.interrupted ||
    expectedEpoch !== sessionEpoch ||
    !Number.isFinite(guardStartedAt) ||
    guardStartedAt < 0 ||
    !analyser ||
    !hasLiveAudioTrack(mediaStream)
  ) {
    return;
  }
  const sessionStatus = sessionClock.check();
  if (!sessionClock.isStarted() || !sessionStatus.ok) {
    stopSession(sessionStatus.expiry ?? "maximum");
    fail("session_expired");
  }

  setTracksEnabled(true);
  const recording = createRecordingState(
    mediaStream,
    playback.nativeAudio === true,
    playback.coachActive === true,
  );
  const pcm = new Float32Array(analyser.fftSize);
  recording.vadPcm = pcm;
  let echoCancellationVerified = hasVerifiedEchoCancellation(mediaStream);
  let echoProbe;
  let unverifiedOutputContaminated = false;
  let vadState = createInterruptVadState(guardStartedAt);
  playback.interruptRecording = recording;
  if (echoCancellationVerified) {
    // Raw provisional PCM is available only when the browser proves AEC.
    // Unverified devices retain the bounded MediaRecorder/HTTPS fallback.
    void startBargePcmMonitoring(
      playback,
      recording,
      expectedEpoch,
      () => echoCancellationVerified,
    );
  }
  playback.resetInterruptGuard = (nextAudibleAt) => {
    if (
      Number.isFinite(nextAudibleAt) &&
      nextAudibleAt >= 0 &&
      !recording.settled &&
      !recording.candidate &&
      vadState.phase !== "confirmed"
    ) {
      if (echoProbe) {
        echoProbe = undefined;
        restorePlaybackGain(playback);
      }
      unverifiedOutputContaminated = false;
      vadState = createInterruptVadState(nextAudibleAt);
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
    const now = performance.now();
    if (now < vadState.startedAt) return;
    const rawOutputActive =
      playback.hasStreamedAudio() && playback.sources.size > 0;
    if (
      echoCancellationVerified &&
      !hasVerifiedEchoCancellation(mediaStream)
    ) {
      echoCancellationVerified = false;
      if (recording.candidate) {
        unverifiedOutputContaminated = true;
      }
      playback.bargePcmMonitor?.stop();
    }
    if (
      echoProbe &&
      (now >= echoProbe.expiresAt ||
        !recording.candidate ||
        !["candidate", "provisional"].includes(vadState.phase))
    ) {
      const candidate = recording.candidate;
      echoProbe = undefined;
      unverifiedOutputContaminated = false;
      recording.interruptOnsetAt = undefined;
      restorePlaybackGain(playback);
      vadState = createInterruptVadState(now);
      if (
        candidate &&
        !discardCurrentCandidate(recording, "interrupt-probe-timeout")
      ) {
        abandonInterruptRecording(recording);
      }
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
    const probeCleanWindow = Boolean(
      echoProbe && now >= echoProbe.tailUntil,
    );
    if (
      probeCleanWindow &&
      echoProbe.proofBaselineVoiceMs === undefined
    ) {
      echoProbe.proofBaselineVoiceMs = vadState.voiceRunMs;
    }
    const postMuteProofReady = Boolean(
      probeCleanWindow &&
        vadState.voiceRunMs - echoProbe.proofBaselineVoiceMs >=
          INTERRUPT_ECHO_PROBE_LIMITS.postMuteProofMs,
    );
    vadState = advanceInterruptVad(
      vadState,
      {
        now,
        outputActive: rawOutputActive && !probeCleanWindow,
        peak,
        rms: Math.sqrt(sumSquares / pcm.length),
      },
      {
        confirmationAllowed:
          echoCancellationVerified ||
          (!unverifiedOutputContaminated && !rawOutputActive) ||
          postMuteProofReady,
        confirmationProofSatisfied: postMuteProofReady,
      },
    );
    if (
      !echoCancellationVerified &&
      rawOutputActive &&
      ["candidate", "provisional"].includes(vadState.phase)
    ) {
      unverifiedOutputContaminated = true;
    }
    if (
      unverifiedOutputContaminated &&
      !echoProbe &&
      recording.candidate &&
      vadState.phase === "provisional" &&
      vadState.voiceRunMs >= INTERRUPT_VAD_LIMITS.confirmationMs
    ) {
      if (!scheduleEchoProbeGain(playback)) {
        recording.interruptOnsetAt = undefined;
        unverifiedOutputContaminated = false;
        restorePlaybackGain(playback);
        vadState = createInterruptVadState(now);
        if (
          !discardCurrentCandidate(recording, "interrupt-probe-unavailable")
        ) {
          abandonInterruptRecording(recording);
        }
        return;
      }
      echoProbe = {
        expiresAt:
          now + INTERRUPT_ECHO_PROBE_LIMITS.probeTimeoutMs,
        proofBaselineVoiceMs: undefined,
        tailUntil:
          now +
          INTERRUPT_ECHO_PROBE_LIMITS.muteRampMs +
          INTERRUPT_ECHO_PROBE_LIMITS.speakerTailMs,
      };
    }
    recording.firstVoiceAt = vadState.firstVoiceAt;
    const hadConfirmedSpeech = recording.vadHasSpeech;
    recording.vadHasSpeech = vadState.phase === "confirmed";
    if (!hadConfirmedSpeech && recording.vadHasSpeech) {
      globalThis.dispatchEvent(
        new CustomEvent("kotae:voice-input-confirmed", {
          detail: Object.freeze({ version: 1 }),
        }),
      );
    }
    if (Number.isFinite(vadState.lastVoiceAt)) {
      recording.lastVoiceAt = vadState.lastVoiceAt;
    }
    updateVoiceReceipt(recording, now);
    if (maybeCommitHybridEndpoint(recording, now)) {
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
    } else if (vadState.action === "provisional") {
      // Keep the response fully audible until sustained foreground speech
      // passes the hard interruption gate.
    } else if (vadState.action === "discard") {
      recording.interruptOnsetAt = undefined;
      echoProbe = undefined;
      unverifiedOutputContaminated = false;
      restorePlaybackGain(playback);
      if (!discardCurrentCandidate(recording, "interrupt-rejected")) {
        abandonInterruptRecording(recording);
      }
    } else if (vadState.action === "confirm") {
      if (echoProbe) {
        echoProbe = undefined;
        restorePlaybackGain(playback);
      }
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

function createStreamingPlayback(
  expectedEpoch,
  nativeAudio = false,
  transportKind = "live",
  coachActive = false,
  speechEndedAt = undefined,
  strictLocal = false,
  onFirstAudible = undefined,
) {
  if (
    activePlayback ||
    !audioContext ||
    audioContext.state === "closed" ||
    typeof nativeAudio !== "boolean" ||
    typeof coachActive !== "boolean" ||
    typeof strictLocal !== "boolean" ||
    (onFirstAudible !== undefined &&
      typeof onFirstAudible !== "function") ||
    (speechEndedAt !== undefined &&
      (!Number.isFinite(speechEndedAt) || speechEndedAt < 0)) ||
    (transportKind !== "http" && transportKind !== "live") ||
    (nativeAudio && transportKind !== "live")
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
  let responseCommitted = false;
  let responseStartedAt;
  let firstAudiblePending = false;
  let firstAudibleTimer;
  const coachInitiallyActive = coachActive;
  const sloGeneration = nextVoiceStartSloGeneration();
  let sloRoute = classifyVoiceStartSloRoute({
    coachActive,
    coachInitiallyActive,
    nativeAudio,
    strictLocal,
    transportKind,
  });
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

  function clearFirstAudibleTimer() {
    if (firstAudibleTimer !== undefined) {
      clearTimeout(firstAudibleTimer);
      firstAudibleTimer = undefined;
    }
  }

  function publishFirstAudible(audibleAt, event) {
    if (!firstAudiblePending || streamedAudio) return;
    if (
      Number.isFinite(responseStartedAt) &&
      audibleAt >=
        responseStartedAt + VOICE_START_SLO_BUDGETS.stalledMs
    ) {
      // The speech-end hard wall owns this boundary. A throttled timer or an
      // `ended` callback may observe the source later, but cannot turn a
      // post-deadline slot into a successful start.
      return;
    }
    firstAudiblePending = false;
    clearFirstAudibleTimer();
    streamedAudio = true;
    onFirstAudible?.(audibleAt);
    advanceCurrentVoiceStartSlo(
      sloGeneration,
      true,
      audibleAt,
    );
    playback.armResponseInterruption(audibleAt);
    globalThis.dispatchEvent(
      new CustomEvent("kotae:first-audio", {
        detail: Object.freeze({ sequence: event.sequence, version: 1 }),
      }),
    );
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
    const ownsFirstAudible =
      !streamedAudio &&
      !firstAudiblePending &&
      Number.isFinite(audibleAt);
    pendingSources += 1;
    sources.add(source);
    source.addEventListener(
      "ended",
      () => {
        if (ownsFirstAudible) {
          // Background timer throttling cannot make a source that already
          // played disappear from the first-audible proof.
          publishFirstAudible(audibleAt, event);
        }
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

    if (ownsFirstAudible) {
      firstAudiblePending = true;
      const audibleDelayMs = Math.max(
        0,
        audibleAt - performance.now(),
      );
      const beforeAbsoluteStall =
        !Number.isFinite(responseStartedAt) ||
        audibleAt <
          responseStartedAt + VOICE_START_SLO_BUDGETS.stalledMs;
      if (beforeAbsoluteStall) {
        if (
          audibleDelayMs <= VOICE_START_AUDIBLE_COMMIT_LOOKAHEAD_MS
        ) {
          // Web Audio now owns a near-term output slot. Longer queued silence
          // keeps the SLO live until the real audible boundary or hard stall.
          publishFirstAudible(audibleAt, event);
        } else {
          firstAudibleTimer = setTimeout(
            () => publishFirstAudible(audibleAt, event),
            audibleDelayMs,
          );
        }
      }
    }
    return audibleAt;
  }

  playback = {
    activateCoach() {
      if (
        transportKind !== "live" ||
        streamedAudio ||
        playback.finalReceived ||
        settled ||
        sealed
      ) {
        fail("voice_response_invalid");
      }
      playback.coachActive = true;
      sloRoute = classifyVoiceStartSloRoute({
        coachActive: true,
        coachInitiallyActive,
        nativeAudio,
        strictLocal,
        transportKind,
      });
      updateCurrentVoiceStartSloRoute(sloGeneration, sloRoute);
      if (
        playback.interruptRecording &&
        !playback.interruptRecording.settled
      ) {
        playback.interruptRecording.coachActive = true;
      }
    },
    armResponseInterruption(
      guardStartedAt,
      { onMiss, onStall } = {},
    ) {
      if (
        !Number.isFinite(guardStartedAt) ||
        guardStartedAt < 0 ||
        (onMiss !== undefined && typeof onMiss !== "function") ||
        (onStall !== undefined && typeof onStall !== "function") ||
        settled ||
        (sealed && !responseCommitted) ||
        expectedEpoch !== sessionEpoch ||
        (Number.isFinite(speechEndedAt) &&
          speechEndedAt > guardStartedAt)
      ) {
        fail("voice_response_invalid");
      }
      if (!responseCommitted) {
        responseCommitted = true;
        responseStartedAt = Number.isFinite(speechEndedAt)
          ? speechEndedAt
          : guardStartedAt;
        beginCurrentVoiceStartSlo({
          generation: sloGeneration,
          onMiss,
          onStall,
          operationalStartedAt: guardStartedAt,
          route: sloRoute,
          startedAt: responseStartedAt,
        });
      } else if (onMiss !== undefined || onStall !== undefined) {
        fail("voice_response_invalid");
      }
      if (playback.interruptRecording) {
        playback.resetInterruptGuard?.(guardStartedAt);
      } else {
        startBargeInMonitoring(
          playback,
          expectedEpoch,
          guardStartedAt,
        );
      }
    },
    bargePcmMonitor: undefined,
    completion,
    drainTimeoutMs() {
      return validatedPlaybackDrainTimeoutMs({
        currentContextTime: playbackContext.currentTime,
        scheduledEndContextTime: nextStartAt,
      });
    },
    coachActive,
    finalReceived: false,
    gainNode,
    nativeAudio,
    sloGeneration,
    transportKind,
    hasCommittedResponse: () => responseCommitted,
    hasPendingFirstAudible: () => firstAudiblePending,
    hasStreamedAudio: () => streamedAudio,
    interruptRecording: undefined,
    interrupted: false,
    interruptedBeforeFinal: false,
    reject(error) {
      if (settled) return;
      settled = true;
      firstAudiblePending = false;
      clearFirstAudibleTimer();
      cancelCurrentVoiceStartSlo(sloGeneration);
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
      if (!firstAudiblePending) {
        cancelCurrentVoiceStartSlo(sloGeneration);
      }
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

function finalizeMeaningfulVoiceStream(
  playback,
  finalResult,
  audioEventCount,
) {
  if (
    !playback ||
    typeof playback.hasPendingFirstAudible !== "function" ||
    typeof playback.hasStreamedAudio !== "function" ||
    !Number.isSafeInteger(audioEventCount) ||
    audioEventCount < 0
  ) {
    fail("voice_response_invalid");
  }
  const pendingFirstAudible =
    playback.hasPendingFirstAudible() === true;
  const streamedAudio = playback.hasStreamedAudio() === true;
  if (
    (streamedAudio && audioEventCount === 0) ||
    (pendingFirstAudible && (streamedAudio || audioEventCount === 0))
  ) {
    fail("voice_response_invalid");
  }
  if (
    !streamedAudio &&
    !pendingFirstAudible &&
    audioEventCount > 0 &&
    !playback.interrupted
  ) {
    // A syntactically valid PCM stream can still be entirely silent. Treat it
    // as the existing recoverable no-reply failure instead of reporting that
    // the assistant spoke when no meaningful sample reached playback.
    fail("voice_turn_unavailable");
  }
  return Object.freeze({
    finalResult,
    streamedAudio: audioEventCount > 0,
  });
}

async function consumeVoiceStream(
  response,
  playback,
  expectedEpoch,
  expectedStrictCloudMinimization,
) {
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
    safeVoiceResponse(result, expectedStrictCloudMinimization),
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
        if (
          playback.interrupted &&
          playback.transportKind === "http"
        ) {
          // createVoiceStreamParser has already enforced sequence, canonical
          // base64 shape, per-event size, and total byte bounds. Do not decode,
          // schedule, or fire another first-audio event after confirmed barge.
          continue;
        }
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
    const finalized = finalizeMeaningfulVoiceStream(
      playback,
      completed.finalResult,
      completed.audioEventCount,
    );
    if (!playback.interrupted) {
      playback.seal();
    }
    return finalized;
  } catch (error) {
    void reader.cancel(error).catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

async function finishTurn(
  serializedSessionState,
  turnMode,
  strictCloudMinimization,
) {
  const recording = activeRecording;
  if (!recording || finishGate.isBusy()) {
    fail("voice_turn_invalid");
  }
  if (
    typeof serializedSessionState !== "string" ||
    serializedSessionState.length > SESSION_STATE_MAX_CHARS ||
    !isValidTurnMode(turnMode) ||
    typeof strictCloudMinimization !== "boolean" ||
    (strictCloudMinimization &&
      (serializedSessionState !== "" || pendingDocument))
  ) {
    fail("voice_turn_invalid");
  }

  const finishToken = finishGate.acquire();
  if (finishToken === null) {
    fail("voice_turn_invalid");
  }
  const expectedEpoch = sessionEpoch;
  let audioBase64 = "";
  let capture;
  let documentForTurn;
  let liveSession;
  let playback;
  let coachActive = recording.coachActive === true;
  let requestController;
  let responseClockActive = false;
  let turnTimedOut = false;
  let voiceStartCancelOwner;
  let voiceStartDeadlineActive = false;
  let voiceStartDeadlineReject;
  let voiceStartDeadlineTimer;
  let voiceStartTimedOut = false;
  const voiceStartDeadlinePromise = new Promise((_, reject) => {
    voiceStartDeadlineReject = reject;
  });
  void voiceStartDeadlinePromise.catch(() => {});
  const turnDeadlineAt =
    performance.now() + VOICE_TURN_CLIENT_TIMEOUT_MS;

  function disarmVoiceStartDeadline(audibleAt) {
    if (Number.isFinite(audibleAt)) {
      liveSession?.publishFirstAudible(audibleAt);
    }
    voiceStartDeadlineActive = false;
    if (voiceStartDeadlineTimer !== undefined) {
      clearTimeout(voiceStartDeadlineTimer);
      voiceStartDeadlineTimer = undefined;
    }
  }

  function armVoiceStartDeadline(speechEndedAt) {
    const now = performance.now();
    if (
      !Number.isFinite(speechEndedAt) ||
      speechEndedAt < 0 ||
      speechEndedAt > now
    ) {
      fail("voice_turn_invalid");
    }
    voiceStartDeadlineActive = true;
    voiceStartDeadlineTimer = setTimeout(() => {
      voiceStartDeadlineTimer = undefined;
      if (!voiceStartDeadlineActive) return;
      voiceStartDeadlineActive = false;
      voiceStartTimedOut = true;
      if (
        Number.isSafeInteger(playback?.sloGeneration) &&
        playback.hasStreamedAudio?.() === false
      ) {
        // The master timer was registered before transport/playback timers.
        // Publish the current generation's exact stall boundary before
        // cancellation can revoke its controller ownership.
        try {
          advanceCurrentVoiceStartSlo(
            playback.sloGeneration,
            false,
            speechEndedAt + VOICE_START_SLO_BUDGETS.stalledMs,
          );
        } catch {
          // Telemetry or a nested recovery failure cannot revoke the hard
          // cancellation and single public timeout below.
        }
      }
      const owner = voiceStartCancelOwner;
      voiceStartCancelOwner = undefined;
      try {
        owner?.cancel?.();
      } catch {
        // The single timeout error below remains the public reason.
      }
      voiceStartDeadlineReject(new Error("voice_turn_timeout"));
    }, Math.max(
      0,
      speechEndedAt + VOICE_START_SLO_BUDGETS.stalledMs - now,
    ));
  }

  async function awaitVoiceTurnResult(promise, cancel) {
    if (voiceStartTimedOut) {
      try {
        cancel?.();
      } finally {
        throw new Error("voice_turn_timeout");
      }
    }
    const remainingMs = Math.max(0, turnDeadlineAt - performance.now());
    let timeout;
    const cancelOwner = Object.freeze({ cancel });
    if (voiceStartDeadlineActive) {
      voiceStartCancelOwner = cancelOwner;
    }
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
      return await Promise.race([
        promise,
        deadline,
        ...(voiceStartDeadlineActive
          ? [voiceStartDeadlinePromise]
          : []),
      ]);
    } finally {
      clearTimeout(timeout);
      if (voiceStartCancelOwner === cancelOwner) {
        voiceStartCancelOwner = undefined;
      }
    }
  }

  async function awaitVoiceStartDeadlineResult(promise, cancel) {
    if (voiceStartTimedOut) {
      try {
        cancel?.();
      } finally {
        throw new Error("voice_turn_timeout");
      }
    }
    if (!voiceStartDeadlineActive) {
      return promise;
    }
    const cancelOwner = Object.freeze({ cancel });
    voiceStartCancelOwner = cancelOwner;
    try {
      // Validated playback is deliberately outside the 60 second network
      // deadline, but it remains inside the speech-end +10 second start proof
      // until the first meaningful output slot disarms that proof.
      return await Promise.race([promise, voiceStartDeadlinePromise]);
    } finally {
      if (voiceStartCancelOwner === cancelOwner) {
        voiceStartCancelOwner = undefined;
      }
    }
  }
  try {
    if (!recording.stopLatch.isRequested()) {
      requestRecordingStop(recording, "manual");
    }
    const turnEnd = await recording.turnEndedPromise;
    if (!turnEnd.hasSpeech) {
      fail("no_speech");
    }
    if (!beginSessionResponse(expectedEpoch)) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    responseClockActive = true;
    armVoiceStartDeadline(recording.lastVoiceAt);

    documentForTurn = pendingDocument;
    if (documentForTurn) {
      clearPendingDocument("consumed");
    }
    liveSession = activeLiveSession;
    if (!liveSession) {
      liveSession = await awaitVoiceTurnResult(
        takePendingLiveSession(recording, expectedEpoch),
        () =>
          retirePendingLiveSession(
            new Error("voice_turn_timeout"),
          ),
      );
    }
    if (
      liveSession &&
      !liveSession.matches(
        serializedSessionState,
        turnMode,
        strictCloudMinimization,
      )
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
        await awaitVoiceTurnResult(audioContext.resume());
      }
      if (expectedEpoch !== sessionEpoch) {
        fail(stoppedSessionCode(expectedEpoch));
      }
      playback = createStreamingPlayback(
        expectedEpoch,
        liveSession.nativeAudio === true,
        "live",
        coachActive,
        recording.lastVoiceAt,
        strictCloudMinimization,
        disarmVoiceStartDeadline,
      );
      try {
        const completed = await awaitVoiceTurnResult(
          liveSession.commit(
            playback,
            recording.lastVoiceAt,
          ),
          () => liveSession.cancel(new Error("voice_turn_timeout")),
        );
        await awaitVoiceStartDeadlineResult(
          awaitValidatedPlaybackCompletion(
            playback,
            expectedEpoch,
          ),
          () =>
            haltStreamingPlayback(
              playback,
              new Error("voice_turn_timeout"),
            ),
        );
        if (
          completed.streamedAudio &&
          !playback.hasStreamedAudio() &&
          !playback.interrupted
        ) {
          fail("voice_turn_timeout");
        }
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
          voiceStartTimedOut ||
          !liveSession.canFallback() ||
          playback.interrupted ||
          expectedEpoch !== sessionEpoch ||
          (error instanceof Error &&
            (error.message === "request_cancelled" ||
              error.message === "session_expired"))
        ) {
          throw error;
        }
        coachActive =
          coachActive ||
          liveSession.requiresStatefulHTTPFallback();
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
    capture = await awaitVoiceTurnResult(
      recording.endPromise,
      () => rejectRecording(recording, "voice_turn_timeout"),
    );
    if (!capture.hasSpeech) {
      fail("no_speech");
    }
    if (capture.blob.size > AUDIO_MAX_BYTES) {
      fail("voice_turn_too_large");
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
      () => rejectRecording(recording, "voice_turn_timeout"),
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
      await awaitVoiceTurnResult(audioContext.resume());
    }
    if (expectedEpoch !== sessionEpoch) {
      fail(stoppedSessionCode(expectedEpoch));
    }
    playback = createStreamingPlayback(
      expectedEpoch,
      false,
      "http",
      coachActive,
      recording.lastVoiceAt,
      strictCloudMinimization,
      disarmVoiceStartDeadline,
    );
    // The microphone remains disabled until the request has actually been
    // committed. After that boundary, the bounded local interruption gate can
    // hear a sustained correction while the provider is still thinking.

    const payload = {
      audioBase64,
      mimeType: capture.mimeType,
      sessionState: serializedSessionState,
      strictCloudMinimization,
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
    const responsePromise = fetch(VOICE_ENDPOINT, {
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
    playback.armResponseInterruption(performance.now(), {
      onStall: () => {
        if (!playback.hasStreamedAudio()) {
          voiceStartTimedOut = true;
          requestController.abort();
        }
      },
    });
    const response = await awaitVoiceTurnResult(
      responsePromise,
      () => requestController.abort(),
    );
    if (!response.ok) {
      fail(mapVoiceResponseError(response.status));
    }
    const completed = await awaitVoiceTurnResult(
      consumeVoiceStream(
        response,
        playback,
        expectedEpoch,
        strictCloudMinimization,
      ),
      () => requestController.abort(),
    );
    await awaitVoiceStartDeadlineResult(
      awaitValidatedPlaybackCompletion(playback, expectedEpoch),
      () =>
        haltStreamingPlayback(
          playback,
          new Error("voice_turn_timeout"),
        ),
    );
    if (
      completed.streamedAudio &&
      !playback.hasStreamedAudio() &&
      !playback.interrupted
    ) {
      fail("voice_turn_timeout");
    }
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
    const interruptionAbortedTransport = Boolean(
      playback?.interruptedBeforeFinal &&
        shouldAbortPlaybackTransportOnInterrupt(playback),
    );
    // A prompt non-stateful HTTP/live abort keeps the held interruption for
    // Rust. A state-preserving HTTP or Native coach drain that fails
    // validation has no final state to bind, so timeout, malformed data, and
    // lifecycle failure discard it.
    if (shouldDiscardInterruptedPlaybackRecording(playback)) {
      discardInterruptedPlaybackRecording(playback);
    }
    const stopCode = stoppedSessionCode(expectedEpoch);
    if (stopCode === "session_expired") {
      fail(stopCode);
    }
    if (interruptionAbortedTransport) {
      fail("voice_interrupted");
    }
    if (turnTimedOut || voiceStartTimedOut) {
      fail("voice_turn_timeout");
    }
    if (error && typeof error === "object" && error.name === "AbortError") {
      fail(stopCode);
    }
    throw error;
  } finally {
    disarmVoiceStartDeadline();
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

function safeDocumentName(name) {
  const cleaned = name
    .normalize("NFC")
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .slice(0, 180)
    .trim();
  return cleaned || "paper.pdf";
}

async function attachDocument(inputId) {
	// Runtime PDF has no reviewed de-identification boundary. Fail before DOM
	// lookup or File access so an older cached UI cannot read or send bytes.
	void inputId;
	fail("document_unavailable");
}

function stopSession(reason = "request_cancelled") {
  const { pauseReason, stopCode } =
    classifyVoiceSessionStopReason(reason);
  const stoppedEpoch = sessionEpoch;
  rememberStoppedSession(stoppedEpoch, stopCode);
  sessionEpoch += 1;
  documentEpoch += 1;
  finishGate.reset();
  sessionExpiryWatchdog.disarm();

  if (activePasskeyController) {
    activePasskeyController.abort();
  }

  if (activeRequestController) {
    activeRequestController.abort();
    activeRequestController = undefined;
  }
  if (activeLiveSession) {
    const liveSession = activeLiveSession;
    activeLiveSession = undefined;
    liveSession.cancel(new Error(stopCode));
  }
  if (preparingLiveSession) {
    const liveSession = preparingLiveSession;
    preparingLiveSession = undefined;
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

  clearPendingDocument("session-stopped");
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
    preparingLiveSession ||
    pendingLiveSession ||
    activePlayback ||
    finishGate.isBusy() ||
    pendingDocument ||
    hasLiveAudioTrack(mediaStream),
  );
}

document.addEventListener("visibilitychange", () => {
  if (document.hidden && activePasskeyController) {
    activePasskeyController.abort();
  }
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
  if (activePasskeyController) {
    activePasskeyController.abort();
  }
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
  attachDocument,
  beginTurn,
  endTurn,
  finishTurn,
  getStatus,
  registerPasskeyAccount,
  stopSession,
  waitForTurnEnd,
});

Object.defineProperty(globalThis, "kotaeCloud", {
  configurable: false,
  enumerable: false,
  value: publicBridge,
  writable: false,
});
