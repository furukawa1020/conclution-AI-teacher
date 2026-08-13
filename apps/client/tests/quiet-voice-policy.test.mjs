import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("小声入口はRust/Wasm単一判定と240ms確認を公開する", async () => {
  const [rust, policy, bootstrap, ui] = await Promise.all([
    readFile(new URL("../../../crates/audio_core/src/lib.rs", import.meta.url), "utf8"),
    readFile(new URL("../web/voice-session-policy.mjs", import.meta.url), "utf8"),
    readFile(new URL("../web/bootstrap.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
  ]);
  assert.match(rust, /pub fn classify_onset_frame/u);
  assert.match(policy, /softVoiceMinimumMs: 240/u);
  assert.match(policy, /softVoiceMinimumEvidenceSpanMs: 120/u);
  assert.match(policy, /onset_frame_classifier_unavailable/u);
  assert.match(bootstrap, /installOnsetFrameClassifier\(classifyOnsetFrame\)/u);
  assert.match(ui, /叫ばなくて大丈夫。小声や、ぼそっとした/u);
});

