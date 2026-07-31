import { VOICE_SESSION_LIMITS } from "./voice-session-policy.mjs";

const MEBIBYTE = 1024 * 1024;

export const VOICE_STREAM_LIMITS = Object.freeze({
  maximumAudioChunkBytes: MEBIBYTE,
  maximumAudioEventCount: 512,
  maximumAudioTotalBytes: 16 * MEBIBYTE,
  maximumEventCount: 514,
  maximumLineCharacters: 1_400_256,
  maximumResponseBytes: 24 * MEBIBYTE,
});

const OUTPUT_SAMPLE_RATE_HZ = 24_000;
const PCM16_BYTES_PER_SAMPLE = 2;

export const VOICE_PLAYBACK_LIMITS = Object.freeze({
  drainGraceMs: 4_000,
  // This is a local-device wait only. It covers every PCM byte accepted by
  // the bounded response protocol without extending the independent network
  // deadline.
  maximumDrainMs:
    Math.ceil(
      (VOICE_STREAM_LIMITS.maximumAudioTotalBytes /
        (OUTPUT_SAMPLE_RATE_HZ * PCM16_BYTES_PER_SAMPLE)) *
        1_000,
    ) + 4_000,
});

export const INTERRUPT_VAD_LIMITS = Object.freeze({
  // Four required voiced frames may each be separated by at most two 40 ms
  // gaps. Keep the recorder watchdog finite while allowing that full path.
  candidateCaptureLimitMs: 400,
  candidateGapMs: 120,
  // Four 40 ms voiced frames confirm after 120 ms wall-clock from the first
  // detected frame while still requiring 160 ms of sampled speech.
  confirmationMs: 160,
  guardMs: 320,
  intervalMs: 40,
  maximumCaptureMs: 55_000,
  reflectiveSilenceMs: 2_200,
  reflectiveSpeechMs: 1_600,
  trailingSilenceMs: 1_200,
});

export const VOICE_LIVE_LIMITS = Object.freeze({
  inputFrameBytes: 640,
  inputSampleRateHz: 16_000,
  maximumQueuedInputFrames: 200,
  maximumServerTextCharacters: 64 * 1024,
  maximumSocketBufferedBytes: 16 * 1024,
  outboundChunkBytes: 640,
  confirmedSpeechLeadInMs: 300,
  workletCreditWindowFrames: 8,
  workletSealTimeoutMs: 1_500,
  handoffReadyTimeoutMs: 450,
  readyTimeoutMs: 4_000,
  terminalCloseTimeoutMs: 1_500,
});

export const BARGE_PCM_LIMITS = Object.freeze({
  frameDurationMs: 20,
  // The slowest valid gapped confirmation arrives 360 ms after onset. Keep
  // that complete candidate plus the requested 100 ms pre-roll in a fixed
  // 23-frame (14,720 byte) local ring.
  historyMs: 460,
  leadInMs: 100,
  maximumBytes: 14_720,
  maximumFrames: 23,
});

export const CONFIRMED_SPEECH_PCM_LIMITS = Object.freeze({
  frameDurationMs: 20,
  // The ring covers the complete finite quiet-speech candidate window plus
  // the requested 300 ms lead-in. At 16 kHz PCM16 this remains a fixed 48 KB;
  // unconfirmed frames stay local and are zeroized on eviction or discard.
  historyMs:
    VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs +
    VOICE_SESSION_LIMITS.softCandidateCaptureLimitMs,
  maximumBytes: 48_000,
  maximumFrames: 75,
});

function erasePcmFrame(frame) {
  if (frame instanceof ArrayBuffer && frame.byteLength > 0) {
    new Uint8Array(frame).fill(0);
  }
}

