export const VOICE_SESSION_LIMITS = Object.freeze({
  vadIntervalMs: 40,
  minimumVoiceMs: 120,
  softVoiceMinimumMs: 600,
  softVoiceSnrRatio: 1.45,
  softVoicePeakToRmsRatio: 1.6,
  softVoiceEnvelopeRatio: 1.18,
  // A new turn has no calibrated room floor yet. For only this bounded
  // bootstrap window, voice-shaped low-level audio may start a candidate from
  // an absolute floor. It still needs sustained, changing-envelope evidence.
  softVoiceBootstrapMs: 1_200,
  softVoiceBootstrapMinimumRms: 0.004,
  // Envelope evidence is a renewable lease rather than a permanent flag. A
  // stationary fan/hum therefore cannot keep an already-confirmed turn alive.
  softVoiceEvidenceLeaseMs: 400,
  // Confirmation also needs envelope changes spread across time; one short
  // echo followed by a stationary tail is not a quiet utterance.
  softVoiceMinimumEvidenceSpanMs: 320,
  // After clear speech, a short but genuinely changing quiet word may refresh
  // the endpoint without opting the whole turn into the three-second mode.
  softVoiceContinuationEvidenceSpanMs: 120,
  endOfTurnSilenceMs: 1_200,
  reflectiveEndOfTurnSilenceMs: 2_200,
  softVoiceEndOfTurnSilenceMs: 3_000,
  reflectiveSpeechSpanMs: 1_600,
  hybridEndpointSilenceMs: 1_200,
  hybridReflectiveEndpointSilenceMs: 2_200,
  hybridSoftVoiceEndpointSilenceMs: 3_000,
  hybridEndpointAgreementWindowMs: 2_000,
  hybridSoftVoiceAgreementWindowMs: 3_500,
  // A voice candidate must either reach the 120 ms confirmation threshold
  // promptly or be discarded. This also bounds unconfirmed room audio before
  // a fresh candidate and its isolated recorder are created.
  candidateCaptureLimitMs: 200,
  // Quiet speech needs a longer, still finite privacy window because it is
  // confirmed from sustained signal-to-noise and envelope evidence.
  softCandidateCaptureLimitMs: 1_200,
  silentCaptureLimitMs: 30_000,
  spokenCaptureLimitMs: 55_000,
  idleSessionLimitMs: 3 * 60_000,
  maximumSessionMs: 30 * 60_000,
  pendingDocumentLimitMs: 5 * 60_000,
});

const RESEARCH_STATUSES = Object.freeze([
  "none",
  "needs_primary_evidence",
  "unavailable",
]);
const RESEARCH_RECORD_KEYS = Object.freeze([
  "doi",
  "published",
  "source",
  "title",
  "url",
]);
const MAX_RESEARCH_RECORDS = 5;

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function boundedCleanString(value, maximumLength, allowEmpty = false) {
  if (typeof value !== "string") return false;
  const characters = Array.from(value);
  if (
    characters.length > maximumLength ||
    (!allowEmpty && characters.length === 0)
  ) {
    return false;
  }
  return characters.every(
    (character) =>
      character !== "\u0000" &&
      character !== "\r" &&
      character !== "\n" &&
      character !== "\u2028" &&
      character !== "\u2029",
  );
}

function validCanonicalDoi(doi) {
  if (
    !boundedCleanString(doi, 256) ||
    doi !== doi.toLowerCase() ||
    !/^10\.\d{4,9}\/\S+$/u.test(doi)
  ) {
    return false;
  }
  return !Array.from(doi).some(
    (character) =>
      /\s/u.test(character) ||
      character === "?" ||
      character === "#" ||
      character === "\\",
  );
}

function validCanonicalDoiUrl(rawUrl, doi) {
  if (!boundedCleanString(rawUrl, 2_048)) return false;
  let parsed;
  try {
    parsed = new URL(rawUrl);
  } catch {
    return false;
  }
  if (
    parsed.protocol !== "https:" ||
    parsed.hostname !== "doi.org" ||
    parsed.port !== "" ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.search !== "" ||
    parsed.hash !== ""
  ) {
    return false;
  }
  try {
    return decodeURIComponent(parsed.pathname.slice(1)) === doi;
  } catch {
    return false;
  }
}

function validPublicationDate(value) {
  return (
    boundedCleanString(value, 40, true) &&
    (value === "" ||
      /^\d{4}(?:-\d{2}(?:-\d{2})?)?(?:T[0-9:.+-]+Z?)?$/u.test(value))
  );
}

