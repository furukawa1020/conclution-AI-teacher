import assert from "node:assert/strict";
import test from "node:test";

import { advanceTemporalVadClock, createTemporalVadClock, installTemporalVadClockAdvancer } from "../web/temporal-vad-clock.mjs";
import { advanceVad, createVadState } from "../web/voice-session-policy.mjs";
import { advanceInterruptVad, createInterruptVadState, installIntentionalInterruptAdvancer, installInterruptFrameClassifier } from "../web/voice-stream-policy.mjs";

function rustClockOracle(rate, started, previous, current) {
  if (rate < 8_000 || rate > 192_000 || started > previous || current <= previous) {
    throw new TypeError("temporal VAD clock must be monotonic");
  }
  return new Float64Array([
    Math.min(40, ((current - previous) * 1_000) / rate),
    ((current - started) * 1_000) / rate,
  ]);
}

installTemporalVadClockAdvancer(rustClockOracle);
installInterruptFrameClassifier(() => 0b111);
installIntentionalInterruptAdvancer((phase, score, foregroundMs, changeCount, gapMs, lastBucket, _lastElapsedMs, _flags, _rms, _peak, creditedMs, elapsedMs, aecVerified) =>
  new Float64Array(aecVerified
    ? [phase, score, foregroundMs + creditedMs, changeCount, gapMs, lastBucket, elapsedMs, 2, 0]
    : [5, score, foregroundMs, changeCount, gapMs, lastBucket, elapsedMs, 0, 0]));

test("sample clock rejects duplicate and reverse ticks", () => {
  const clock = createTemporalVadClock({ sampleRateHz: 48_000, startedFrame: 1_000 });
  const first = advanceTemporalVadClock(clock, 2_920);
  assert.equal(first.creditedMs, 40);
  assert.equal(first.elapsedMs, 40);
  assert.throws(() => advanceTemporalVadClock(first.clock, 2_920));
  assert.throws(() => advanceTemporalVadClock(first.clock, 2_919));
});

test("a delayed JS task cannot manufacture normal-VAD dwell", () => {
  let state = createVadState(10_000, { sampleRateHz: 48_000, startedFrame: 0 });
  state = advanceVad(state, {
    clockFrame: 48_000,
    now: 99_000,
    peak: 0.08,
    rms: 0.03,
  });
  assert.equal(state.clearVoiceRunMs, 40);
  assert.equal(state.hasSpeech, false);
  assert.equal(state.temporalClock.lastFrame, 48_000);
});

test("interrupt confirmation remains sample-clock dwell bounded", () => {
  let state = createInterruptVadState(1_000, { sampleRateHz: 48_000, startedFrame: 0 });
  state = advanceInterruptVad(state, {
    clockFrame: 48_000,
    now: 90_000,
    outputActive: false,
    peak: 0.2,
    rms: 0.08,
  });
  assert.equal(state.phase, "candidate");
  assert.equal(state.voiceRunMs, 40);
  assert.notEqual(state.action, "confirm");
  assert.throws(() => advanceInterruptVad(state, {
    clockFrame: 48_000,
    now: 90_001,
    outputActive: false,
    peak: 0.2,
    rms: 0.08,
  }));
});
