import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("interrupt VAD fails closed until the Rust/Wasm classifier is installed", async () => {
  const policy = await import(
    "../web/voice-stream-policy.mjs?classifier-unavailable"
  );
  const state = policy.createInterruptVadState(1_000);
  assert.throws(
    () =>
      policy.advanceInterruptVad(state, {
        now: 1_400,
        outputActive: true,
        peak: 0.2,
        rms: 0.1,
      }),
    /interrupt_vad_classifier_unavailable/u,
  );
});

test("interrupt VAD rejects an invalid Wasm bit contract", async () => {
  const policy = await import(
    "../web/voice-stream-policy.mjs?classifier-invalid-result"
  );
  policy.installInterruptFrameClassifier(() => 8);
  assert.throws(
    () =>
      policy.advanceInterruptVad(policy.createInterruptVadState(1_000), {
        now: 1_400,
        outputActive: false,
        peak: 0.2,
        rms: 0.1,
      }),
    /interrupt_vad_classifier_result_invalid/u,
  );
});

test("bootstrap installs the classifier before launching the Wasm UI", async () => {
  const bootstrap = await readFile(
    new URL("../web/bootstrap.js", import.meta.url),
    "utf8",
  );
  const classifierImport = bootstrap.indexOf("classifyInterruptFrame");
  const classifierInstall = bootstrap.indexOf(
    "installInterruptFrameClassifier(classifyInterruptFrame)",
  );
  const launch = bootstrap.indexOf("await init()");
  assert.ok(classifierImport >= 0);
  assert.ok(classifierInstall > classifierImport);
  assert.ok(launch > classifierInstall);
});
