import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("原観測混合はRust所有で配布Wasmの必須証拠になる", async () => {
  const [rust, runtime, fixture, deploy, docs] = await Promise.all([
    readFile(new URL("../../../crates/pcm_ring/src/lib.rs", import.meta.url), "utf8"),
    readFile(new URL("../web/pcm-ring-worklet-runtime.js", import.meta.url), "utf8"),
    readFile(new URL("browser/pcm-ring-audio-worklet.fixture.mjs", import.meta.url), "utf8"),
    readFile(new URL("../../../scripts/deploy-hosting.ps1", import.meta.url), "utf8"),
    readFile(new URL("../../../docs/observation-adding.md", import.meta.url), "utf8"),
  ]);
  assert.match(rust, /OBSERVATION_MINIMUM_MIX: f64 = 0\.30/u);
  assert.match(rust, /OBSERVATION_MAXIMUM_MIX: f64 = 0\.40/u);
  assert.match(rust, /cross_energy/u);
  assert.match(rust, /residual_ratio/u);
  assert.match(rust, /observation_adding_self_test/u);
  assert.match(runtime, /observationAddingSelfTest\(\) !== true/u);
  assert.match(fixture, /observationAddingValidated/u);
  assert.match(fixture, /const framesPerCohort = 16/u);
  assert.match(fixture, /cohort < 64/u);
  assert.doesNotMatch(fixture, /observation_adding_wasm_p95_exceeded/u);
  assert.match(deploy, /\$result\.observationAddingValidated/u);
  assert.match(docs, /raw混合率を30%から40%/u);
});

test("原音と強調音の特徴や本文をJavaScriptイベントへ公開しない", async () => {
  const [runtime, worklet, bridge] = await Promise.all([
    readFile(new URL("../web/pcm-ring-worklet-runtime.js", import.meta.url), "utf8"),
    readFile(new URL("../web/pcm-capture-worklet.js", import.meta.url), "utf8"),
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
  ]);
  for (const source of [runtime, worklet, bridge]) {
    assert.doesNotMatch(source, /observationMix|residualRatio|rawCorrelation/u);
    assert.doesNotMatch(source, /kotae:observation/u);
  }
});