// Research metadata is deliberately normalized separately from the spoken
// answer. It identifies material worth checking; it never represents
// claim-level verification.
export function normalizeResearchDiscovery(status, records) {
  if (
    !RESEARCH_STATUSES.includes(status) ||
    !Array.isArray(records) ||
    records.length > MAX_RESEARCH_RECORDS ||
    (status !== "needs_primary_evidence" && records.length !== 0)
  ) {
    throw new TypeError("research_discovery_invalid");
  }

  const normalizedRecords = records.map((record) => {
    if (
      !isPlainRecord(record) ||
      Object.keys(record).sort().join("\u0000") !==
        RESEARCH_RECORD_KEYS.join("\u0000") ||
      !boundedCleanString(record.title, 300, true) ||
      !validCanonicalDoi(record.doi) ||
      !validCanonicalDoiUrl(record.url, record.doi) ||
      !validPublicationDate(record.published) ||
      record.source !== "Crossref"
    ) {
      throw new TypeError("research_discovery_invalid");
    }
    return Object.freeze({
      title: record.title,
      doi: record.doi,
      url: record.url,
      published: record.published,
      source: record.source,
    });
  });

  return Object.freeze({
    status,
    records: Object.freeze(normalizedRecords),
  });
}

function finiteTimestamp(value, name) {
  if (!Number.isFinite(value) || value < 0) {
    throw new TypeError(`${name}_invalid`);
  }
  return value;
}

export async function initializeWithCleanup(initialize, cleanup) {
  if (typeof initialize !== "function" || typeof cleanup !== "function") {
    throw new TypeError("initializer_invalid");
  }

  try {
    return await initialize();
  } catch (initializationError) {
    try {
      await cleanup();
    } catch {
      // Initialization failure remains the useful boundary error. Cleanup
      // functions must be idempotent so callers can stop the session again.
    }
    throw initializationError;
  }
}

export function createSessionClock({
  now,
  idleLimitMs = VOICE_SESSION_LIMITS.idleSessionLimitMs,
  maximumLimitMs = VOICE_SESSION_LIMITS.maximumSessionMs,
} = {}) {
  if (typeof now !== "function") {
    throw new TypeError("clock_invalid");
  }
  if (
    !Number.isFinite(idleLimitMs) ||
    idleLimitMs <= 0 ||
    !Number.isFinite(maximumLimitMs) ||
    maximumLimitMs <= 0
  ) {
    throw new TypeError("session_limits_invalid");
  }

  let startedAt = null;
  let lastSpeechAt = null;
  let responseActive = false;

  function currentTime() {
    return finiteTimestamp(now(), "clock");
  }

  function expiryAt(timestamp) {
    if (startedAt === null) return null;
    if (timestamp - startedAt >= maximumLimitMs) return "maximum";
    if (
      !responseActive &&
      lastSpeechAt !== null &&
      timestamp - lastSpeechAt >= idleLimitMs
    ) {
      return "idle";
    }
    return null;
  }

  return Object.freeze({
    begin() {
      const timestamp = currentTime();
      const expiry = expiryAt(timestamp);
      if (expiry !== null) {
        return Object.freeze({ expiry, ok: false });
      }
      if (startedAt === null) {
        startedAt = timestamp;
        lastSpeechAt = timestamp;
      }
      return Object.freeze({ expiry: null, ok: true });
    },
    isStarted() {
      return startedAt !== null;
    },
    markSpeech() {
      const timestamp = currentTime();
      const expiry = expiryAt(timestamp);
      if (expiry !== null) {
        return Object.freeze({ expiry, ok: false });
      }
      if (startedAt !== null) {
        lastSpeechAt = timestamp;
      }
      return Object.freeze({ expiry: null, ok: true });
    },
    beginResponse() {
      const timestamp = currentTime();
      const expiry = expiryAt(timestamp);
      if (startedAt === null || expiry !== null) {
        return Object.freeze({ expiry: expiry ?? "maximum", ok: false });
      }
      responseActive = true;
      return Object.freeze({ expiry: null, ok: true });
    },
    completeResponse() {
      const timestamp = currentTime();
      if (startedAt === null || !responseActive) {
        return Object.freeze({ expiry: "maximum", ok: false });
      }
      if (timestamp - startedAt >= maximumLimitMs) {
        responseActive = false;
        return Object.freeze({ expiry: "maximum", ok: false });
      }
      responseActive = false;
      lastSpeechAt = timestamp;
      return Object.freeze({ expiry: null, ok: true });
    },
    cancelResponse() {
      const timestamp = currentTime();
      if (startedAt === null || !responseActive) {
        return Object.freeze({ expiry: "maximum", ok: false });
      }
      responseActive = false;
      const expiry = expiryAt(timestamp);
      return Object.freeze({ expiry, ok: expiry === null });
    },
    check() {
      const expiry = expiryAt(currentTime());
      return Object.freeze({ expiry, ok: expiry === null });
    },
    millisecondsUntilExpiry() {
      if (startedAt === null) return null;
      const timestamp = currentTime();
      const maximumRemaining = maximumLimitMs - (timestamp - startedAt);
      const idleRemaining =
        responseActive || lastSpeechAt === null
          ? maximumRemaining
          : idleLimitMs - (timestamp - lastSpeechAt);
      return Math.max(0, Math.min(maximumRemaining, idleRemaining));
    },
    reset() {
      startedAt = null;
      lastSpeechAt = null;
      responseActive = false;
    },
    snapshot() {
      return Object.freeze({ lastSpeechAt, responseActive, startedAt });
    },
  });
}

