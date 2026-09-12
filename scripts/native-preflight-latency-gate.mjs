import { createHash } from "node:crypto";

export const NATIVE_PREFLIGHT_BENCHMARK_VERSION =
  "kotae.native-preflight-benchmark.v1";
export const NATIVE_PREFLIGHT_BENCHMARK_POLICY = Object.freeze({
  coldP95LimitMs: 4_000,
  maximumEstimatedCostMicroUsd: 100_000,
  maximumFallbackRateBps: 500,
  requiredColdRuns: 100,
  requiredWarmRuns: 100,
  speechEndConnectionWaitLimitMs: 0,
  warmP95LimitMs: 1_000,
});

const REVISION_PATTERN = /^[0-9a-f]{40}$/u;
const RUNTIME_REVISION_PATTERN = /^kotae-api-[a-z0-9-]{1,52}$/u;
const RUN_KEYS = Object.freeze([
  "estimatedCostMicroUsd",
  "fallback",
  "index",
  "latencyMs",
  "unusedConnectionMs",
]);
const WARM_RUN_KEYS = Object.freeze([
  ...RUN_KEYS,
  "speechEndConnectionWaitMs",
].sort());
const ENVIRONMENT_KEYS = Object.freeze([
  "browser",
  "browserVersion",
  "cloudRegion",
  "deviceModel",
  "measuredAt",
  "networkCondition",
  "operatingSystem",
  "providerRegion",
  "runtimeRevision",
]);
const RAW_KEYS = Object.freeze([
  "coldRuns",
  "environment",
  "schemaVersion",
  "sourceCommit",
  "warmRuns",
]);
const NETWORK_CONDITIONS = new Set([
  "ethernet",
  "unshaped",
  "wifi",
  "4g-emulated",
]);

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function exactKeys(value, expected) {
  return (
    isRecord(value) &&
    Object.keys(value).sort().join("\u0000") === expected.join("\u0000")
  );
}

function boundedText(value, maximum = 128) {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= maximum &&
    value.trim() === value &&
    !/[\r\n\u0000]/u.test(value)
  );
}

function finiteNonNegative(value) {
  return Number.isFinite(value) && value >= 0;
}

function validateEnvironment(value) {
  if (
    !exactKeys(value, ENVIRONMENT_KEYS) ||
    value.browser !== "Google Chrome" ||
    !/^\d+\.\d+\.\d+\.\d+$/u.test(value.browserVersion) ||
    value.cloudRegion !== "asia-northeast1" ||
    value.providerRegion !== "us-central1" ||
    !boundedText(value.deviceModel) ||
    !boundedText(value.operatingSystem) ||
    !NETWORK_CONDITIONS.has(value.networkCondition) ||
    !RUNTIME_REVISION_PATTERN.test(value.runtimeRevision) ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/u.test(
      value.measuredAt,
    )
  ) {
    throw new TypeError("native_preflight_benchmark_environment_invalid");
  }
}

function validateRuns(values, required, warm) {
  if (!Array.isArray(values) || values.length !== required) {
    throw new TypeError("native_preflight_benchmark_runs_invalid");
  }
  const expectedKeys = warm ? WARM_RUN_KEYS : RUN_KEYS;
  for (let position = 0; position < values.length; position += 1) {
    const run = values[position];
    if (
      !exactKeys(run, expectedKeys) ||
      run.index !== position + 1 ||
      typeof run.fallback !== "boolean" ||
      (!run.fallback && !finiteNonNegative(run.latencyMs)) ||
      (run.fallback && run.latencyMs !== null) ||
      !finiteNonNegative(run.unusedConnectionMs) ||
      !Number.isSafeInteger(run.estimatedCostMicroUsd) ||
      run.estimatedCostMicroUsd < 0 ||
      (warm && !finiteNonNegative(run.speechEndConnectionWaitMs))
    ) {
      throw new TypeError("native_preflight_benchmark_run_invalid");
    }
  }
}

