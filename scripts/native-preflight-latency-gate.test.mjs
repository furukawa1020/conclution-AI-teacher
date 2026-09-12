import assert from "node:assert/strict";
import test from "node:test";
import {
  evaluateNativePreflightBenchmark,
  NATIVE_PREFLIGHT_BENCHMARK_POLICY,
  NATIVE_PREFLIGHT_BENCHMARK_VERSION,
} from "./native-preflight-latency-gate.mjs";

const commit = "0123456789abcdef0123456789abcdef01234567";

function artifact(run = {}) {
  const makeRuns = (warm) => Array.from({ length: 100 }, (_, index) => ({
    estimatedCostMicroUsd: 100,
    fallback: false,
    index: index + 1,
    latencyMs: warm ? 600 : 2_000,
    unusedConnectionMs: 20,
    ...(warm ? { speechEndConnectionWaitMs: 0 } : {}),
    ...run,
  }));
  return {
    coldRuns: makeRuns(false),
    environment: {
      browser: "Google Chrome",
      browserVersion: "140.0.7339.81",
      cloudRegion: "asia-northeast1",
      deviceModel: "reference-desktop-1",
      measuredAt: "2026-09-12T03:00:00Z",
      networkCondition: "unshaped",
      operatingSystem: "Windows 11 24H2",
      providerRegion: "us-central1",
      runtimeRevision: "kotae-api-lat-853efaca-0912",
    },
    schemaVersion: NATIVE_PREFLIGHT_BENCHMARK_VERSION,
    sourceCommit: commit,
    warmRuns: makeRuns(true),
  };
}

test("100 cold and 100 warm samples produce reproducible hashes", () => {
  const raw = artifact();
  const first = evaluateNativePreflightBenchmark(raw);
  const second = evaluateNativePreflightBenchmark(JSON.parse(JSON.stringify(raw)));
  assert.equal(first.passed, true);
  assert.deepEqual(first.manifest, second.manifest);
  assert.match(first.manifest.rawSha256, /^[0-9a-f]{64}$/u);
  assert.match(first.manifest.summarySha256, /^[0-9a-f]{64}$/u);
  assert.equal(first.summary.cold.p95Ms, 2_000);
  assert.equal(first.summary.warm.p95Ms, 600);
  assert.equal(first.summary.warm.speechEndConnectionWaitMaximumMs, 0);
  assert.equal(first.summary.totalCostMicroUsd, 20_000);
});

test("one-second, fallback, post-speech wait, and cost gates fail together", () => {
  const raw = artifact();
  for (let index = 0; index < 6; index += 1) {
    raw.coldRuns[index].fallback = true;
    raw.coldRuns[index].latencyMs = null;
  }
  for (let index = 94; index < 100; index += 1) {
    raw.warmRuns[index].latencyMs = 1_001;
  }
  raw.warmRuns[99].speechEndConnectionWaitMs = 1;
  raw.warmRuns[99].estimatedCostMicroUsd = 100_001;
  const result = evaluateNativePreflightBenchmark(raw);
  assert.equal(result.passed, false);
  assert.deepEqual(result.failureCodes, [
    "warm_p95_exceeded",
    "cold_fallback_rate_exceeded",
    "speech_end_connection_wait_exceeded",
    "estimated_cost_exceeded",
  ]);
});

test("environment, exact shape, count, and index are mandatory", () => {
  const short = artifact();
  short.coldRuns.pop();
  assert.throws(() => evaluateNativePreflightBenchmark(short), /runs_invalid/u);
  const wrongIndex = artifact();
  wrongIndex.warmRuns[4].index = 9;
  assert.throws(() => evaluateNativePreflightBenchmark(wrongIndex), /run_invalid/u);
  const content = artifact();
  content.warmRuns[0].transcript = "secret";
  assert.throws(() => evaluateNativePreflightBenchmark(content), /run_invalid/u);
  const browser = artifact();
  browser.environment.browser = "Chromium";
  assert.throws(() => evaluateNativePreflightBenchmark(browser), /environment_invalid/u);
});

test("policy constants are fixed and immutable", () => {
  assert.deepEqual(NATIVE_PREFLIGHT_BENCHMARK_POLICY, {
    coldP95LimitMs: 4_000,
    maximumEstimatedCostMicroUsd: 100_000,
    maximumFallbackRateBps: 500,
    requiredColdRuns: 100,
    requiredWarmRuns: 100,
    speechEndConnectionWaitLimitMs: 0,
    warmP95LimitMs: 1_000,
  });
  assert.equal(Object.isFrozen(NATIVE_PREFLIGHT_BENCHMARK_POLICY), true);
});
