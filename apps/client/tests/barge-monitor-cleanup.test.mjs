import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
const bridge = readFileSync(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const start = bridge.indexOf("function stopBargeInMonitoring(");
const end = bridge.indexOf("function discardInterruptedPlaybackRecording(", start);
assert.ok(start >= 0 && end > start);
const run = new Function("playback", "restorePlaybackGain", "abandonInterruptRecording", "setTracksEnabled",
  `${bridge.slice(start, end)}; stopBargeInMonitoring(playback);`);

test("monitor cleanup continues through independent device errors", () => {
  const calls = [];
  const fail = name => () => { calls.push(name); throw new Error("device"); };
  const playback = { bargePcmMonitor: { stop: fail("monitor") }, interruptRecording: {}, resetInterruptGuard() {} };
  run(playback, fail("gain"), recording => {
    assert.equal(playback.interruptRecording, undefined);
    assert.ok(recording);
    fail("recording")();
  }, enabled => calls.push(enabled));
  assert.deepEqual(calls, ["monitor", "gain", "recording", false]);
  assert.equal(playback.bargePcmMonitor, undefined);
  assert.equal(playback.resetInterruptGuard, undefined);
});

test("accepted interruption preserves recording and microphone for the next turn", () => {
  const recording = {};
  const playback = { interrupted: true, interruptRecording: recording, bargePcmMonitor: { stop() { throw new Error("device"); } } };
  const unexpected = () => assert.fail("accepted interruption must keep capture");
  run(playback, unexpected, unexpected, unexpected);
  assert.equal(playback.interruptRecording, recording);
  assert.equal(playback.bargePcmMonitor, undefined);
});