export function createSessionExpiryWatchdog({
  check,
  clearTimer,
  expire,
  millisecondsUntilExpiry,
  setTimer,
} = {}) {
  if (
    typeof check !== "function" ||
    typeof clearTimer !== "function" ||
    typeof expire !== "function" ||
    typeof millisecondsUntilExpiry !== "function" ||
    typeof setTimer !== "function"
  ) {
    throw new TypeError("session_watchdog_invalid");
  }

  let generation = 0;
  let timer;

  function disarm() {
    generation += 1;
    if (timer !== undefined) {
      clearTimer(timer);
      timer = undefined;
    }
  }

  function arm() {
    disarm();
    const status = check();
    if (!status?.ok) {
      expire(status?.expiry ?? "maximum");
      return false;
    }
    const delay = millisecondsUntilExpiry();
    if (delay === null) return true;
    if (!Number.isFinite(delay) || delay < 0) {
      expire("maximum");
      return false;
    }

    const armedGeneration = generation;
    timer = setTimer(() => {
      if (armedGeneration !== generation) return;
      timer = undefined;
      const current = check();
      if (!current?.ok) {
        expire(current?.expiry ?? "maximum");
        return;
      }
      arm();
    }, Math.ceil(delay));
    return true;
  }

  return Object.freeze({ arm, disarm });
}

// Only these fixed, content-free reasons may cross the JS/Rust boundary and
// turn an active voice session into Paused. Unknown strings are deliberately
// reduced to an ordinary cancellation so error text, transcripts, and device
// labels can never become event metadata.
export function classifyVoiceSessionStopReason(reason) {
  switch (reason) {
    case "idle":
    case "maximum":
      return Object.freeze({ pauseReason: reason, stopCode: "session_expired" });
    case "hidden":
    case "pagehide":
      return Object.freeze({ pauseReason: reason, stopCode: "request_cancelled" });
    case "microphone_lost":
      return Object.freeze({
        pauseReason: reason,
        stopCode: "microphone_unavailable",
      });
    default:
      return Object.freeze({
        pauseReason: null,
        stopCode: "request_cancelled",
      });
  }
}

export function shouldStopSessionForLifecycle(eventType, hidden, active) {
  if (!active) return false;
  if (eventType === "pagehide") return true;
  return eventType === "visibilitychange" && hidden === true;
}

export function isPendingDocumentExpired(
  attachedAt,
  now,
  limitMs = VOICE_SESSION_LIMITS.pendingDocumentLimitMs,
) {
  const attachedTimestamp = finiteTimestamp(attachedAt, "document_attached_at");
  const currentTimestamp = finiteTimestamp(now, "document_time");
  if (!Number.isFinite(limitMs) || limitMs <= 0) {
    throw new TypeError("document_limit_invalid");
  }
  if (currentTimestamp < attachedTimestamp) {
    throw new TypeError("document_time_invalid");
  }
  return currentTimestamp - attachedTimestamp >= limitMs;
}

export function createTurnGate() {
  let activeToken = null;
  let sequence = 0;

  return Object.freeze({
    acquire() {
      if (activeToken !== null) return null;
      sequence += 1;
      activeToken = Object.freeze({ sequence });
      return activeToken;
    },
    isBusy() {
      return activeToken !== null;
    },
    release(token) {
      if (token === null || token !== activeToken) return false;
      activeToken = null;
      return true;
    },
    reset() {
      activeToken = null;
      sequence += 1;
    },
  });
}

