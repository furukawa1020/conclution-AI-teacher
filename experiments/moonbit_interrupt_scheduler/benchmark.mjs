import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
const moonPath = path.join(here, "_build", "wasm", "release", "build", "interrupt_scheduler.wasm");
const rustWasmPath = path.join(
  here,
  "rust_reference",
  "target",
  "wasm32-unknown-unknown",
  "release",
  "kotae_interrupt_scheduler_reference.wasm",
);
const [moonBytes, rustBytes] = await Promise.all([readFile(moonPath), readFile(rustWasmPath)]);
const moonStart = performance.now();
const { instance: moon } = await WebAssembly.instantiate(moonBytes, {});
const moonInitMs = performance.now() - moonStart;
const rustStart = performance.now();
const { instance: rust } = await WebAssembly.instantiate(rustBytes, {});
const rustInitMs = performance.now() - rustStart;

function pack(state, signal = 0, fastReady = false) {
  return BigInt(state.phase) |
    (BigInt(state.score + 24) << 3n) |
    (BigInt(state.foregroundMs) << 9n) |
    (BigInt(state.changeCount) << 21n) |
    (BigInt(state.gapMs) << 25n) |
    (BigInt(state.lastBucket) << 32n) |
    (BigInt(state.lastElapsedMs) << 35n) |
    (BigInt(signal) << 47n) |
    (BigInt(fastReady ? 1 : 0) << 49n);
}

function unpack(value) {
  return {
    phase: Number(value & 7n),
    score: Number((value >> 3n) & 63n) - 24,
    foregroundMs: Number((value >> 9n) & 4095n),
    changeCount: Number((value >> 21n) & 15n),
    gapMs: Number((value >> 25n) & 127n),
    lastBucket: Number((value >> 32n) & 7n),
    lastElapsedMs: Number((value >> 35n) & 4095n),
    signal: Number((value >> 47n) & 3n),
    fastReady: ((value >> 49n) & 1n) === 1n,
  };
}

function advanceRust(state, input) {
  const value = rust.exports.advance_intentional_interrupt_packed(
    pack(state),
    input.flags,
    input.rms,
    input.peak,
    input.creditedMs,
    input.elapsedMs,
    input.aec ? 1 : 0,
  );
  if (value === -1n) {
    throw new Error("rust_invalid_state");
  }
  const out = unpack(value);
  return {
    state: {
      phase: out.phase,
      score: out.score,
      foregroundMs: out.foregroundMs,
      changeCount: out.changeCount,
      gapMs: out.gapMs,
      lastBucket: out.lastBucket,
      lastElapsedMs: out.lastElapsedMs,
    },
    signal: out.signal,
    fastReady: out.fastReady,
  };
}

function advanceMoon(state, input) {
  const value = moon.exports.advance_intentional_interrupt(
    pack(state),
    input.flags,
    input.rms,
    input.peak,
    input.creditedMs,
    input.elapsedMs,
    input.aec ? 1 : 0,
  );
  if (value === -1n) {
    throw new Error("moon_invalid_state");
  }
  const out = unpack(value);
  return {
    state: {
      phase: out.phase,
      score: out.score,
      foregroundMs: out.foregroundMs,
      changeCount: out.changeCount,
      gapMs: out.gapMs,
      lastBucket: out.lastBucket,
      lastElapsedMs: out.lastElapsedMs,
    },
    signal: out.signal,
    fastReady: out.fastReady,
  };
}

let seed = 0x6d2b79f5;
function random() {
  seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0;
  return seed / 0x100000000;
}

const initial = {
  phase: 0,
  score: 0,
  foregroundMs: 0,
  changeCount: 0,
  gapMs: 0,
  lastBucket: 0,
  lastElapsedMs: 0,
};
let transitions = 0;
let digest = 0n;
for (let sequence = 0; sequence < 2500; sequence += 1) {
  let rustState = { ...initial };
  let moonState = { ...initial };
  for (let frame = 1; frame <= 40; frame += 1) {
    const input = {
      flags: Math.floor(random() * 8),
      rms: random(),
      peak: random(),
      creditedMs: 1 + Math.floor(random() * 40),
      elapsedMs: frame * 40,
      aec: random() >= 0.05,
    };
    const expected = advanceRust(rustState, input);
    const actual = advanceMoon(moonState, input);
    assert.deepEqual(actual, expected, "transition " + transitions);
    rustState = expected.state;
    moonState = actual.state;
    digest ^= pack(actual.state, actual.signal, actual.fastReady);
    transitions += 1;
  }
}

const benchInputs = [
  { flags: 7, rms: 0.081, peak: 0.22, creditedMs: 40, elapsedMs: 40, aec: true },
  { flags: 7, rms: 0.041, peak: 0.11, creditedMs: 40, elapsedMs: 80, aec: true },
  { flags: 7, rms: 0.079, peak: 0.21, creditedMs: 40, elapsedMs: 120, aec: true },
  { flags: 7, rms: 0.042, peak: 0.12, creditedMs: 40, elapsedMs: 160, aec: true },
];
const warmIterations = 1_000_000;

function benchmarkRust() {
  let state = { ...initial };
  let checksum = 0;
  const start = performance.now();
  for (let index = 0; index < warmIterations; index += 1) {
    const input = benchInputs[index & 3];
    const elapsedMs = (index & 3) * 40 + 40;
    const step = advanceRust(state, { ...input, elapsedMs });
    checksum ^= step.state.score + step.signal;
    state = index % 4 === 3 ? { ...initial } : step.state;
  }
  return { durationMs: performance.now() - start, checksum };
}

function benchmarkMoon() {
  let state = { ...initial };
  let checksum = 0;
  const start = performance.now();
  for (let index = 0; index < warmIterations; index += 1) {
    const input = benchInputs[index & 3];
    const elapsedMs = (index & 3) * 40 + 40;
    const step = advanceMoon(state, { ...input, elapsedMs });
    checksum ^= step.state.score + step.signal;
    state = index % 4 === 3 ? { ...initial } : step.state;
  }
  return { durationMs: performance.now() - start, checksum };
}

const rustRuns = Array.from({ length: 9 }, benchmarkRust);
const moonRuns = Array.from({ length: 9 }, benchmarkMoon);
function quantile(values, q) {
  const sorted = values.toSorted((a, b) => a - b);
  return sorted[Math.ceil((sorted.length - 1) * q)];
}
const result = {
  source: "node-screening-only",
  node: process.version,
  transitions,
  digest: digest.toString(16),
  bytes: {
    moonbitKernel: moonBytes.byteLength,
    rustReferenceKernel: rustBytes.byteLength,
  },
  coldInitMs: { moonbit: moonInitMs, rust: rustInitMs },
  oneMillionStepsMs: {
    moonbit: {
      p50: quantile(moonRuns.map((run) => run.durationMs), 0.5),
      p95: quantile(moonRuns.map((run) => run.durationMs), 0.95),
    },
    rustPackedReference: {
      p50: quantile(rustRuns.map((run) => run.durationMs), 0.5),
      p95: quantile(rustRuns.map((run) => run.durationMs), 0.95),
    },
  },
  checksumsMatch: moonRuns.every((run, index) => run.checksum === rustRuns[index].checksum),
};
console.log(JSON.stringify(result, null, 2));
