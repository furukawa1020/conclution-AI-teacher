import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const bridge = fs.readFileSync(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");

test("HTTP stream and WebSocket share the server answer boundary before local receipt authorization", () => {
  assert.match(bridge, /answerProof: result\?\.answerProof/);
  assert.match(bridge, /coachPhase: result\?\.coachPhase/);
  assert.match(bridge, /finalizeSilenceReceiptResult\(compared, expectedEpoch, false\)/);
  assert.match(bridge, /finalizeSilenceReceiptResult\(compared, expectedEpoch, true\)/);
  assert.match(bridge, /silenceReceiptGate\.observe\([\s\S]*?event\.data\.contextFrame/);
});

test("the public receipt event is an exact content-free finite enum", () => {
  const start = bridge.indexOf("function dispatchSilenceReceipt");
  const end = bridge.indexOf("function finalizeSilenceReceiptResult", start);
  const source = bridge.slice(start, end);
  assert.match(source, /outcome !== "none" && outcome !== "ai_waited_for_answer"/);
  assert.match(source, /detail: Object\.freeze\(\{ outcome, version: 1 \}\)/);
  assert.doesNotMatch(source, /caption|transcript|answerText|latency|milliseconds|pcm/i);
});

test("Rust accepts only the exact receipt and never presents a score", () => {
  assert.match(client, /keys\.length\(\) == 2/);
  assert.match(client, /Some\("ai_waited_for_answer"\) => visible\.set\(true\)/);
  assert.match(client, /Some\("none"\) => visible\.set\(false\)/);
  assert.match(client, /AIより先に、あなたの答えが出ました/);
  assert.match(client, /速さや上達の点数ではありません/);
});