export function createStopLatch() {
  let requestedReason = null;

  return Object.freeze({
    isRequested() {
      return requestedReason !== null;
    },
    reason() {
      return requestedReason;
    },
    request(reason) {
      if (typeof reason !== "string" || reason.length === 0) {
        throw new TypeError("stop_reason_invalid");
      }
      if (requestedReason !== null) return false;
      requestedReason = reason;
      return true;
    },
  });
}

export function createRetryableInitializer(initialize) {
  if (typeof initialize !== "function") {
    throw new TypeError("initializer_invalid");
  }

  let pending;
  return () => {
    if (pending === undefined) {
      const attempt = Promise.resolve().then(() => initialize());
      pending = attempt.catch((error) => {
        pending = undefined;
        throw error;
      });
    }
    return pending;
  };
}

export function createCaptureBuffer({ maximumBytes } = {}) {
  if (!Number.isSafeInteger(maximumBytes) || maximumBytes <= 0) {
    throw new TypeError("capture_limits_invalid");
  }

  let retained = [];
  let retainedBytes = 0;
  let tooLarge = false;

  function chunkSize(chunk) {
    if (
      chunk === null ||
      typeof chunk !== "object" ||
      !Number.isSafeInteger(chunk.size) ||
      chunk.size < 0
    ) {
      throw new TypeError("capture_chunk_invalid");
    }
    return chunk.size;
  }

  function clearArrays() {
    retained.length = 0;
    retainedBytes = 0;
  }

  function snapshot() {
    return Object.freeze({
      retainedBytes,
      retainedChunks: retained.length,
      tooLarge,
      totalBytes: retainedBytes,
    });
  }

  return Object.freeze({
    append(chunk) {
      if (tooLarge) return snapshot();
      const size = chunkSize(chunk);
      if (retainedBytes + size > maximumBytes) {
        clearArrays();
        tooLarge = true;
        return snapshot();
      }
      retained.push(chunk);
      retainedBytes += size;
      return snapshot();
    },
    clear() {
      clearArrays();
      tooLarge = false;
      return snapshot();
    },
    snapshot,
    take() {
      const chunks = retained.slice();
      const totalBytes = retainedBytes;
      clearArrays();
      tooLarge = false;
      return Object.freeze({ chunks, totalBytes });
    },
  });
}

export function isValidTurnMode(turnMode) {
  return (
    turnMode === "intentional" ||
    turnMode === "foreground" ||
    turnMode === "ambient"
  );
}

export function turnModeForGestureEpoch(firstTurnAfterExplicitGesture) {
  if (typeof firstTurnAfterExplicitGesture !== "boolean") {
    throw new TypeError("gesture_epoch_invalid");
  }
  return firstTurnAfterExplicitGesture ? "intentional" : "foreground";
}

export const CANDIDATE_CAPTURE_PHASES = Object.freeze({
  armed: "armed",
  candidate: "candidate",
  confirmed: "confirmed",
});

export function createCandidateCaptureState() {
  return Object.freeze({
    action: null,
    candidateStartedAt: null,
    captureLimitMs: null,
    phase: CANDIDATE_CAPTURE_PHASES.armed,
  });
}

