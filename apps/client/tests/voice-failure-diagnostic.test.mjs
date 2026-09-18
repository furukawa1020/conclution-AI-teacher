import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { safeVoiceFailureCode } from "../web/voice-session-policy.mjs";

test("voice diagnostic discloses only finite codes, never arbitrary errors", () => {
  assert.equal(safeVoiceFailureCode(new Error("voice_live_frame_invalid")), "voice_live_frame_invalid");
  assert.equal(safeVoiceFailureCode(new Error("voice_response_invalid")), "voice_response_invalid");
  assert.equal(safeVoiceFailureCode(new Error("Bearer private-token")), "unclassified");
  assert.equal(safeVoiceFailureCode({ name: "TypeError", message: "user transcript" }), "unclassified_type_error");
  assert.equal(safeVoiceFailureCode(null), "unclassified");
});

test("visible begin, wait, and finish failure paths emit finite diagnostics", async () => {
  const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  assert.match(bridge, /function reportVoiceFailure\(phase, error\)/u);
  assert.match(bridge, /safeVoiceFailureCode\(error\)/u);
  for (const phase of ["begin", "wait"]) {
    assert.match(bridge, new RegExp(`reportVoiceFailure\\(\\x60${phase}\\x60, error\\)`, "u"));
  }
  const reporter = bridge.slice(
    bridge.indexOf(`function reportVoiceFailure(phase, error)`),
    bridge.indexOf(`function boundedLatency(value)`),
  );
  assert.match(reporter, /console\?\.warn\?\.\(`KOTAE_VOICE_FAILURE`, phase, safeVoiceFailureCode\(error\)\)/u);
});

test('live and worklet failures retain finite diagnostic codes', async () => {
  assert.equal(safeVoiceFailureCode(new Error('capture_overflow')), 'capture_overflow');
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  assert.match(bridge, /reportVoiceFailure\(`live`, error\)/u);
  assert.match(bridge, /reportVoiceFailure\(`worklet`, new Error\(signal\)\)/u);
});

test('finish stages and stop reasons are finite diagnostics', async () => {
  assert.equal(safeVoiceFailureCode(new Error('no_speech')), 'no_speech');
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  for (const phase of ['turn_end', 'live_commit', 'live_playback', 'fallback_capture', 'fallback_encode', 'fallback_read', 'fallback_auth', 'fallback_base64', 'http_request', 'http_playback']) {
    assert.ok(bridge.includes(`finishPhase = \`${phase}\``));
  }
  assert.match(bridge, /reportVoiceFailure\(finishPhase, error\)/u);
  assert.match(bridge, /KOTAE_VOICE_STOP/u);
  assert.match(bridge, /KOTAE_VOICE_CAPTURE/u);
});
