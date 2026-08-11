const MAXIMUM_SAFE_FRAME = Number.MAX_SAFE_INTEGER;
let advanceClockInWasm = null;

export function installTemporalVadClockAdvancer(advancer) {
  if (typeof advancer !== "function") {
    throw new TypeError("temporal_vad_clock_advancer_invalid");
  }
  advanceClockInWasm = advancer;
}

export function createTemporalVadClock({ sampleRateHz, startedFrame }) {
  if (
    !Number.isInteger(sampleRateHz) ||
    sampleRateHz < 8_000 ||
    sampleRateHz > 192_000 ||
    !Number.isSafeInteger(startedFrame) ||
    startedFrame < 0 ||
    startedFrame > MAXIMUM_SAFE_FRAME
  ) {
    throw new TypeError("temporal_vad_clock_invalid");
  }
  return Object.freeze({
    lastFrame: startedFrame,
    sampleRateHz,
    startedFrame,
  });
}

export function advanceTemporalVadClock(clock, currentFrame) {
  if (
    clock === null ||
    typeof clock !== "object" ||
    !Number.isSafeInteger(currentFrame) ||
    currentFrame < 0 ||
    typeof advanceClockInWasm !== "function"
  ) {
    throw new TypeError("temporal_vad_clock_unavailable");
  }
  const result = advanceClockInWasm(
    clock.sampleRateHz,
    clock.startedFrame,
    clock.lastFrame,
    currentFrame,
  );
  if (
    !(result instanceof Float64Array) ||
    result.length !== 2 ||
    !Number.isFinite(result[0]) ||
    result[0] <= 0 ||
    result[0] > 40 ||
    !Number.isFinite(result[1]) ||
    result[1] <= 0
  ) {
    throw new TypeError("temporal_vad_clock_result_invalid");
  }
  return Object.freeze({
    clock: Object.freeze({ ...clock, lastFrame: currentFrame }),
    creditedMs: result[0],
    elapsedMs: result[1],
  });
}
