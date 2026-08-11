import assert from "node:assert/strict";
import test from "node:test";

import { installTemporalVadClockAdvancer } from "../web/temporal-vad-clock.mjs";
import {
  advanceInterruptVad,
  createInterruptVadState,
  installIntentionalInterruptAdvancer,
  installInterruptFrameClassifier,
} from "../web/voice-stream-policy.mjs";

let frameFlags = 0b111;
installInterruptFrameClassifier(() => frameFlags);
installTemporalVadClockAdvancer((rate, started, previous, current) => {
  if (current <= previous) throw new TypeError("clock_invalid");
  return new Float64Array([
    Math.min(40, ((current - previous) * 1_000) / rate),
    ((current - started) * 1_000) / rate,
  ]);
});
installIntentionalInterruptAdvancer((phase, score, foregroundMs, changes, gapMs, lastBucket, lastElapsedMs, flags, rms, peak, credit, elapsed, aec) => {
  if (elapsed <= lastElapsedMs) throw new TypeError("clock_invalid");
  if ([5, 6].includes(phase)) return new Float64Array([phase, score, foregroundMs, changes, gapMs, lastBucket, elapsed, 0, 0]);
  if (!aec) return new Float64Array([5, score, foregroundMs, changes, gapMs, lastBucket, elapsed, 0, 0]);
  if (elapsed > 520) return new Float64Array([5, score, foregroundMs, changes, gapMs, lastBucket, elapsed, 0, 0]);
  const foreground = (flags & 1) !== 0 && (flags & 4) !== 0;
  const voiced = (flags & 2) !== 0;
  const bucket = foreground
    ? rms >= 0.075 || peak >= 0.2 ? 4 : rms >= 0.055 || peak >= 0.15 ? 3 : rms >= 0.038 || peak >= 0.105 ? 2 : 1
    : 0;
  const changed = foreground && lastBucket !== 0 && Math.abs(bucket - lastBucket) >= 2;
  let signal = 0;
  if (changed) {
    signal = 3; score = Math.min(32, score + 6); changes = Math.min(8, changes + 1);
  } else if (foreground) {
    signal = 2; score = Math.min(32, score + 2);
  } else if (voiced) {
    signal = 1; score = Math.max(-24, score - 6); phase = 5;
  } else {
    score = Math.max(-24, score - 5); gapMs += credit;
    if (gapMs > 80 || score <= -12) { phase = 6; gapMs = 80; }
  }
  if (foreground) { foregroundMs += credit; gapMs = 0; lastBucket = bucket; }
  if (phase === 0 && foreground) phase = 1;
  if (phase === 1 && foregroundMs >= 160 && changes >= 1 && score >= 10) phase = 2;
  if ([1, 2, 3].includes(phase) && foregroundMs >= 320 && changes >= 3 && score >= 24 && gapMs <= 80) phase = 3;
  const ready = phase === 3 && elapsed >= 400 && elapsed <= 520;
  if (ready) phase = 4;
  return new Float64Array([phase, score, foregroundMs, changes, gapMs, lastBucket, elapsed, signal, Number(ready)]);
});

function runTrace({ aecVerified, levels, ticks }) {
  let state = createInterruptVadState(0, { sampleRateHz: 48_000, startedFrame: 0 });
  for (let index = 0; index < ticks; index += 1) {
    const level = levels(index);
    frameFlags = level.flags;
    state = advanceInterruptVad(state, {
      clockFrame: (index + 1) * 1_920,
      now: (index + 1) * 40,
      outputActive: true,
      peak: level.peak,
      rms: level.rms,
    }, {
      confirmationAllowed: true,
      fastLaneAllowed: aecVerified(index),
    });
    if (state.action === "confirm") return { decisionMs: (index + 1) * 40, state };
  }
  return { decisionMs: null, state };
}

const changingForeground = (index) => index % 2 === 0
  ? { flags: 0b111, peak: 0.12, rms: 0.045 }
  : { flags: 0b111, peak: 0.21, rms: 0.08 };

test("AEC verified changing foreground confirms at 400 ms", () => {
  const result = runTrace({ aecVerified: () => true, levels: changingForeground, ticks: 13 });
  assert.equal(result.decisionMs, 400);
  assert.equal(result.state.phase, "confirmed");
});

test("constant foreground mutter never uses fast lane and keeps 720 ms", () => {
  const result = runTrace({
    aecVerified: () => true,
    levels: () => ({ flags: 0b111, peak: 0.16, rms: 0.06 }),
    ticks: 18,
  });
  assert.equal(result.decisionMs, 720);
});

test("quiet mutter and AEC loss produce zero fast confirmations", () => {
  const quiet = runTrace({
    aecVerified: () => true,
    levels: () => ({ flags: 0b011, peak: 0.05, rms: 0.02 }),
    ticks: 15,
  });
  assert.equal(quiet.decisionMs, null);
  const lost = runTrace({
    aecVerified: (index) => index < 6,
    levels: changingForeground,
    ticks: 13,
  });
  assert.equal(lost.decisionMs, null);
  assert.equal(lost.state.intentionalFastLane.phase, 5);
});

test("cough, impulse, sparse mutter, and playback leakage never fast confirm", () => {
  const traces = [
    (index) => index < 3 ? changingForeground(index) : { flags: 0, peak: 0.006, rms: 0.003 },
    (index) => index === 0 ? changingForeground(index) : { flags: 0, peak: 0.006, rms: 0.003 },
    (index) => index % 5 < 2 ? changingForeground(index) : { flags: 0, peak: 0.006, rms: 0.003 },
    (index) => ({ ...changingForeground(index), flags: 0b110 }),
  ];
  for (const levels of traces) {
    assert.equal(runTrace({ aecVerified: () => true, levels, ticks: 18 }).decisionMs, null);
  }
});
