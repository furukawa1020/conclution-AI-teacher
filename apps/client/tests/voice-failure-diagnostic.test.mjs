import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { safeVoiceFailureCode, zeroizeCaptureFrame } from "../web/voice-session-policy.mjs";

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
  for (const phase of ['turn_end', 'live_commit', 'live_playback', 'fallback_capture', 'fallback_encode', 'fallback_read', 'fallback_auth', 'fallback_base64', 'http_prepare', 'http_request', 'http_playback']) {
    assert.ok(bridge.includes(`finishPhase = \`${phase}\``));
  }
  assert.match(bridge, /reportVoiceFailure\(finishPhase, error\)/u);
  assert.match(bridge, /KOTAE_VOICE_STOP/u);
  assert.match(bridge, /KOTAE_VOICE_CAPTURE/u);
});

test('live and HTTP playback both import the finite latency transport enum', async () => {
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  assert.match(bridge, /import \{[\s\S]*?VOICE_LATENCY_TRANSPORTS,[\s\S]*?\} from "\.\/voice-latency-trace-policy\.mjs"/u);
  assert.match(bridge, /Object\.values\(VOICE_LATENCY_TRANSPORTS\)\.includes\(traceTransport\)/u);
  assert.doesNotMatch(bridge, /(?:const|let|var) VOICE_LATENCY_TRANSPORTS\s*=/u);
});

test('Native HTTP fallback can zeroize all PCM buffers outside the live-session closure', async () => {
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  assert.match(bridge, /import \{[\s\S]*?zeroizeCaptureFrame,[\s\S]*?\} from "\.\/voice-session-policy\.mjs"/u);
  assert.doesNotMatch(bridge, /function zeroizeCaptureFrame\(/u);
  assert.match(bridge, /zeroizeCaptureFrame\(audioBuffer\)/u);
  assert.match(bridge, /zeroizeCaptureFrame\(quietHttpAudioBuffer\.baseline\)/u);
  assert.match(bridge, /zeroizeCaptureFrame\(quietHttpAudioBuffer\.weak\)/u);
  const frame = new Uint8Array(640).fill(173);
  zeroizeCaptureFrame(frame.buffer);
  assert.ok(frame.every((byte) => byte === 0));
  assert.doesNotThrow(() => zeroizeCaptureFrame(undefined));
});

test('a paused AudioContext frame grants no VAD credit and preserves the recording', async () => {
  const bridge = await readFile(new URL('../web/firebase-bridge.js', import.meta.url), 'utf8');
  const vad = bridge.slice(bridge.indexOf('function armVad(recording)'), bridge.indexOf('function createRecordingState('));
  const skipAt = vad.indexOf('if (clockFrame === vadState.temporalClock.lastFrame) return;');
  const trackerAt = vad.indexOf('recording.quietEvidenceTracker.advance(');
  assert.ok(skipAt > 0 && trackerAt > skipAt);
  assert.match(vad, /evidenceStage = "evidence_tracker"/u);
  assert.match(vad, /evidenceStage = "evidence_contract"/u);
  assert.match(vad, /evidenceStage = "vad_transition"/u);
  assert.match(vad, /rejectRecording\(recording, "voice_turn_invalid", evidenceStage\)/u);
});
