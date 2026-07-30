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
  candidateGapMs: 120,
  // Four 40 ms voiced frames confirm after 120 ms wall-clock from the first
  // detected frame while still requiring 160 ms of sampled speech.
  confirmationMs: 160,
  guardMs: 320,
  intervalMs: 40,
  maximumCaptureMs: 55_000,
  reflectiveSilenceMs: 1_700,
  reflectiveSpeechMs: 2_400,
  trailingSilenceMs: 1_100,
});

export const VOICE_LIVE_LIMITS = Object.freeze({
  inputFrameBytes: 640,
  inputSampleRateHz: 16_000,
  maximumQueuedInputFrames: 200,
  maximumServerTextCharacters: 64 * 1024,
  maximumSocketBufferedBytes: 16 * 1024,
  outboundChunkBytes: 3_200,
  readyTimeoutMs: 4_000,
});

export function shouldAbortVoiceTransportOnInterrupt(finalReceived) {
  if (typeof finalReceived !== "boolean") {
    throw new TypeError("voice_stream_final_latch_invalid");
  }
  return !finalReceived;
}

export function safeLiveCaptureFrame(value) {
  if (
    !isPlainRecord(value) ||
    !hasExactKeys(value, [
      "pcm",
      "sampleRateHz",
      "type",
      "version",
    ]) ||
    value.type !== "frame" ||
    value.version !== 1 ||
    value.sampleRateHz !== VOICE_LIVE_LIMITS.inputSampleRateHz ||
    !(value.pcm instanceof ArrayBuffer) ||
    value.pcm.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes
  ) {
    throw new Error("voice_live_frame_invalid");
  }
  return value.pcm;
}

export function createLivePcmQueue() {
  let frames = [];

  return Object.freeze({
    clear() {
      frames = [];
    },
    push(frame) {
      if (
        !(frame instanceof ArrayBuffer) ||
        frame.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes
      ) {
        throw new Error("voice_live_frame_invalid");
      }
      if (frames.length >= VOICE_LIVE_LIMITS.maximumQueuedInputFrames) {
        frames = [];
        throw new Error("voice_live_queue_overflow");
      }
      frames.push(frame);
      return frames.length;
    },
    take(maximum = frames.length) {
      if (!Number.isSafeInteger(maximum) || maximum < 0) {
        throw new TypeError("voice_live_queue_take_invalid");
      }
      const taken = frames.slice(0, maximum);
      frames = frames.slice(taken.length);
      return taken;
    },
    size() {
      return frames.length;
    },
  });
}