export function advanceCandidateCapture(
  previous,
  vadState,
  now,
  {
    candidateCaptureLimitMs =
      VOICE_SESSION_LIMITS.candidateCaptureLimitMs,
    softCandidateCaptureLimitMs =
      VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  } = {},
) {
  if (
    previous === null ||
    typeof previous !== "object" ||
    !Object.values(CANDIDATE_CAPTURE_PHASES).includes(previous.phase) ||
    (previous.candidateStartedAt !== null &&
      (!Number.isFinite(previous.candidateStartedAt) ||
        previous.candidateStartedAt < 0)) ||
    (previous.captureLimitMs !== undefined &&
      previous.captureLimitMs !== null &&
      (!Number.isFinite(previous.captureLimitMs) ||
        previous.captureLimitMs < candidateCaptureLimitMs ||
        previous.captureLimitMs > softCandidateCaptureLimitMs)) ||
    vadState === null ||
    typeof vadState !== "object" ||
    typeof vadState.hasSpeech !== "boolean" ||
    typeof vadState.sampleVoiced !== "boolean" ||
    !Number.isFinite(vadState.voiceRunMs) ||
    vadState.voiceRunMs < 0 ||
    !Number.isFinite(candidateCaptureLimitMs) ||
    candidateCaptureLimitMs <= 0 ||
    !Number.isFinite(softCandidateCaptureLimitMs) ||
    softCandidateCaptureLimitMs < candidateCaptureLimitMs ||
    (vadState.softVoiceCandidate !== undefined &&
      typeof vadState.softVoiceCandidate !== "boolean")
  ) {
    throw new TypeError("candidate_capture_state_invalid");
  }
  const timestamp = finiteTimestamp(now, "candidate_capture_time");
  const requestedCaptureLimitMs = vadState.softVoiceCandidate
    ? softCandidateCaptureLimitMs
    : candidateCaptureLimitMs;
  const previousCaptureLimitMs =
    previous.captureLimitMs ??
    (previous.phase === CANDIDATE_CAPTURE_PHASES.armed
      ? null
      : candidateCaptureLimitMs);
  // Once any frame needs the quiet-speech window, retain that finite absolute
  // limit for this candidate. Classification may later become clear again,
  // but the onset timestamp never moves and the limit can never exceed 1.2 s.
  const effectiveCaptureLimitMs =
    previousCaptureLimitMs === null
      ? requestedCaptureLimitMs
      : Math.max(previousCaptureLimitMs, requestedCaptureLimitMs);
  if (
    previous.candidateStartedAt !== null &&
    timestamp < previous.candidateStartedAt
  ) {
    throw new TypeError("candidate_capture_time_invalid");
  }

  if (previous.phase === CANDIDATE_CAPTURE_PHASES.confirmed) {
    if (!vadState.hasSpeech) {
      throw new TypeError("candidate_capture_transition_invalid");
    }
    return Object.freeze({
      action: null,
      candidateStartedAt: previous.candidateStartedAt,
      captureLimitMs: previousCaptureLimitMs,
      phase: CANDIDATE_CAPTURE_PHASES.confirmed,
    });
  }
  if (previous.phase === CANDIDATE_CAPTURE_PHASES.armed) {
    if (!vadState.sampleVoiced) {
      return Object.freeze({
        action: null,
        candidateStartedAt: null,
        captureLimitMs: null,
        phase: CANDIDATE_CAPTURE_PHASES.armed,
      });
    }
    return Object.freeze({
      action: "start",
      candidateStartedAt: timestamp,
      captureLimitMs: effectiveCaptureLimitMs,
      phase: vadState.hasSpeech
        ? CANDIDATE_CAPTURE_PHASES.confirmed
        : CANDIDATE_CAPTURE_PHASES.candidate,
    });
  }
  if (
    vadState.voiceRunMs === 0 ||
    timestamp - previous.candidateStartedAt >= effectiveCaptureLimitMs
  ) {
    return Object.freeze({
      action: "discard",
      candidateStartedAt: null,
      captureLimitMs: null,
      phase: CANDIDATE_CAPTURE_PHASES.armed,
    });
  }
  if (vadState.hasSpeech) {
    return Object.freeze({
      action: "confirm",
      candidateStartedAt: previous.candidateStartedAt,
      captureLimitMs: effectiveCaptureLimitMs,
      phase: CANDIDATE_CAPTURE_PHASES.confirmed,
    });
  }
  return Object.freeze({
    action: null,
    candidateStartedAt: previous.candidateStartedAt,
    captureLimitMs: effectiveCaptureLimitMs,
    phase: CANDIDATE_CAPTURE_PHASES.candidate,
  });
}

export function createVadState(startedAt) {
  return Object.freeze({
    action: null,
    clearVoiceRunMs: 0,
    firstVoiceAt: null,
    hasSpeech: false,
    lastVoiceAt: null,
    noiseFloor: 0.006,
    sampleVoiced: false,
    softVoiceCandidate: false,
    softVoiceConfirmed: false,
    softVoiceEvidenceAt: null,
    softVoiceEvidenceStartedAt: null,
    softVoiceMaxRms: 0,
    softVoiceMinRms: null,
    softVoiceRunMs: 0,
    startedAt: finiteTimestamp(startedAt, "vad_started_at"),
    voiceRunMs: 0,
  });
}

