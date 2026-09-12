const LATENCY_KEYS = Object.freeze([
  "coldMs",
  "generation",
  "speechEndConnectionWaitMs",
  "version",
  "warmMs",
]);
const PREPARE_KEYS = Object.freeze([
  "generation",
  "latency_ms",
  "outcome",
  "result",
  "route",
  "version",
]);
const LATENCY_VERSION = "kotae.native-preflight-latency.v1";

function exactKeys(value, expected) {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).sort().join("\u0000") === expected.join("\u0000")
  );
}

function validLatency(value) {
  return Number.isFinite(value) && value >= 0 && value <= 15_000;
}

export function createNativePreflightBrowserCollector({
  costMicroUsdPerUnusedMs,
  requiredRuns = 100,
}) {
  if (
    !Number.isFinite(costMicroUsdPerUnusedMs) ||
    costMicroUsdPerUnusedMs < 0 ||
    !Number.isSafeInteger(requiredRuns) ||
    requiredRuns < 1 ||
    requiredRuns > 100
  ) {
    throw new TypeError("native_preflight_collector_configuration_invalid");
  }
  const completed = [];
  let pendingLatency;

  function acceptLatency(detail) {
    if (
      completed.length >= requiredRuns ||
      pendingLatency !== undefined ||
      !exactKeys(detail, LATENCY_KEYS) ||
      detail.version !== LATENCY_VERSION ||
      !Number.isSafeInteger(detail.generation) ||
      detail.generation < 1 ||
      !validLatency(detail.coldMs) ||
      !validLatency(detail.warmMs) ||
      detail.warmMs > detail.coldMs ||
      detail.speechEndConnectionWaitMs !== 0
    ) {
      throw new TypeError("native_preflight_collector_latency_invalid");
    }
    pendingLatency = Object.freeze({ ...detail });
  }

  function acceptPrepare(detail) {
    if (
      completed.length >= requiredRuns ||
      !exactKeys(detail, PREPARE_KEYS) ||
      !Number.isSafeInteger(detail.generation) ||
      detail.generation < 1 ||
      !Number.isSafeInteger(detail.latency_ms) ||
      detail.latency_ms < 0 ||
      detail.version !== 1
    ) {
      throw new TypeError("native_preflight_collector_prepare_invalid");
    }
    const ready = detail.result === "ready" && detail.route === "native-ready";
    const fallback =
      detail.result === "fallback" && detail.route === "http-fallback";
    if ((!ready && !fallback) || (ready && pendingLatency === undefined)) {
      throw new TypeError("native_preflight_collector_prepare_invalid");
    }
    if (fallback && pendingLatency !== undefined) {
      throw new TypeError("native_preflight_collector_pairing_invalid");
    }
    const latency = pendingLatency;
    pendingLatency = undefined;
    const unusedConnectionMs = ready
      ? Math.round((latency.coldMs - latency.warmMs) * 10) / 10
      : 0;
    const estimatedCostMicroUsd = Math.round(
      unusedConnectionMs * costMicroUsdPerUnusedMs,
    );
    completed.push(Object.freeze({
      coldMs: ready ? latency.coldMs : null,
      estimatedCostMicroUsd,
      fallback,
      index: completed.length + 1,
      prepareLatencyMs: detail.latency_ms,
      speechEndConnectionWaitMs: ready
        ? latency.speechEndConnectionWaitMs
        : 0,
      unusedConnectionMs,
      warmMs: ready ? latency.warmMs : null,
    }));
    return completed.length === requiredRuns;
  }

  function snapshot() {
    return Object.freeze({
      complete: completed.length === requiredRuns,
      pendingLatency: pendingLatency !== undefined,
      runs: Object.freeze([...completed]),
    });
  }

  return Object.freeze({ acceptLatency, acceptPrepare, snapshot });
}
