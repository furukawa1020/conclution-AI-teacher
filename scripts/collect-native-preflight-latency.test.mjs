import assert from "node:assert/strict";
import test from "node:test";

import {
  buildRawBenchmark,
  parseCollectorArguments,
  resolveEvidencePaths,
  selectProductionTarget,
} from "./collect-native-preflight-latency.mjs";
import { evaluateNativePreflightBenchmark } from "./native-preflight-latency-gate.mjs";

const commit = "a".repeat(40);
const argv = [
  "--debugging-port", "9222",
  "--output", "native-preflight-raw.json",
  "--device-model", "Example PC",
  "--operating-system", "Windows 11 24H2",
  "--network-condition", "wifi",
  "--runtime-revision", "kotae-api-example-0912",
  "--source-commit", commit,
  "--cost-micro-usd-per-unused-ms", "0.5",
];

test("CLI引数を厳密な計測条件へ変換する", () => {
  const options = parseCollectorArguments(argv);
  assert.equal(options.debuggingPort, 9222);
  assert.equal(options.timeoutMs, 3_600_000);
  assert.equal(options.costMicroUsdPerUnusedMs, 0.5);
  assert.throws(() => parseCollectorArguments([...argv, "--unknown", "x"]), /arguments_invalid/u);
  assert.throws(() => parseCollectorArguments(argv.slice(0, -2)), /arguments_invalid/u);
});

test("証拠出力をGit無視ディレクトリ内だけに拘束する", () => {
  const paths = resolveEvidencePaths("C:\\repo", "trial/raw.json");
  assert.equal(paths.rawPath, "C:\\repo\\.release-evidence\\trial\\raw.json");
  assert.equal(paths.resultPath, "C:\\repo\\.release-evidence\\trial\\raw.result.json");
  assert.throws(() => resolveEvidencePaths("C:\\repo", "../raw.json"), /output_invalid/u);
  assert.throws(() => resolveEvidencePaths("C:\\repo", "raw.txt"), /output_invalid/u);
});

test("公開版タブが厳密に1枚のときだけ選ぶ", () => {
  const target = { type: "page", url: "https://kotae-ai.web.app/", webSocketDebuggerUrl: "ws://127.0.0.1:9222/devtools/page/ABC-123" };
  assert.equal(selectProductionTarget([target], 9222), target);
  assert.throws(() => selectProductionTarget([target, { ...target }], 9222), /target_invalid/u);
  assert.throws(() => selectProductionTarget([{ ...target, url: "https://example.com" }], 9222), /target_invalid/u);
  assert.throws(() => selectProductionTarget([target], 9333), /target_invalid/u);
});

test("100試行を既存gateが再計算可能なrawへ変換する", () => {
  const options = parseCollectorArguments(argv);
  const runs = Array.from({ length: 100 }, (_, offset) => ({
    coldMs: 500 + offset,
    estimatedCostMicroUsd: 50,
    fallback: false,
    index: offset + 1,
    prepareLatencyMs: 10,
    speechEndConnectionWaitMs: 0,
    unusedConnectionMs: 100,
    warmMs: 400 + offset,
  }));
  const raw = buildRawBenchmark({ browserVersion: "140.0.7339.81", measuredAt: "2026-09-12T00:00:00.000Z", options, runs });
  const result = evaluateNativePreflightBenchmark(raw);
  assert.equal(result.passed, true);
  assert.equal(raw.coldRuns.length, 100);
  assert.equal(raw.warmRuns.length, 100);
  assert.equal(raw.environment.browser, "Google Chrome");
});
