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

test("小声の帯域証拠はRustの二時間尺度floorからproduction VADへ入る", async () => {
  const [core, client, policy, bridge, bootstrap] = await Promise.all([
    readFile(new URL("../../../crates/audio_core/src/lib.rs", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
    readFile(new URL("../web/voice-session-policy.mjs", import.meta.url), "utf8"),
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../web/bootstrap.js", import.meta.url), "utf8"),
  ]);
  assert.match(core, /pub struct QuietEvidenceTracker/u);
  assert.match(core, /QUIET_EVIDENCE_BANDS_HZ/u);
  assert.match(core, /fast_band_floor/u);
  assert.match(core, /slow_band_floor/u);
  assert.match(core, /freeze_until_frame/u);
  assert.match(core, /QUIET_EVIDENCE_IN_SESSION_COVERAGE/u);
  assert.match(core, /coverage_stable_observations/u);
  assert.match(core, /QUIET_COVERAGE_MAXIMUM_CLIPPED_PER_MILLE/u);
  assert.match(core, /distribution_transport/u);
  assert.match(core, /QUIET_EVIDENCE_EXCITATION_INVARIANT/u);
  assert.match(client, /createQuietEvidenceTracker/u);
  assert.match(
    bootstrap,
    /installQuietEvidenceTrackerFactory\(createQuietEvidenceTracker\)/u,
  );
  assert.match(
    bridge,
    /quietEvidenceTracker\.advance\(\s*clockFrame,\s*pcm/u,
  );
  assert.match(bridge, /acousticEvidence: Object\.freeze/u);
  assert.match(bridge, /rustEvidence\[0\] > 31/u);
  assert.match(bridge, /rustEvidence\.length !== 3/u);
  assert.match(bridge, /rustEvidence\[2\] > 8/u);
  assert.match(
    bridge,
    /\(rustEvidence\[0\] & 3\) !== 0 && \(rustEvidence\[0\] & 8\) === 0/u,
  );
  assert.match(policy, /rustQuietCandidate/u);
  assert.match(policy, /QUIET_EVIDENCE_FLAGS/u);
  assert.doesNotMatch(bridge, /kotae:quiet.*evidence/iu);
});

test("ゲスト小声は通常入口と同じ有限proofを使い本文をイベントへ載せない", async () => {
  const [bridge, rust, fixture, deploy] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
    readFile(new URL("browser/pcm-ring-audio-worklet.fixture.mjs", import.meta.url), "utf8"),
    readFile(new URL("../../../scripts/deploy-hosting.ps1", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(bridge, /kotae:quiet-voice-confirmed/u);
  assert.doesNotMatch(rust, /その小さな声で届いています/u);
  assert.match(bridge, /recording\.softVoiceConfirmed/u);
  assert.match(bridge, /quietConfirmed/u);
  assert.match(rust, /guestQuietOnsetSelfTest/u);
  assert.match(fixture, /guestQuietOnsetValidated/u);
  assert.match(deploy, /guestQuietOnsetValidated/u);
});

