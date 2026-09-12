import assert from "node:assert/strict";
import test from "node:test";
import { createNativePreflightBrowserCollector } from "./native-preflight-browser-collector-policy.mjs";

function latency(generation = 1) {
  return {
    coldMs: 800,
    generation,
    speechEndConnectionWaitMs: 0,
    version: "kotae.native-preflight-latency.v1",
    warmMs: 200,
  };
}

function prepare(result = "ready", generation = 1) {
  return {
    generation,
    latency_ms: 810,
    outcome: "on-target",
    result,
    route: result === "ready" ? "native-ready" : "http-fallback",
    version: 1,
  };
}

test("one production latency event pairs with one ready terminal", () => {
  const collector = createNativePreflightBrowserCollector({
    costMicroUsdPerUnusedMs: 0.25,
    requiredRuns: 2,
  });
  collector.acceptLatency(latency());
  assert.equal(collector.acceptPrepare(prepare()), false);
  assert.equal(collector.acceptPrepare(prepare("fallback", 2)), true);
  assert.deepEqual(collector.snapshot(), {
    complete: true,
    pendingLatency: false,
    runs: [
      {
        coldMs: 800,
        estimatedCostMicroUsd: 150,
        fallback: false,
        index: 1,
        prepareLatencyMs: 810,
        speechEndConnectionWaitMs: 0,
        unusedConnectionMs: 600,
        warmMs: 200,
      },
      {
        coldMs: null,
        estimatedCostMicroUsd: 0,
        fallback: true,
        index: 2,
        prepareLatencyMs: 810,
        speechEndConnectionWaitMs: 0,
        unusedConnectionMs: 0,
        warmMs: null,
      },
    ],
  });
});

test("fallback cannot hide a pending successful Native event", () => {
  const collector = createNativePreflightBrowserCollector({
    costMicroUsdPerUnusedMs: 0,
    requiredRuns: 1,
  });
  collector.acceptLatency(latency());
  assert.throws(
    () => collector.acceptPrepare(prepare("fallback")),
    /pairing_invalid/u,
  );
});

test("missing ready latency, duplicate latency, and content fields fail", () => {
  const collector = createNativePreflightBrowserCollector({
    costMicroUsdPerUnusedMs: 0,
    requiredRuns: 1,
  });
  assert.throws(() => collector.acceptPrepare(prepare()), /prepare_invalid/u);
  collector.acceptLatency(latency());
  assert.throws(() => collector.acceptLatency(latency()), /latency_invalid/u);
  const withTranscript = prepare();
  withTranscript.transcript = "secret";
  assert.throws(() => collector.acceptPrepare(withTranscript), /prepare_invalid/u);
});

test("collector configuration is finite and capped at 100", () => {
  for (const options of [
    { costMicroUsdPerUnusedMs: -1, requiredRuns: 100 },
    { costMicroUsdPerUnusedMs: 0, requiredRuns: 0 },
    { costMicroUsdPerUnusedMs: 0, requiredRuns: 101 },
  ]) {
    assert.throws(
      () => createNativePreflightBrowserCollector(options),
      /configuration_invalid/u,
    );
  }
});
