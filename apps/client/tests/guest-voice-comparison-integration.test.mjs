import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const bridge = fs.readFileSync(
  new URL("../web/firebase-bridge.js", import.meta.url),
  "utf8",
);
const policy = fs.readFileSync(
  new URL("../web/voice-stream-policy.mjs", import.meta.url),
  "utf8",
);
const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");

test("guest comparison owns a local clone before normal voice transport", () => {
  const capture = bridge.indexOf("guestVoiceComparison.captureOwnedFrame(");
  const clone = bridge.indexOf("frame.slice(0)", capture);
  const transport = bridge.indexOf("clientTransport.pushFrame(frame)", capture);
  assert.ok(capture > 0);
  assert.ok(clone > capture);
  assert.ok(transport > clone);
  assert.match(policy, /framesPerClip:\s*50/);
  assert.match(policy, /totalBytes:\s*2 \* 50 \* VOICE_LIVE_LIMITS\.inputFrameBytes/);
});

test("comparison is explicit, proof-bound, content-free, and erasable", () => {
  assert.match(bridge, /guestVoiceComparison\.authorize\(\s*result\.guestAFirstOutcome/);
  assert.match(bridge, /result\.interrupted !== true/);
  assert.match(bridge, /detail: Object\.freeze\(\{ version: 1 \}\)/);
  assert.doesNotMatch(
    bridge.slice(
      bridge.indexOf("function finalizeGuestVoiceComparisonResult"),
      bridge.indexOf("function mapVoiceResponseError"),
    ),
    /caption|transcript|pcm|audio|voiceprint|feature/i,
  );
  assert.match(bridge, /globalThis\.addEventListener\("pagehide", \(\) => \{[\s\S]*?guestVoiceComparison\.clear\(\)/);
  assert.match(bridge, /function stopSession[\s\S]*?guestVoiceComparison\.clear\(\)/);
  assert.match(bridge, /new Uint8Array\(frame\)\.fill\(0\)/);
  assert.match(bridge, /buffer\.getChannelData\(0\)\.fill\(0\)/);
});

test("guest UI defaults to skip and offers one replay or immediate deletion", () => {
  assert.match(client, /guest_voice_comparison_opt_in = use_signal\(\|\| false\)/);
  assert.match(client, /任意：自分の冒頭を二回だけ聞き比べる/);
  assert.match(client, /端末内だけ・最大1秒ずつ・再生または離脱ですぐ消去/);
  assert.match(client, /"二回だけ聞く"/);
  assert.match(client, /"聞かずに消す"/);
  assert.match(client, /set_guest_voice_comparison_opt_in\(false\)/);
});