function createBoundedPcmRing(limits) {
  let entries = [];
  let lastCapturedAt = null;
  let totalBytes = 0;

  function removeOldest(erase) {
    const removed = entries.shift();
    if (!removed) return;
    totalBytes -= removed.pcm.byteLength;
    if (erase) erasePcmFrame(removed.pcm);
  }

  function clear() {
    while (entries.length > 0) removeOldest(true);
    lastCapturedAt = null;
  }

  return Object.freeze({
    clear,
    drainSince(cutoffAt) {
      if (!Number.isFinite(cutoffAt) || cutoffAt < 0) {
        throw new TypeError("barge_pcm_cutoff_invalid");
      }
      const retained = [];
      for (const entry of entries) {
        if (entry.capturedAt >= cutoffAt) {
          retained.push(entry);
        } else {
          erasePcmFrame(entry.pcm);
        }
      }
      entries = [];
      totalBytes = 0;
      lastCapturedAt = null;
      return Object.freeze(retained);
    },
    push(pcm, capturedAt) {
      if (
        !(pcm instanceof ArrayBuffer) ||
        pcm.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes ||
        !Number.isFinite(capturedAt) ||
        capturedAt < 0 ||
        (lastCapturedAt !== null && capturedAt < lastCapturedAt)
      ) {
        erasePcmFrame(pcm);
        throw new TypeError("barge_pcm_frame_invalid");
      }
      while (
        entries.length > 0 &&
        (entries.length >= limits.maximumFrames ||
          totalBytes + pcm.byteLength > limits.maximumBytes ||
          entries[0].capturedAt <
            capturedAt - limits.historyMs)
      ) {
        removeOldest(true);
      }
      const entry = Object.freeze({ capturedAt, pcm });
      entries.push(entry);
      totalBytes += pcm.byteLength;
      lastCapturedAt = capturedAt;
      return Object.freeze({
        frameCount: entries.length,
        totalBytes,
      });
    },
    snapshot() {
      return Object.freeze({
        frameCount: entries.length,
        newestAt:
          entries.length === 0
            ? null
            : entries[entries.length - 1].capturedAt,
        oldestAt: entries.length === 0 ? null : entries[0].capturedAt,
        totalBytes,
      });
    },
  });
}

export function createBargePcmRing() {
  return createBoundedPcmRing(BARGE_PCM_LIMITS);
}

export function createConfirmedSpeechPcmGate(sendFrame) {
  if (typeof sendFrame !== "function") {
    throw new TypeError("voice_live_gate_sink_invalid");
  }
  const ring = createBoundedPcmRing(CONFIRMED_SPEECH_PCM_LIMITS);
  let closed = false;
  let confirmed = false;

  return Object.freeze({
    clear() {
      if (closed) return;
      closed = true;
      ring.clear();
    },
    confirm(candidateStartedAt) {
      if (
        closed ||
        !Number.isFinite(candidateStartedAt) ||
        candidateStartedAt < 0
      ) {
        throw new TypeError("voice_live_gate_confirmation_invalid");
      }
      if (confirmed) return 0;
      const entries = ring.drainSince(
        Math.max(
          0,
          candidateStartedAt -
            VOICE_LIVE_LIMITS.confirmedSpeechLeadInMs,
        ),
      );
      confirmed = true;
      for (let index = 0; index < entries.length; index += 1) {
        try {
          sendFrame(entries[index].pcm);
        } catch (error) {
          closed = true;
          for (
            let remaining = index;
            remaining < entries.length;
            remaining += 1
          ) {
            erasePcmFrame(entries[remaining].pcm);
          }
          throw error;
        }
      }
      return entries.length;
    },
    push(pcm, capturedAt) {
      if (closed) {
        erasePcmFrame(pcm);
        throw new TypeError("voice_live_gate_closed");
      }
      if (confirmed) {
        try {
          sendFrame(pcm);
        } catch (error) {
          closed = true;
          erasePcmFrame(pcm);
          throw error;
        }
        return;
      }
      ring.push(pcm, capturedAt);
    },
    snapshot() {
      return Object.freeze({
        ...ring.snapshot(),
        closed,
        confirmed,
      });
    },
  });
}

export function ambientHandoffAssignmentAllowed({
  activeRecordingMatches,
  activeSlotEmpty,
  currentEpoch,
  expectedEpoch,
  recordingSettled,
}) {
  if (
    typeof activeRecordingMatches !== "boolean" ||
    typeof activeSlotEmpty !== "boolean" ||
    !Number.isSafeInteger(currentEpoch) ||
    currentEpoch < 0 ||
    !Number.isSafeInteger(expectedEpoch) ||
    expectedEpoch < 0 ||
    typeof recordingSettled !== "boolean"
  ) {
    throw new TypeError("barge_handoff_assignment_invalid");
  }
  return (
    activeRecordingMatches &&
    activeSlotEmpty &&
    currentEpoch === expectedEpoch &&
    !recordingSettled
  );
}

export function claimAmbientLiveHandoff(nextLiveSession, assignment) {
  if (
    nextLiveSession === null ||
    typeof nextLiveSession !== "object" ||
    typeof nextLiveSession.cancel !== "function"
  ) {
    throw new TypeError("barge_handoff_session_invalid");
  }
  if (ambientHandoffAssignmentAllowed(assignment)) {
    return nextLiveSession;
  }
  try {
    nextLiveSession.cancel(new Error("request_cancelled"));
  } catch {
    // A stale candidate remains rejected even if its close races the socket.
  }
  return undefined;
}

export function shouldStartAmbientLiveHandoff({
  captureAvailable,
  finalReceived,
  liveState,
}) {
  if (
    typeof captureAvailable !== "boolean" ||
    typeof finalReceived !== "boolean" ||
    typeof liveState !== "string"
  ) {
    throw new TypeError("barge_handoff_state_invalid");
  }
  return (
    captureAvailable &&
    !finalReceived &&
    liveState === "committed"
  );
}

