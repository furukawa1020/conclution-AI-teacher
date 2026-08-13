import assert from "node:assert/strict";
import test from "node:test";

import {
  createGuestVoiceComparison,
  GUEST_VOICE_COMPARISON_LIMITS,
} from "../web/voice-stream-policy.mjs";

function frame(fill = 7) {
  const value = new ArrayBuffer(GUEST_VOICE_COMPARISON_LIMITS.frameBytes);
  new Uint8Array(value).fill(fill);
  return value;
}

function isZero(value) {
  return [...new Uint8Array(value)].every((byte) => byte === 0);
}

test("two opted-in beginnings stay inside one finite local owner", () => {
  const comparison = createGuestVoiceComparison();
  assert.equal(comparison.optIn(9), true);
  for (let clip = 0; clip < 2; clip += 1) {
    const serial = comparison.beginTurn(9);
    assert.ok(serial > 0);
    for (let index = 0; index < 75; index += 1) {
      const pcm = frame(clip + 1);
      const accepted = comparison.captureOwnedFrame(pcm, 9, serial);
      assert.equal(accepted, index < 50);
      if (!accepted) assert.equal(isZero(pcm), true);
    }
    assert.equal(comparison.finishTurn(9, serial), true);
  }
  assert.deepEqual(comparison.snapshot(), {
    activeFrames: 0,
    clipCount: 2,
    state: "armed",
    totalBytes: 64_000,
  });
  assert.equal(comparison.authorize("changed_to_answer_first", 9), true);
  const clips = comparison.take(9);
  assert.equal(clips.length, 2);
  assert.equal(clips.every((clip) => clip.length === 50), true);
  assert.equal(comparison.snapshot().totalBytes, 0);
  for (const clip of clips) for (const pcm of clip) new Uint8Array(pcm).fill(0);
});

test("no proof, stale generation, opt-out, and exceptions zeroize every owned byte", () => {
  for (const outcome of ["no_verified_change", "stayed_answer_first"]) {
    const comparison = createGuestVoiceComparison();
    comparison.optIn(11);
    const firstSerial = comparison.beginTurn(11);
    const first = frame();
    comparison.captureOwnedFrame(first, 11, firstSerial);
    comparison.finishTurn(11, firstSerial);
    const secondSerial = comparison.beginTurn(11);
    const second = frame();
    comparison.captureOwnedFrame(second, 11, secondSerial);
    comparison.finishTurn(11, secondSerial);
    assert.equal(comparison.authorize(outcome, 11), false);
    assert.equal(isZero(first), true);
    assert.equal(isZero(second), true);
    assert.equal(comparison.snapshot().state, "disabled");
  }

  const comparison = createGuestVoiceComparison();
  comparison.optIn(12);
  const serial = comparison.beginTurn(12);
  const retained = frame();
  comparison.captureOwnedFrame(retained, 12, serial);
  const stale = frame();
  assert.equal(comparison.captureOwnedFrame(stale, 13, serial), false);
  assert.equal(isZero(stale), true);
  comparison.clear();
  assert.equal(isZero(retained), true);
});