export function shouldCommitHybridEndpoint({
  firstVoiceAt,
  hasSpeech,
  lastVoiceAt,
  now,
  providerEndpointAt,
  softVoiceConfirmed = false,
}) {
  if (
    typeof hasSpeech !== "boolean" ||
    typeof softVoiceConfirmed !== "boolean" ||
    !Number.isFinite(now) ||
    now < 0 ||
    (firstVoiceAt !== null &&
      (!Number.isFinite(firstVoiceAt) || firstVoiceAt < 0)) ||
    (lastVoiceAt !== null &&
      (!Number.isFinite(lastVoiceAt) || lastVoiceAt < 0)) ||
    (providerEndpointAt !== null &&
      (!Number.isFinite(providerEndpointAt) ||
        providerEndpointAt < 0))
  ) {
    throw new TypeError("hybrid_endpoint_state_invalid");
  }
  if (
    !hasSpeech ||
    firstVoiceAt === null ||
    lastVoiceAt === null ||
    providerEndpointAt === null ||
    firstVoiceAt > lastVoiceAt ||
    lastVoiceAt > providerEndpointAt ||
    providerEndpointAt > now
  ) {
    return false;
  }
  const speechSpan =
    lastVoiceAt -
    firstVoiceAt +
    VOICE_SESSION_LIMITS.vadIntervalMs;
  const requiredSilence = softVoiceConfirmed
    ? VOICE_SESSION_LIMITS.hybridSoftVoiceEndpointSilenceMs
    : speechSpan >= VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs
      ? VOICE_SESSION_LIMITS.hybridReflectiveEndpointSilenceMs
      : VOICE_SESSION_LIMITS.hybridEndpointSilenceMs;
  const agreementWindow = softVoiceConfirmed
    ? VOICE_SESSION_LIMITS.hybridSoftVoiceAgreementWindowMs
    : VOICE_SESSION_LIMITS.hybridEndpointAgreementWindowMs;
  if (now - providerEndpointAt > agreementWindow) return false;
  return now - lastVoiceAt >= requiredSilence;
}

