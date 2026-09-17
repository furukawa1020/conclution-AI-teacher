import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import test from "node:test";

import {
  safeVoiceReceiptVisible,
  shouldShowVoiceReceipt,
} from "../web/voice-session-policy.mjs";

test("invalid or reversed receipt clock only clears advisory UI", () => {
  const normal = { hasSpeech: true, lastVoiceAt: 1_000, now: 1_700 };
  assert.equal(safeVoiceReceiptVisible(normal), true);
  assert.equal(safeVoiceReceiptVisible({ ...normal, now: 999 }), false);
  assert.equal(safeVoiceReceiptVisible({ ...normal, now: Number.NaN }), false);
  assert.equal(safeVoiceReceiptVisible({ ...normal, lastVoiceAt: undefined }), false);
  assert.equal(safeVoiceReceiptVisible({ ...normal, hasSpeech: undefined }), false);
  assert.throws(
    () => shouldShowVoiceReceipt({ ...normal, now: 999 }),
    /voice_receipt_state_invalid/u,
  );
});

test("production recording interval uses the fail-closed receipt wrapper", async () => {
  const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const start = bridge.indexOf("function updateVoiceReceipt(recording, now)");
  const end = bridge.indexOf("function nextPcmCaptureGeneration()", start);
  assert.ok(start >= 0 && end > start);
  const update = runInNewContext(`${bridge.slice(start, end)}\nupdateVoiceReceipt`, {
    safeVoiceReceiptVisible,
    setVoiceReceiptVisible(value) { visible = value; },
  });
  let visible = null;
  const recording = { vadHasSpeech: true, lastVoiceAt: 1_000 };
  assert.doesNotThrow(() => update(recording, 999));
  assert.equal(visible, false);
  update(recording, 1_700);
  assert.equal(visible, true);
});
