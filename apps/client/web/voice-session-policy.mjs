export const VOICE_SESSION_LIMITS = Object.freeze({
  vadIntervalMs: 40,
  minimumVoiceMs: 120,
  endOfTurnSilenceMs: 700,
  reflectiveEndOfTurnSilenceMs: 1_700,
  reflectiveSpeechSpanMs: 2_400,
  hybridEndpointSilenceMs: 700,
  hybridReflectiveEndpointSilenceMs: 1_700,
  hybridEndpointAgreementWindowMs: 2_000,
  candidateCaptureLimitMs: 1_500,
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

  function currentTime() {
    return finiteTimestamp(now(), "clock");
  }

  function expiryAt(timestamp) {
    if (startedAt === null) return null;
    if (timestamp - startedAt >= maximumLimitMs) return "maximum";
    if (
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
      if (startedAt !== null) {
        lastSpeechAt = currentTime();
      }
    },
    reset() {
      startedAt = null;
      lastSpeechAt = null;
    },
    snapshot() {
      return Object.freeze({ lastSpeechAt, startedAt });
    },
  });
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
  } = {},
) {
  if (
    previous === null ||
    typeof previous !== "object" ||
    !Object.values(CANDIDATE_CAPTURE_PHASES).includes(previous.phase) ||
    (previous.candidateStartedAt !== null &&
      (!Number.isFinite(previous.candidateStartedAt) ||
        previous.candidateStartedAt < 0)) ||
    vadState === null ||
    typeof vadState !== "object" ||
    typeof vadState.hasSpeech !== "boolean" ||
    typeof vadState.sampleVoiced !== "boolean" ||
    !Number.isFinite(vadState.voiceRunMs) ||
    vadState.voiceRunMs < 0 ||
    !Number.isFinite(candidateCaptureLimitMs) ||
    candidateCaptureLimitMs <= 0
  ) {
    throw new TypeError("candidate_capture_state_invalid");
  }
  const timestamp = finiteTimestamp(now, "candidate_capture_time");
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
      phase: CANDIDATE_CAPTURE_PHASES.confirmed,
    });
  }
  if (previous.phase === CANDIDATE_CAPTURE_PHASES.armed) {
    if (!vadState.sampleVoiced) {
      return Object.freeze({
        action: null,
        candidateStartedAt: null,
        phase: CANDIDATE_CAPTURE_PHASES.armed,
      });
    }
    return Object.freeze({
      action: "start",
      candidateStartedAt: timestamp,
      phase: vadState.hasSpeech
        ? CANDIDATE_CAPTURE_PHASES.confirmed
        : CANDIDATE_CAPTURE_PHASES.candidate,
    });
  }
  if (vadState.hasSpeech) {
    return Object.freeze({
      action: "confirm",
      candidateStartedAt: previous.candidateStartedAt,
      phase: CANDIDATE_CAPTURE_PHASES.confirmed,
    });
  }
  if (
    vadState.voiceRunMs === 0 ||
    timestamp - previous.candidateStartedAt >= candidateCaptureLimitMs
  ) {
    return Object.freeze({
      action: "discard",
      candidateStartedAt: null,
      phase: CANDIDATE_CAPTURE_PHASES.armed,
    });
  }
  return Object.freeze({
    action: null,
    candidateStartedAt: previous.candidateStartedAt,
    phase: CANDIDATE_CAPTURE_PHASES.candidate,
  });
}

export function createVadState(startedAt) {
  return Object.freeze({
    action: null,
    firstVoiceAt: null,
    hasSpeech: false,
    lastVoiceAt: null,
    noiseFloor: 0.006,
    sampleVoiced: false,
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
}) {
  if (
    typeof hasSpeech !== "boolean" ||
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
    providerEndpointAt > now ||
    now - providerEndpointAt >
      VOICE_SESSION_LIMITS.hybridEndpointAgreementWindowMs
  ) {
    return false;
  }
  const speechSpan =
    lastVoiceAt -
    firstVoiceAt +
    VOICE_SESSION_LIMITS.vadIntervalMs;
  const requiredSilence =
    speechSpan >= VOICE_SESSION_LIMITS.reflectiveSpeechSpanMs
      ? VOICE_SESSION_LIMITS.hybridReflectiveEndpointSilenceMs
      : VOICE_SESSION_LIMITS.hybridEndpointSilenceMs;
  return now - lastVoiceAt >= requiredSilence;
}

export function advanceVad(
  previous,
  { now, peak, rms },
  {
    intervalMs = VOICE_SESSION_LIMITS.vadIntervalMs,
    minimumVoiceMs = VOICE_SESSION_LIMITS.minimumVoiceMs,
    endOfTurnSilenceMs = VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
    reflectiveEndOfTurnSilenceMs =
      VOICE_SESSION_LIMITS.reflectiveEndOfTurnSilenceMs,
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
    firstVoiceAt,
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    voiceRunMs,
  } = previous;
  const threshold = hasSpeech
    ? Math.max(0.009, noiseFloor * 1.7)
    : Math.max(0.014, noiseFloor * 2.8);
  const peakMultiplier = hasSpeech ? 1.35 : 1.8;
  const soundsVoiced = rms >= threshold && peak >= threshold * peakMultiplier;

  if (soundsVoiced) {
    if (firstVoiceAt === null) {
      firstVoiceAt = timestamp;
    }
    voiceRunMs += intervalMs;
    // Candidate capture starts here. Confirmation still requires the full
    // minimum voice run, but every voiced sample anchors the trailing-silence
    // deadline so a natural pause cannot cut off resumed speech.
    lastVoiceAt = timestamp;
    if (voiceRunMs >= minimumVoiceMs) {
      hasSpeech = true;
    }
  } else {
    if (!hasSpeech) {
      noiseFloor = Math.min(
        0.04,
        Math.max(0.0025, noiseFloor * 0.94 + rms * 0.06),
      );
    }
    // Preserve brief gaps and unvoiced consonants without accepting an
    // isolated noise spike as speech.
    voiceRunMs = Math.max(0, voiceRunMs - intervalMs * 0.5);
    if (!hasSpeech && voiceRunMs === 0) {
      firstVoiceAt = null;
      lastVoiceAt = null;
    }
  }

  let action = null;
  const speechSpanMs =
    firstVoiceAt === null || lastVoiceAt === null
      ? 0
      : lastVoiceAt - firstVoiceAt + intervalMs;
  const trailingSilenceMs =
    speechSpanMs >= reflectiveSpeechSpanMs
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
    const elapsed = timestamp - previous.startedAt;
    if (!hasSpeech && elapsed >= silentCaptureLimitMs) {
      action = "silence";
    } else if (hasSpeech && elapsed >= spokenCaptureLimitMs) {
      action = "duration-limit";
    }
  }

  return Object.freeze({
    action,
    firstVoiceAt,
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    sampleVoiced: soundsVoiced,
    startedAt: previous.startedAt,
    voiceRunMs,
  });
}