export function shouldAbortVoiceTransportOnInterrupt(finalReceived) {
  if (typeof finalReceived !== "boolean") {
    throw new TypeError("voice_stream_final_latch_invalid");
  }
  return !finalReceived;
}

export function isCleanVoiceLiveTerminalClose(value) {
  return Boolean(
    isPlainRecord(value) &&
      hasExactKeys(value, ["code", "reason", "wasClean"]) &&
      value.code === 1_000 &&
      value.reason === "complete" &&
      value.wasClean === true,
  );
}

export function validatedPlaybackDrainTimeoutMs(value) {
  if (
    !isPlainRecord(value) ||
    !hasExactKeys(value, [
      "currentContextTime",
      "scheduledEndContextTime",
    ]) ||
    !Number.isFinite(value.currentContextTime) ||
    value.currentContextTime < 0 ||
    !Number.isFinite(value.scheduledEndContextTime) ||
    value.scheduledEndContextTime < 0
  ) {
    throw new TypeError("voice_playback_deadline_invalid");
  }
  const maximumRemainingMs =
    VOICE_PLAYBACK_LIMITS.maximumDrainMs -
    VOICE_PLAYBACK_LIMITS.drainGraceMs;
  const scheduledRemainingMs = Math.max(
    0,
    (value.scheduledEndContextTime - value.currentContextTime) *
      1_000,
  );
  return Math.ceil(
    Math.min(maximumRemainingMs, scheduledRemainingMs) +
      VOICE_PLAYBACK_LIMITS.drainGraceMs,
  );
}

export function estimateAudiblePerformanceTime({
  baseLatencySeconds,
  currentContextTime,
  outputLatencySeconds,
  outputTimestamp,
  performanceNow,
  targetContextTime,
}) {
  if (
    !Number.isFinite(currentContextTime) ||
    currentContextTime < 0 ||
    !Number.isFinite(performanceNow) ||
    performanceNow < 0 ||
    !Number.isFinite(targetContextTime) ||
    targetContextTime < 0
  ) {
    throw new TypeError("voice_audible_time_invalid");
  }

  if (
    outputTimestamp !== null &&
    typeof outputTimestamp === "object" &&
    Number.isFinite(outputTimestamp.contextTime) &&
    outputTimestamp.contextTime >= 0 &&
    Number.isFinite(outputTimestamp.performanceTime) &&
    outputTimestamp.performanceTime > 0 &&
    Math.abs(outputTimestamp.performanceTime - performanceNow) <= 5_000
  ) {
    return (
      outputTimestamp.performanceTime +
      Math.max(
        0,
        targetContextTime - outputTimestamp.contextTime,
      ) *
        1_000
    );
  }

  const outputLatency =
    Number.isFinite(outputLatencySeconds) &&
    outputLatencySeconds >= 0
      ? outputLatencySeconds
      : Number.isFinite(baseLatencySeconds) &&
          baseLatencySeconds >= 0
        ? baseLatencySeconds
        : 0;
  return (
    performanceNow +
    Math.max(0, targetContextTime - currentContextTime) * 1_000 +
    outputLatency * 1_000
  );
}

function eraseLiveCaptureValue(value) {
  if (
    value !== null &&
    typeof value === "object" &&
    value.pcm instanceof ArrayBuffer
  ) {
    erasePcmFrame(value.pcm);
  }
}

export function safeLiveCaptureFrame(
  value,
  { cutoffContextFrame, generation, sequence } = {},
) {
  if (
    !Number.isSafeInteger(cutoffContextFrame) ||
    cutoffContextFrame < 0 ||
    !Number.isSafeInteger(generation) ||
    generation <= 0 ||
    !Number.isSafeInteger(sequence) ||
    sequence < 0 ||
    !isPlainRecord(value) ||
    !hasExactKeys(value, [
      "contextFrame",
      "generation",
      "pcm",
      "sequence",
      "type",
      "version",
    ]) ||
    value.type !== "frame" ||
    value.version !== 1 ||
    value.generation !== generation ||
    value.sequence !== sequence ||
    !Number.isSafeInteger(value.contextFrame) ||
    value.contextFrame < cutoffContextFrame ||
    !(value.pcm instanceof ArrayBuffer) ||
    value.pcm.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes
  ) {
    eraseLiveCaptureValue(value);
    throw new Error("voice_live_frame_invalid");
  }
  return value.pcm;
}

