export const VOICE_SESSION_LIMITS = Object.freeze({
  vadIntervalMs: 40,
  minimumVoiceMs: 200,
  endOfTurnSilenceMs: 1_100,
  silentCaptureLimitMs: 30_000,
  spokenCaptureLimitMs: 55_000,
  idleSessionLimitMs: 3 * 60_000,
  maximumSessionMs: 30 * 60_000,
  pendingDocumentLimitMs: 5 * 60_000,
  preRollByteLimit: 64 * 1024,
  preRollChunkLimit: 4,
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

export function createCaptureBuffer({
  maximumBytes,
  preRollByteLimit = VOICE_SESSION_LIMITS.preRollByteLimit,
  preRollChunkLimit = VOICE_SESSION_LIMITS.preRollChunkLimit,
} = {}) {
  if (
    !Number.isSafeInteger(maximumBytes) ||
    maximumBytes <= 0 ||
    !Number.isSafeInteger(preRollByteLimit) ||
    preRollByteLimit <= 0 ||
    preRollByteLimit > maximumBytes ||
    !Number.isSafeInteger(preRollChunkLimit) ||
    preRollChunkLimit <= 0
  ) {
    throw new TypeError("capture_limits_invalid");
  }

  let preRoll = [];
  let preRollBytes = 0;
  let retained = [];
  let retainedBytes = 0;
  let promoted = false;
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
    preRoll.length = 0;
    retained.length = 0;
    preRollBytes = 0;
    retainedBytes = 0;
  }

  function snapshot() {
    return Object.freeze({
      preRollBytes,
      preRollChunks: preRoll.length,
      promoted,
      retainedBytes,
      retainedChunks: retained.length,
      tooLarge,
      totalBytes: promoted ? retainedBytes : preRollBytes,
    });
  }

  return Object.freeze({
    append(chunk, hasSpeech) {
      if (typeof hasSpeech !== "boolean") {
        throw new TypeError("capture_speech_flag_invalid");
      }
      if (tooLarge) return snapshot();
      const size = chunkSize(chunk);

      if (!promoted && !hasSpeech) {
        preRoll.push(chunk);
        preRollBytes += size;
        while (
          preRoll.length > preRollChunkLimit ||
          preRollBytes > preRollByteLimit
        ) {
          const evicted = preRoll.shift();
          preRollBytes -= evicted.size;
        }
        return snapshot();
      }

      if (!promoted) {
        retained = preRoll;
        retainedBytes = preRollBytes;
        preRoll = [];
        preRollBytes = 0;
        promoted = true;
      }
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
      promoted = false;
      tooLarge = false;
      return snapshot();
    },
    snapshot,
    take() {
      const chunks = promoted ? retained.slice() : [];
      const totalBytes = promoted ? retainedBytes : 0;
      clearArrays();
      promoted = false;
      return Object.freeze({ chunks, totalBytes });
    },
  });
}

export function createCapturePhase() {
  let boundaryPending = false;
  let speechConfirmed = false;

  return Object.freeze({
    classifyChunk() {
      if (!speechConfirmed) return "pre-roll";
      if (boundaryPending) {
        boundaryPending = false;
        return "discard-boundary";
      }
      return "retain";
    },
    confirmSpeech() {
      if (speechConfirmed) return false;
      speechConfirmed = true;
      boundaryPending = true;
      return true;
    },
    reset() {
      boundaryPending = false;
      speechConfirmed = false;
    },
    snapshot() {
      return Object.freeze({ boundaryPending, speechConfirmed });
    },
  });
}

export function isValidTurnMode(turnMode) {
  return turnMode === "intentional" || turnMode === "ambient";
}

export function turnModeForGestureEpoch(firstTurnAfterExplicitGesture) {
  if (typeof firstTurnAfterExplicitGesture !== "boolean") {
    throw new TypeError("gesture_epoch_invalid");
  }
  return firstTurnAfterExplicitGesture ? "intentional" : "ambient";
}

export function createVadState(startedAt) {
  return Object.freeze({
    action: null,
    hasSpeech: false,
    lastVoiceAt: null,
    noiseFloor: 0.006,
    startedAt: finiteTimestamp(startedAt, "vad_started_at"),
    voiceRunMs: 0,
  });
}

export function advanceVad(
  previous,
  { now, peak, rms },
  {
    intervalMs = VOICE_SESSION_LIMITS.vadIntervalMs,
    minimumVoiceMs = VOICE_SESSION_LIMITS.minimumVoiceMs,
    endOfTurnSilenceMs = VOICE_SESSION_LIMITS.endOfTurnSilenceMs,
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
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    voiceRunMs,
  } = previous;
  const threshold = Math.max(0.014, noiseFloor * 2.8);
  const soundsVoiced = rms >= threshold && peak >= threshold * 1.8;

  if (soundsVoiced) {
    voiceRunMs += intervalMs;
    if (voiceRunMs >= minimumVoiceMs) {
      hasSpeech = true;
      lastVoiceAt = timestamp;
    }
  } else {
    if (!hasSpeech) {
      noiseFloor = Math.min(
        0.04,
        Math.max(0.0025, noiseFloor * 0.94 + rms * 0.06),
      );
    }
    voiceRunMs = Math.max(0, voiceRunMs - intervalMs * 2);
  }

  let action = null;
  if (
    hasSpeech &&
    lastVoiceAt !== null &&
    timestamp - lastVoiceAt >= endOfTurnSilenceMs
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
    hasSpeech,
    lastVoiceAt,
    noiseFloor,
    startedAt: previous.startedAt,
    voiceRunMs,
  });
}