export function advanceVad(
  previous,
  { now, peak, rms },
  {
    intervalMs = VOICE_SESSION_LIMITS.vadIntervalMs,
    minimumVoiceMs = VOICE_SESSION_LIMITS.minimumVoiceMs,
    softVoiceMinimumMs = VOICE_SESSION_LIMITS.softVoiceMinimumMs,
    softVoiceSnrRatio = VOICE_SESSION_LIMITS.softVoiceSnrRatio,
    softVoicePeakToRmsRatio =
      VOICE_SESSION_LIMITS.softVoicePeakToRmsRatio,
    softVoiceEnvelopeRatio =
      VOICE_SESSION_LIMITS.softVoiceEnvelopeRatio,
    softVoiceBootstrapMs = VOICE_SESSION_LIMITS.softVoiceBootstrapMs,
    softVoiceBootstrapMinimumRms =
      VOICE_SESSION_LIMITS.softVoiceBootstrapMinimumRms,
    softVoiceEvidenceLeaseMs =
      VOICE_SESSION_LIMITS.softVoiceEvidenceLeaseMs,
    softVoiceMinimumEvidenceSpanMs =
      VOICE_SESSION_LIMITS.softVoiceMinimumEvidenceSpanMs,
    softVoiceContinuationEvidenceSpanMs =
      VOICE_SESSION_LIMITS.softVoiceContinuationEvidenceSpanMs,
    softCandidateCaptureLimitMs =
      VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
    endOfTurnSilenceMs = VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
    reflectiveEndOfTurnSilenceMs =
      VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
    softVoiceEndOfTurnSilenceMs =
      VOICE_SESSION_LIMITS.softVoiceEndOfTurnSilenceMs,
    reflectiveSpeechSpanMs =
      VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs,
    silentCaptureLimitMs = VOICE_SESSION_LIMITS.silentCaptureLimitMs,
    spokenCaptureLimitMs = VOICE_SESSION_LIMITS.spokenCaptureLimitMs,
  } = {},
) {
  if (
    previous === null ||
    typeof previous !== "object" ||
    !Number.isFinite(previous.startedAt)
  ) {
    throw new TypeError("vad_state_invalid");
  }
  const timestamp = finiteTimestamp(now, "vad_time");
  if (
    timestamp < previous.startedAt ||
    !Number.isFinite(rms) ||
    rms < 0 ||
    !Number.isFinite(peak) ||
    peak < 0
  ) {
    throw new TypeError("vad_sample_invalid");
  }
  if (previous.action !== null) return previous;

  let {
    clearVoiceRunMs = previous.voiceRunMs,
    firstVoiceAt,
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    softVoiceCandidate = false,
    softVoiceConfirmed = false,
    softVoiceEvidenceAt = null,
    softVoiceEvidenceStartedAt = null,
    softVoiceMaxRms = 0,
    softVoiceMinRms = null,
    softVoiceRunMs = 0,
    voiceRunMs,
  } = previous;
  const elapsed = timestamp - previous.startedAt;
  const threshold = hasSpeech
    ? Math.max(0.009, noiseFloor * 1.7)
    : Math.max(0.014, noiseFloor * 2.8);
  const peakMultiplier = hasSpeech ? 1.35 : 1.8;
  const soundsClear =
    rms >= threshold && peak >= threshold * peakMultiplier;
  // This path is relative to the learned room floor. It deliberately has no
  // lower fixed speech threshold: low-SNR evidence must instead persist and
  // show a changing speech envelope before it can become an utterance.
  const hasVoiceShapedPeak =
    peak >= rms * softVoicePeakToRmsRatio;
  const bootstrapSoftVoice =
    !hasSpeech &&
    (elapsed <= softVoiceBootstrapMs || softVoiceCandidate) &&
    rms >= softVoiceBootstrapMinimumRms;
  const soundsSoft =
    !soundsClear &&
    hasVoiceShapedPeak &&
    (rms >= noiseFloor * softVoiceSnrRatio || bootstrapSoftVoice);
  let sampleVoiced = soundsClear || soundsSoft;

  let hasRecentSoftEnvelope = false;
  if (soundsSoft) {
    softVoiceCandidate = true;
    softVoiceRunMs += intervalMs;
    softVoiceMinRms =
      softVoiceMinRms === null
        ? rms
        : Math.min(softVoiceMinRms, rms);
    softVoiceMaxRms = Math.max(softVoiceMaxRms, rms);
    const envelopeChanged =
      softVoiceMinRms > 0 &&
      softVoiceMaxRms >= softVoiceMinRms * softVoiceEnvelopeRatio;
    if (envelopeChanged) {
      // Reset the range after each observation so old speech dynamics cannot
      // grant an unlimited lease to later stationary room noise or echo.
      if (softVoiceEvidenceStartedAt === null) {
        softVoiceEvidenceStartedAt = timestamp;
      }
      softVoiceEvidenceAt = timestamp;
      softVoiceMinRms = rms;
      softVoiceMaxRms = rms;
    }
    hasRecentSoftEnvelope =
      softVoiceEvidenceAt !== null &&
      timestamp - softVoiceEvidenceAt <= softVoiceEvidenceLeaseMs;
  }
  const hasSustainedSoftEnvelope =
    hasRecentSoftEnvelope &&
    softVoiceEvidenceStartedAt !== null &&
    softVoiceEvidenceAt - softVoiceEvidenceStartedAt >=
      softVoiceMinimumEvidenceSpanMs;
  const hasSoftContinuationEnvelope =
    hasRecentSoftEnvelope &&
    softVoiceEvidenceStartedAt !== null &&
    softVoiceEvidenceAt - softVoiceEvidenceStartedAt >=
      softVoiceContinuationEvidenceSpanMs;

  if (hasSpeech) {
    if (soundsClear) {
      clearVoiceRunMs += intervalMs;
      lastVoiceAt = timestamp;
      softVoiceCandidate = false;
      softVoiceEvidenceAt = null;
      softVoiceEvidenceStartedAt = null;
      softVoiceMaxRms = 0;
      softVoiceMinRms = null;
      softVoiceRunMs = 0;
    } else if (soundsSoft) {
      clearVoiceRunMs = Math.max(
        0,
        clearVoiceRunMs - intervalMs * 0.5,
      );
      if (
        !softVoiceConfirmed &&
        softVoiceRunMs >= softVoiceMinimumMs &&
        hasSustainedSoftEnvelope
      ) {
        softVoiceConfirmed = true;
      }
      if (
        (softVoiceConfirmed && hasRecentSoftEnvelope) ||
        (!softVoiceConfirmed && hasSoftContinuationEnvelope)
      ) {
        lastVoiceAt = timestamp;
      } else if (
        softVoiceRunMs >= softCandidateCaptureLimitMs &&
        !hasRecentSoftEnvelope
      ) {
        softVoiceCandidate = false;
        softVoiceEvidenceAt = null;
        softVoiceEvidenceStartedAt = null;
        softVoiceMaxRms = 0;
        softVoiceMinRms = null;
        softVoiceRunMs = 0;
        sampleVoiced = false;
      }
    } else {
      clearVoiceRunMs = Math.max(
        0,
        clearVoiceRunMs - intervalMs * 0.5,
      );
      softVoiceRunMs = Math.max(
        0,
        softVoiceRunMs - intervalMs * 0.5,
      );
      if (softVoiceRunMs === 0) {
        softVoiceCandidate = false;
        softVoiceEvidenceAt = null;
        softVoiceEvidenceStartedAt = null;
        softVoiceMaxRms = 0;
        softVoiceMinRms = null;
      }
      sampleVoiced = false;
    }
  } else if (soundsClear) {
    if (firstVoiceAt === null) firstVoiceAt = timestamp;
    clearVoiceRunMs += intervalMs;
    softVoiceRunMs = Math.max(
      0,
      softVoiceRunMs - intervalMs * 0.5,
    );
    lastVoiceAt = timestamp;
    if (clearVoiceRunMs >= minimumVoiceMs) {
      hasSpeech = true;
      softVoiceCandidate = false;
      softVoiceEvidenceAt = null;
      softVoiceEvidenceStartedAt = null;
      softVoiceMaxRms = 0;
      softVoiceMinRms = null;
      softVoiceRunMs = 0;
    }
  } else if (soundsSoft) {
    if (firstVoiceAt === null) firstVoiceAt = timestamp;
    clearVoiceRunMs = Math.max(
      0,
      clearVoiceRunMs - intervalMs * 0.5,
    );
    lastVoiceAt = timestamp;
    if (
      softVoiceRunMs >= softVoiceMinimumMs &&
      hasSustainedSoftEnvelope
    ) {
      hasSpeech = true;
      softVoiceConfirmed = true;
    } else if (
      softVoiceRunMs >= softCandidateCaptureLimitMs &&
      !hasRecentSoftEnvelope
    ) {
      // A stationary hum can remain above the learned floor indefinitely. A
      // full probe with no envelope movement is room noise, not quiet speech.
      noiseFloor = Math.min(0.04, Math.max(0.0025, rms));
      clearVoiceRunMs = 0;
      softVoiceCandidate = false;
      softVoiceEvidenceAt = null;
      softVoiceEvidenceStartedAt = null;
      softVoiceMaxRms = 0;
      softVoiceMinRms = null;
      softVoiceRunMs = 0;
      sampleVoiced = false;
    }
  } else {
    noiseFloor = Math.min(
      0.04,
      Math.max(0.0025, noiseFloor * 0.94 + rms * 0.06),
    );
    // Preserve brief gaps and unvoiced consonants without accepting an
    // isolated noise spike as speech.
    clearVoiceRunMs = Math.max(
      0,
      clearVoiceRunMs - intervalMs * 0.5,
    );
    softVoiceRunMs = Math.max(0, softVoiceRunMs - intervalMs);
    if (softVoiceRunMs === 0 && clearVoiceRunMs === 0) {
      softVoiceCandidate = false;
      softVoiceEvidenceAt = null;
      softVoiceEvidenceStartedAt = null;
      softVoiceMaxRms = 0;
      softVoiceMinRms = null;
    }
  }

  voiceRunMs = Math.max(clearVoiceRunMs, softVoiceRunMs);
  if (!hasSpeech && voiceRunMs === 0) {
    firstVoiceAt = null;
    lastVoiceAt = null;
  }

  let action = null;
  const speechSpanMs =
    firstVoiceAt === null || lastVoiceAt === null
      ? 0
      : lastVoiceAt - firstVoiceAt + intervalMs;
  const trailingSilenceMs = softVoiceConfirmed
    ? softVoiceEndOfTurnSilenceMs
    : speechSpanMs >= reflectiveSpeechSpanMs
      ? reflectiveEndOfTurnSilenceMs
      : endOfTurnSilenceMs;
  // Keep direct questions fast, while giving a longer, think-aloud utterance
  // enough room for a natural Japanese pause before committing the turn.
  if (
    hasSpeech &&
    lastVoiceAt !== null &&
    timestamp - lastVoiceAt >= trailingSilenceMs
  ) {
    action = "end-of-turn";
  } else {
    if (!hasSpeech && elapsed >= silentCaptureLimitMs) {
      action = "silence";
    } else if (hasSpeech && elapsed >= spokenCaptureLimitMs) {
      action = "duration-limit";
    }
  }

  return Object.freeze({
    action,
    clearVoiceRunMs,
    firstVoiceAt,
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    sampleVoiced,
    softVoiceCandidate,
    softVoiceConfirmed,
    softVoiceEvidenceAt,
    softVoiceEvidenceStartedAt,
    softVoiceMaxRms,
    softVoiceMinRms,
    softVoiceRunMs,
    startedAt: previous.startedAt,
    voiceRunMs,
  });
}