export function safeLiveCaptureSignal(
  value,
  { generation, lastSequence, sealing } = {},
) {
  if (
    !Number.isSafeInteger(generation) ||
    generation <= 0 ||
    !Number.isSafeInteger(lastSequence) ||
    lastSequence < -1 ||
    typeof sealing !== "boolean" ||
    !isPlainRecord(value) ||
    value.version !== 1 ||
    value.generation !== generation
  ) {
    eraseLiveCaptureValue(value);
    throw new Error("voice_live_frame_invalid");
  }
  if (
    value.type === "error" &&
    hasExactKeys(value, ["code", "generation", "type", "version"]) &&
    value.code === "capture_overflow"
  ) {
    return "capture_overflow";
  }
  if (
    sealing &&
    value.type === "sealed" &&
    hasExactKeys(value, [
      "generation",
      "lastSequence",
      "type",
      "version",
    ]) &&
    value.lastSequence === lastSequence
  ) {
    return "sealed";
  }
  eraseLiveCaptureValue(value);
  throw new Error("voice_live_frame_invalid");
}

export function createLivePcmQueue() {
  let frames = [];

  return Object.freeze({
    clear() {
      for (const frame of frames) erasePcmFrame(frame);
      frames = [];
    },
    push(frame) {
      if (
        !(frame instanceof ArrayBuffer) ||
        frame.byteLength !== VOICE_LIVE_LIMITS.inputFrameBytes
      ) {
        erasePcmFrame(frame);
        throw new Error("voice_live_frame_invalid");
      }
      if (frames.length >= VOICE_LIVE_LIMITS.maximumQueuedInputFrames) {
        for (const queued of frames) erasePcmFrame(queued);
        frames = [];
        erasePcmFrame(frame);
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
    !["ambient", "foreground", "intentional"].includes(startFrame.turnMode) ||
    startFrame.sampleRateHz !== VOICE_LIVE_LIMITS.inputSampleRateHz
  ) {
    throw new TypeError("voice_live_start_invalid");
  }

  const queue = createLivePcmQueue();
  let pendingStart = Object.freeze({ ...startFrame });
  let state = "connecting";
  let inputFrameCount = 0;

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
      queue.take(frameCount).forEach((frame, index) => {
        chunk.set(
          new Uint8Array(frame),
          index * VOICE_LIVE_LIMITS.inputFrameBytes,
        );
        erasePcmFrame(frame);
      });
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
      if (inputFrameCount === 0) {
        throw new Error("voice_api_unavailable");
      }
      flush(true);
      if (queue.size() !== 0) {
        queue.clear();
        state = "closed";
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
        erasePcmFrame(frame);
        throw new Error("voice_live_frame_invalid");
      }
      if (state === "ready") {
        queue.push(frame);
        inputFrameCount += 1;
        flush(false);
      } else if (state === "connecting" || state === "awaiting-ready") {
        queue.push(frame);
        inputFrameCount += 1;
      } else {
        erasePcmFrame(frame);
        invalid();
      }
    },
    snapshot() {
      return Object.freeze({
        inputFrameCount,
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
  "voice_turn_timeout",
  "voice_turn_unavailable",
]);

export function createVoiceLiveServerProtocol(validateFinalResult) {
  if (typeof validateFinalResult !== "function") {
    throw new TypeError("validateFinalResult must be a function");
  }
  let audioEventCount = 0;
  let endpointReceived = false;
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
      (state === "ready" || state === "committed") &&
      value.type === "endpoint"
    ) {
      if (
        endpointReceived ||
        (state === "committed" && audioEventCount !== 0) ||
        !hasExactKeys(value, ["type", "version"]) ||
        value.version !== 1
      ) {
        invalid();
      }
      endpointReceived = true;
      return Object.freeze({ type: "endpoint", version: 1 });
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
        endpointReceived,
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
    if (value.type === "error") {
      if (
        !hasExactKeys(value, ["code", "type", "version"]) ||
        value.version !== 1 ||
        !LIVE_SERVER_ERROR_CODES.includes(value.code)
      ) {
        invalid();
      }
      throw new Error(value.code);
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

  // The MediaRecorder watchdog and this deterministic VAD boundary share the
  // same candidate onset. Whichever browser task runs first after a stalled
  // main thread must discard the candidate: otherwise the watchdog can detach
  // the recorder while this state later advances to `confirmed`, leaving
  // playback permanently ducked with no interruption audio owner.
  if (
    phase === "candidate" &&
    now - candidateStartedAt >=
      INTERRUPT_VAD_LIMITS.candidateCaptureLimitMs
  ) {
    return Object.freeze({
      action: "discard",
      candidateSilenceMs: 0,
      candidateStartedAt: null,
      firstVoiceAt: null,
      lastVoiceAt: null,
      noiseFloor,
      phase:
        now - state.startedAt < INTERRUPT_VAD_LIMITS.guardMs
          ? "guard"
          : "armed",
      startedAt: state.startedAt,
      voiceRunMs: 0,
    });
  }

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