export function createVoiceLiveClientTransport(socket, startFrame) {
  if (
    !isPlainRecord(socket) &&
    (socket === null || typeof socket !== "object")
  ) {
    throw new TypeError("voice_live_socket_invalid");
  }
  if (
    typeof socket.send !== "function" ||
    !isPlainRecord(startFrame) ||
    !hasExactKeys(startFrame, [
      "appCheckToken",
      "idToken",
      "sampleRateHz",
      "sessionState",
      "turnMode",
      "type",
      "version",
    ]) ||
    startFrame.type !== "start" ||
    startFrame.version !== 1 ||
    typeof startFrame.idToken !== "string" ||
    startFrame.idToken.length === 0 ||
    typeof startFrame.appCheckToken !== "string" ||
    startFrame.appCheckToken.length === 0 ||
    typeof startFrame.sessionState !== "string" ||
    !["ambient", "intentional"].includes(startFrame.turnMode) ||
    startFrame.sampleRateHz !== VOICE_LIVE_LIMITS.inputSampleRateHz
  ) {
    throw new TypeError("voice_live_start_invalid");
  }

  const queue = createLivePcmQueue();
  let pendingStart = Object.freeze({ ...startFrame });
  let state = "connecting";

  function socketReady() {
    if (
      socket.readyState !== 1 ||
      !Number.isFinite(socket.bufferedAmount) ||
      socket.bufferedAmount < 0
    ) {
      throw new Error("voice_api_unavailable");
    }
  }

  function sendText(value) {
    socketReady();
    if (
      socket.bufferedAmount >
      VOICE_LIVE_LIMITS.maximumSocketBufferedBytes
    ) {
      throw new Error("voice_api_unavailable");
    }
    socket.send(value);
  }

  function flush(allowPartial) {
    socketReady();
    const framesPerChunk =
      VOICE_LIVE_LIMITS.outboundChunkBytes /
      VOICE_LIVE_LIMITS.inputFrameBytes;
    while (
      queue.size() >= framesPerChunk ||
      (allowPartial && queue.size() > 0)
    ) {
      const frameCount = Math.min(framesPerChunk, queue.size());
      const chunkBytes =
        frameCount * VOICE_LIVE_LIMITS.inputFrameBytes;
      if (
        socket.bufferedAmount + chunkBytes >
        VOICE_LIVE_LIMITS.maximumSocketBufferedBytes
      ) {
        break;
      }
      const chunk = new Uint8Array(chunkBytes);
      queue
        .take(frameCount)
        .forEach((frame, index) =>
          chunk.set(
            new Uint8Array(frame),
            index * VOICE_LIVE_LIMITS.inputFrameBytes,
          ),
        );
      socket.send(chunk.buffer);
    }
  }

  return Object.freeze({
    close() {
      pendingStart = undefined;
      queue.clear();
      state = "closed";
    },
    commit() {
      if (state !== "ready") invalid();
      flush(true);
      if (queue.size() !== 0) {
        throw new Error("voice_api_unavailable");
      }
      sendText(JSON.stringify({ type: "commit", version: 1 }));
      state = "committed";
    },
    markReady() {
      if (state !== "awaiting-ready") invalid();
      state = "ready";
      flush(false);
    },
    open() {
      if (state !== "connecting" || pendingStart === undefined) invalid();
      sendText(JSON.stringify(pendingStart));
      pendingStart = undefined;
      state = "awaiting-ready";
    },
    pushFrame(frame) {
      if (
        !(frame instanceof ArrayBuffer) ||
        frame.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes
      ) {
        throw new Error("voice_live_frame_invalid");
      }
      if (state === "ready") {
        queue.push(frame);
        flush(false);
      } else if (state === "connecting" || state === "awaiting-ready") {
        queue.push(frame);
      } else {
        invalid();
      }
    },
    snapshot() {
      return Object.freeze({
        queuedFrames: queue.size(),
        state,
      });
    },
  });
}

const LIVE_SERVER_ERROR_CODES = Object.freeze([
  "authentication_failed",
  "no_speech",
  "rate_limited",
  "voice_api_unavailable",
  "voice_response_invalid",
  "voice_turn_invalid",
  "voice_turn_too_large",
]);

