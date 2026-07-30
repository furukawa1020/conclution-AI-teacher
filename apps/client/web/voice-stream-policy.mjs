const MEBIBYTE = 1024 * 1024;

export const VOICE_STREAM_LIMITS = Object.freeze({
  maximumAudioChunkBytes: MEBIBYTE,
  maximumAudioTotalBytes: 16 * MEBIBYTE,
  maximumEventCount: 512,
  maximumLineCharacters: 1_400_256,
  maximumResponseBytes: 24 * MEBIBYTE,
  maximumTransportChunkBytes: 2 * MEBIBYTE,
});

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
    if (
      value.type !== "final" ||
      !hasExactKeys(value, ["result", "type", "version"]) ||
      value.version !== 1 ||
      !isPlainRecord(value.result) ||
      value.result.audioBase64 !== "" ||
      value.result.audioMimeType !== "audio/L16"
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
    buffered += text;
    if (buffered.length > VOICE_STREAM_LIMITS.maximumLineCharacters) {
      invalid();
    }

    const events = [];
    for (;;) {
      const newline = buffered.indexOf("\n");
      if (newline < 0) break;
      const line = buffered.slice(0, newline);
      buffered = buffered.slice(newline + 1);
      events.push(parseLine(line));
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
