const MEBIBYTE = 1024 * 1024;

export const VOICE_STREAM_LIMITS = Object.freeze({
  maximumAudioChunkBytes: MEBIBYTE,
  maximumAudioEventCount: 512,
  maximumAudioTotalBytes: 16 * MEBIBYTE,
  maximumEventCount: 514,
  maximumLineCharacters: 1_400_256,
  maximumResponseBytes: 24 * MEBIBYTE,
});

export const INTERRUPT_VAD_LIMITS = Object.freeze({
  confirmationMs: 240,
  guardMs: 320,
  intervalMs: 40,
  maximumCaptureMs: 55_000,
  reflectiveSilenceMs: 1_700,
  reflectiveSpeechMs: 2_400,
  trailingSilenceMs: 1_100,
});

export function shouldAbortVoiceTransportOnInterrupt(finalReceived) {
  if (typeof finalReceived !== "boolean") {
    throw new TypeError("voice_stream_final_latch_invalid");
  }
  return !finalReceived;
}

function invalid() {
  throw new Error("voice_response_invalid");
}

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function hasExactKeys(value, expected) {
  const keys = Object.keys(value).sort();
  const sortedExpected = [...expected].sort();
  return (
    keys.length === sortedExpected.length &&
    keys.every((key, index) => key === sortedExpected[index])
  );
}

function decodedBase64Length(value) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length % 4 !== 0 ||
    !/^[A-Za-z0-9+/]*={0,2}$/.test(value)
  ) {
    invalid();
  }
  const padding = value.endsWith("==") ? 2 : value.endsWith("=") ? 1 : 0;
  return (value.length / 4) * 3 - padding;
}

function safeReadyEvent(value) {
  if (
    !hasExactKeys(value, ["type", "version"]) ||
    value.type !== "ready" ||
    value.version !== 1
  ) {
    invalid();
  }
  return Object.freeze({ type: "ready", version: 1 });
}

function safeAudioEvent(value, expectedSequence, totalAudioBytes) {
  if (
    !hasExactKeys(value, [
      "audioBase64",
      "sampleRateHz",
      "sequence",
      "type",
    ]) ||
    value.type !== "audio" ||
    !Number.isSafeInteger(value.sequence) ||
    value.sequence !== expectedSequence ||
    expectedSequence >= VOICE_STREAM_LIMITS.maximumAudioEventCount ||
    value.sampleRateHz !== 24_000
  ) {
    invalid();
  }
  const decodedBytes = decodedBase64Length(value.audioBase64);
  if (
    decodedBytes === 0 ||
    decodedBytes % 2 !== 0 ||
    decodedBytes > VOICE_STREAM_LIMITS.maximumAudioChunkBytes ||
    totalAudioBytes + decodedBytes >
      VOICE_STREAM_LIMITS.maximumAudioTotalBytes
  ) {
    invalid();
  }
  return Object.freeze({
    audioBase64: value.audioBase64,
    decodedBytes,
    sampleRateHz: 24_000,
    sequence: value.sequence,
    type: "audio",
  });
}

export function createVoiceStreamParser(validateFinalResult) {
  if (typeof validateFinalResult !== "function") {
    throw new TypeError("validateFinalResult must be a function");
  }

  let buffered = "";
  let eventCount = 0;
  let expectedSequence = 0;
  let finalResult;
  let ready = false;
  let totalAudioBytes = 0;

  function parseLine(line) {
    if (
      line.length === 0 ||
      line.length > VOICE_STREAM_LIMITS.maximumLineCharacters ||
      finalResult !== undefined
    ) {
      invalid();
    }
    eventCount += 1;
    if (eventCount > VOICE_STREAM_LIMITS.maximumEventCount) {
      invalid();
    }

    let value;
    try {
      value = JSON.parse(line);
    } catch {
      invalid();
    }
    if (!isPlainRecord(value) || typeof value.type !== "string") {
      invalid();
    }

    if (!ready) {
      const event = safeReadyEvent(value);
      ready = true;
      return event;
    }
    if (value.type === "audio") {
      const event = safeAudioEvent(
        value,
        expectedSequence,
        totalAudioBytes,
      );
      expectedSequence += 1;
      totalAudioBytes += event.decodedBytes;
      return event;
    }
    const expectedAudioMIME =
      expectedSequence === 0 ? "" : "audio/L16";
    if (
      value.type !== "final" ||
      !hasExactKeys(value, ["result", "type", "version"]) ||
      value.version !== 1 ||
      !isPlainRecord(value.result) ||
      value.result.audioBase64 !== "" ||
      value.result.audioMimeType !== expectedAudioMIME
    ) {
      invalid();
    }
    finalResult = validateFinalResult(value.result);
    return Object.freeze({
      result: finalResult,
      type: "final",
      version: 1,
    });
  }

  function push(text) {
    if (typeof text !== "string") {
      invalid();
    }
    if (finalResult !== undefined && text.length > 0) {
      invalid();
    }
    const events = [];
    let offset = 0;
    for (;;) {
      const newline = text.indexOf("\n", offset);
      if (newline < 0) {
        buffered += text.slice(offset);
        if (buffered.length > VOICE_STREAM_LIMITS.maximumLineCharacters) {
          invalid();
        }
        break;
      }
      const segment = text.slice(offset, newline);
      if (
        buffered.length + segment.length >
        VOICE_STREAM_LIMITS.maximumLineCharacters
      ) {
        invalid();
      }
      const line = buffered + segment;
      buffered = "";
      events.push(parseLine(line));
      offset = newline + 1;
    }
    return events;
  }

  function finish() {
    const events = [];
    if (buffered.length > 0) {
      const line = buffered;
      buffered = "";
      events.push(parseLine(line));
    }
    if (!ready || finalResult === undefined) {
      invalid();
    }
    return Object.freeze({
      audioEventCount: expectedSequence,
      events: Object.freeze(events),
      finalResult,
      totalAudioBytes,
    });
  }

  return Object.freeze({ finish, push });
}