export function createVoiceLiveServerProtocol(validateFinalResult) {
  if (typeof validateFinalResult !== "function") {
    throw new TypeError("validateFinalResult must be a function");
  }
  let audioEventCount = 0;
  let state = "awaiting-ready";
  let totalAudioBytes = 0;

  function acceptText(text) {
    if (
      typeof text !== "string" ||
      text.length === 0 ||
      text.length > VOICE_LIVE_LIMITS.maximumServerTextCharacters ||
      state === "final" ||
      state === "error"
    ) {
      invalid();
    }
    let value;
    try {
      value = JSON.parse(text);
    } catch {
      invalid();
    }
    if (!isPlainRecord(value) || typeof value.type !== "string") {
      invalid();
    }
    if (value.type === "error") {
      if (
        !hasExactKeys(value, ["code", "type", "version"]) ||
        value.version !== 1 ||
        !LIVE_SERVER_ERROR_CODES.includes(value.code)
      ) {
        invalid();
      }
      state = "error";
      return Object.freeze({
        code: value.code,
        type: "error",
        version: 1,
      });
    }
    if (state === "awaiting-ready") {
      const event = safeReadyEvent(value);
      state = "ready";
      return event;
    }
    if (
      state !== "committed" ||
      value.type !== "final" ||
      !hasExactKeys(value, ["result", "type", "version"]) ||
      value.version !== 1 ||
      !isPlainRecord(value.result)
    ) {
      invalid();
    }
    const expectedAudioMIME =
      audioEventCount === 0 ? "" : "audio/L16";
    if (
      value.result.audioBase64 !== "" ||
      value.result.audioMimeType !== expectedAudioMIME
    ) {
      invalid();
    }
    const result = validateFinalResult(value.result);
    state = "final";
    return Object.freeze({
      result,
      type: "final",
      version: 1,
    });
  }

  function acceptBinary(value) {
    if (
      state !== "committed" ||
      !(value instanceof ArrayBuffer) ||
      value.byteLength === 0 ||
      value.byteLength % 2 !== 0 ||
      value.byteLength > VOICE_STREAM_LIMITS.maximumAudioChunkBytes ||
      audioEventCount >= VOICE_STREAM_LIMITS.maximumAudioEventCount ||
      totalAudioBytes + value.byteLength >
        VOICE_STREAM_LIMITS.maximumAudioTotalBytes
    ) {
      invalid();
    }
    const event = Object.freeze({
      pcm: value,
      sampleRateHz: 24_000,
      sequence: audioEventCount,
      type: "audio",
      version: 1,
    });
    audioEventCount += 1;
    totalAudioBytes += value.byteLength;
    return event;
  }

  function markCommitted() {
    if (state !== "ready") invalid();
    state = "committed";
  }

  return Object.freeze({
    acceptBinary,
    acceptText,
    markCommitted,
    snapshot() {
      return Object.freeze({
        audioEventCount,
        state,
        totalAudioBytes,
      });
    },
  });
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
      "version",
    ]) ||
    value.type !== "audio" ||
    value.version !== 1 ||
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
    version: 1,
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
    candidateSilenceMs: 0,
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
    !Number.isFinite(state.candidateSilenceMs) ||
    state.candidateSilenceMs < 0 ||
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
    candidateSilenceMs,
    candidateStartedAt,
    firstVoiceAt,
    lastVoiceAt,
    noiseFloor,
    phase,
    voiceRunMs,
  } = state;
  let action = null;

  if (now - state.startedAt < INTERRUPT_VAD_LIMITS.guardMs) {
    const guardVoiced =
      rms >=
        Math.max(outputActive ? 0.05 : 0.03, noiseFloor * 4.5) &&
      peak >=
        Math.max(outputActive ? 0.12 : 0.08, noiseFloor * 9);
    if (guardVoiced) {
      if (phase !== "candidate") {
        phase = "candidate";
        candidateStartedAt = now;
        candidateSilenceMs = 0;
        voiceRunMs = 0;
        action = "start";
      }
      candidateSilenceMs = 0;
      voiceRunMs += INTERRUPT_VAD_LIMITS.intervalMs;
    } else if (phase === "candidate") {
      candidateSilenceMs += INTERRUPT_VAD_LIMITS.intervalMs;
      if (
        candidateSilenceMs >= INTERRUPT_VAD_LIMITS.candidateGapMs
      ) {
        action = "discard";
        phase = "guard";
        candidateStartedAt = null;
        candidateSilenceMs = 0;
        voiceRunMs = 0;
      }
    } else {
      noiseFloor = clampNoiseFloor(noiseFloor * 0.72 + rms * 0.28);
    }
    return Object.freeze({
      action,
      candidateSilenceMs,
      candidateStartedAt,
      firstVoiceAt: null,
      lastVoiceAt: null,
      noiseFloor,
      phase,
      startedAt: state.startedAt,
      voiceRunMs,
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
      candidateSilenceMs = 0;
      voiceRunMs = 0;
      action = "start";
    }
    candidateSilenceMs = 0;
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
      candidateSilenceMs += INTERRUPT_VAD_LIMITS.intervalMs;
      if (
        candidateSilenceMs < INTERRUPT_VAD_LIMITS.candidateGapMs
      ) {
        return Object.freeze({
          action,
          candidateSilenceMs,
          candidateStartedAt,
          firstVoiceAt,
          lastVoiceAt,
          noiseFloor,
          phase,
          startedAt: state.startedAt,
          voiceRunMs,
        });
      }
      action = "discard";
    }
    phase = "armed";
    candidateSilenceMs = 0;
    candidateStartedAt = null;
    voiceRunMs = 0;
  }

  return Object.freeze({
    action,
    candidateSilenceMs,
    candidateStartedAt,
    firstVoiceAt,
    lastVoiceAt,
    noiseFloor,
    phase,
    startedAt: state.startedAt,
    voiceRunMs,
  });
}