export function canonicalJson(value) {
  if (Array.isArray(value)) {
    return `[${value.map(canonicalJson).join(",")}]`;
  }
  if (isRecord(value)) {
    return `{${Object.keys(value)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonicalJson(value[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}

function digest(value) {
  return createHash("sha256").update(canonicalJson(value), "utf8").digest("hex");
}

function percentile(values, fraction) {
  if (values.length === 0) return null;
  const sorted = [...values].sort((left, right) => left - right);
  return sorted[Math.ceil(sorted.length * fraction) - 1];
}

function summarizeRuns(runs, warm) {
  const successful = runs.filter((run) => !run.fallback);
  const latencies = successful.map((run) => run.latencyMs);
  return Object.freeze({
    attempts: runs.length,
    estimatedCostMicroUsd: runs.reduce(
      (total, run) => total + run.estimatedCostMicroUsd,
      0,
    ),
    fallbackRateBps: Math.round(
      (runs.filter((run) => run.fallback).length * 10_000) / runs.length,
    ),
    maximumMs: latencies.length === 0 ? null : Math.max(...latencies),
    p50Ms: percentile(latencies, 0.5),
    p95Ms: percentile(latencies, 0.95),
    speechEndConnectionWaitMaximumMs: warm
      ? Math.max(...runs.map((run) => run.speechEndConnectionWaitMs))
      : null,
    successes: successful.length,
    unusedConnectionMs: runs.reduce(
      (total, run) => total + run.unusedConnectionMs,
      0,
    ),
  });
}

export function evaluateNativePreflightBenchmark(raw) {
  if (
    !exactKeys(raw, RAW_KEYS) ||
    raw.schemaVersion !== NATIVE_PREFLIGHT_BENCHMARK_VERSION ||
    !REVISION_PATTERN.test(raw.sourceCommit)
  ) {
    throw new TypeError("native_preflight_benchmark_invalid");
  }
  validateEnvironment(raw.environment);
  validateRuns(raw.coldRuns, NATIVE_PREFLIGHT_BENCHMARK_POLICY.requiredColdRuns, false);
  validateRuns(raw.warmRuns, NATIVE_PREFLIGHT_BENCHMARK_POLICY.requiredWarmRuns, true);
  const cold = summarizeRuns(raw.coldRuns, false);
  const warm = summarizeRuns(raw.warmRuns, true);
  const totalCostMicroUsd =
    cold.estimatedCostMicroUsd + warm.estimatedCostMicroUsd;
  const failureCodes = [];
  if (cold.p95Ms === null || cold.p95Ms > NATIVE_PREFLIGHT_BENCHMARK_POLICY.coldP95LimitMs) {
    failureCodes.push("cold_p95_exceeded");
  }
  if (warm.p95Ms === null || warm.p95Ms > NATIVE_PREFLIGHT_BENCHMARK_POLICY.warmP95LimitMs) {
    failureCodes.push("warm_p95_exceeded");
  }
  if (cold.fallbackRateBps > NATIVE_PREFLIGHT_BENCHMARK_POLICY.maximumFallbackRateBps) {
    failureCodes.push("cold_fallback_rate_exceeded");
  }
  if (warm.fallbackRateBps > NATIVE_PREFLIGHT_BENCHMARK_POLICY.maximumFallbackRateBps) {
    failureCodes.push("warm_fallback_rate_exceeded");
  }
  if (warm.speechEndConnectionWaitMaximumMs > NATIVE_PREFLIGHT_BENCHMARK_POLICY.speechEndConnectionWaitLimitMs) {
    failureCodes.push("speech_end_connection_wait_exceeded");
  }
  if (totalCostMicroUsd > NATIVE_PREFLIGHT_BENCHMARK_POLICY.maximumEstimatedCostMicroUsd) {
    failureCodes.push("estimated_cost_exceeded");
  }
  const summary = Object.freeze({ cold, totalCostMicroUsd, warm });
  return Object.freeze({
    failureCodes: Object.freeze(failureCodes),
    manifest: Object.freeze({
      rawSha256: digest(raw),
      schemaVersion: "kotae.native-preflight-benchmark-manifest.v1",
      sourceCommit: raw.sourceCommit,
      summarySha256: digest(summary),
    }),
    passed: failureCodes.length === 0,
    policy: NATIVE_PREFLIGHT_BENCHMARK_POLICY,
    summary,
  });
}