function boundedLevel(value) {
  return (
    typeof value === "number" &&
    Number.isFinite(value) &&
    value >= 0 &&
    value <= 1
  );
}

function clampNoiseFloor(value) {
  return Math.min(0.08, Math.max(0.002, value));
}

export function createInterruptVadState(startedAt) {
  if (!Number.isFinite(startedAt) || startedAt < 0) {
    throw new TypeError("interrupt_vad_time_invalid");
  }
  return Object.freeze({
    action: null,
    candidateStartedAt: null,
    firstVoiceAt: null,
    lastVoiceAt: null,
    noiseFloor: 0.004,
    phase: "guard",
    startedAt,
    voiceRunMs: 0,
  });
}

export function advanceInterruptVad(
  state,
  { now, outputActive, peak, rms },
) {
  const finiteOrNull = (value) =>
    value === null || (Number.isFinite(value) && value >= state.startedAt);
  if (
    !isPlainRecord(state) ||
    !Number.isFinite(state.startedAt) ||
    state.startedAt < 0 ||
    !boundedLevel(state.noiseFloor) ||
    !Number.isFinite(state.voiceRunMs) ||
    state.voiceRunMs < 0 ||
    !finiteOrNull(state.candidateStartedAt) ||
    !finiteOrNull(state.firstVoiceAt) ||
    !finiteOrNull(state.lastVoiceAt) ||
    (state.phase === "confirmed" &&
      (state.firstVoiceAt === null || state.lastVoiceAt === null)) ||
    (state.phase === "candidate" && state.candidateStartedAt === null) ||
    !Number.isFinite(now) ||
    now < state.startedAt ||
    typeof outputActive !== "boolean" ||
    !boundedLevel(peak) ||
    !boundedLevel(rms) ||
    !["guard", "armed", "candidate", "confirmed"].includes(state.phase)
  ) {
    throw new TypeError("interrupt_vad_state_invalid");
  }

  let {
    candidateStartedAt,
    firstVoiceAt,
    lastVoiceAt,
    noiseFloor,
    phase,
    voiceRunMs,
  } = state;
  let action = null;

  if (now - state.startedAt < INTERRUPT_VAD_LIMITS.guardMs) {
    noiseFloor = clampNoiseFloor(noiseFloor * 0.72 + rms * 0.28);
    return Object.freeze({
      action,
      candidateStartedAt: null,
      firstVoiceAt: null,
      lastVoiceAt: null,
      noiseFloor,
      phase: "guard",
      startedAt: state.startedAt,
      voiceRunMs: 0,
    });
  }
  if (phase === "guard") phase = "armed";

  const rmsThreshold = Math.max(
    outputActive ? 0.026 : 0.014,
    noiseFloor * (outputActive ? 3.2 : 2.35),
  );
  const peakThreshold = Math.max(
    outputActive ? 0.065 : 0.035,
    noiseFloor * (outputActive ? 7 : 5),
  );
  const voiced = rms >= rmsThreshold && peak >= peakThreshold;

  if (phase === "confirmed") {
    if (voiced) {
      lastVoiceAt = now;
    } else {
      noiseFloor = clampNoiseFloor(noiseFloor * 0.96 + rms * 0.04);
    }
    const spokenFor = now - firstVoiceAt;
    const silenceLimit =
      spokenFor >= INTERRUPT_VAD_LIMITS.reflectiveSpeechMs
        ? INTERRUPT_VAD_LIMITS.reflectiveSilenceMs
        : INTERRUPT_VAD_LIMITS.trailingSilenceMs;
    if (spokenFor >= INTERRUPT_VAD_LIMITS.maximumCaptureMs) {
      action = "duration-limit";
    } else if (now - lastVoiceAt >= silenceLimit) {
      action = "end-of-turn";
    }
  } else if (voiced) {
    if (phase !== "candidate") {
      phase = "candidate";
      candidateStartedAt = now;
      voiceRunMs = 0;
      action = "start";
    }
    voiceRunMs += INTERRUPT_VAD_LIMITS.intervalMs;
    if (voiceRunMs >= INTERRUPT_VAD_LIMITS.confirmationMs) {
      phase = "confirmed";
      firstVoiceAt = candidateStartedAt;
      lastVoiceAt = now;
      action = "confirm";
    }
  } else {
    noiseFloor = clampNoiseFloor(noiseFloor * 0.92 + rms * 0.08);
    if (phase === "candidate") {
      action = "discard";
    }
    phase = "armed";
    candidateStartedAt = null;
    voiceRunMs = 0;
  }

  return Object.freeze({
    action,
    candidateStartedAt,
    firstVoiceAt,
    lastVoiceAt,
    noiseFloor,
    phase,
    startedAt: state.startedAt,
    voiceRunMs,
  });
}
