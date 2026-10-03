import assert from "node:assert/strict";
import test from "node:test";

import {
  createVoiceStartupHarness,
  flushStartupTasks,
} from "./helpers/voice-startup-harness.mjs";

async function finishMutedGraph(startup) {
  startup.microphone.resolve();
  startup.audioGraph.resolve();
  await flushStartupTasks();
}

test("guest worklet loading overlaps authentication and shares the live loader", async () => {
  const startup = await createVoiceStartupHarness({ guest: true, workletPending: true });
  const pending = startup.start();
  await finishMutedGraph(startup);
  assert.equal(startup.events.filter((event) => event === "worklet-load").length, 1);
  assert.equal(startup.events.filter((event) => event === "ring-load").length, 1);
  assert.equal(startup.events.includes("credentials-ready"), false);
  assert.equal(startup.events.includes("live-start"), false);
  assert.equal(startup.events.includes("recording"), false);
  assert.equal(startup.stream.enabled, false);

  startup.credentials.resolve();
  startup.providerReady.resolve();
  await flushStartupTasks();
  assert.equal(startup.events.includes("live-start"), true);
  assert.equal(startup.events.includes("recording"), false);
  assert.equal(startup.stream.enabled, false);
  startup.ringModule.resolve({});
  startup.worklet.resolve();
  assert.equal((await pending).state, "listening");
  assert.equal(startup.events.filter((event) => event === "worklet-load").length, 1);
  assert.equal(startup.events.filter((event) => event === "ring-load").length, 1);
  assert.equal(startup.isPlaybackReady(), true);
});

test("loaded guest modules cannot open the provider or microphone before authentication", async () => {
  const startup = await createVoiceStartupHarness({ guest: true });
  const pending = startup.start();
  await finishMutedGraph(startup);
  assert.equal(startup.isPlaybackReady(), true);
  assert.equal(startup.events.includes("live-start"), false);
  assert.equal(startup.stream.enabled, false);
  startup.credentials.reject(new Error("guest_session_expired"));
  await assert.rejects(pending, /guest_session_expired/u);
  assert.equal(startup.events.includes("recording"), false);
  assert.equal(startup.events.includes("microphone-release"), true);
  assert.equal(startup.stream.enabled, false);
});

test("failed speculative modules preserve the established live retry and HTTP fallback", async () => {
  const startup = await createVoiceStartupHarness({ guest: true, workletPending: true });
  const pending = startup.start();
  await finishMutedGraph(startup);
  assert.equal(startup.events.includes("worklet-load"), true);
  startup.ringModule.resolve({});
  startup.worklet.reject(new Error("fixture_load_failed"));
  await flushStartupTasks();
  assert.equal(startup.events.includes("live-start"), false);
  assert.equal(startup.isPlaybackReady(), false);
  startup.credentials.resolve();
  assert.equal((await pending).state, "listening");
  assert.equal(startup.events.includes("http-fallback"), true);
  assert.equal(startup.events.filter((event) => event === "worklet-load").length, 2);
});

test("cancelled guest preparation cannot start a load or adopt a completed old load", async (t) => {
  for (const cancelBeforeGraph of [true, false]) {
    await t.test(cancelBeforeGraph ? "before graph" : "after speculative load", async () => {
      const startup = await createVoiceStartupHarness({ guest: true, workletPending: true });
      const pending = startup.start();
      if (cancelBeforeGraph) startup.cancel();
      await finishMutedGraph(startup);
      assert.equal(startup.events.includes("worklet-load"), !cancelBeforeGraph);
      if (!cancelBeforeGraph) startup.cancel();
      startup.ringModule.resolve({});
      startup.worklet.resolve();
      startup.credentials.resolve();
      await assert.rejects(pending, /request_cancelled/u);
      assert.equal(startup.events.includes("live-start"), false);
      assert.equal(startup.events.includes("recording"), false);
      assert.equal(startup.stream.enabled, false);
      assert.equal(startup.context.state, "closed");
    });
  }
});

test("strict, document and unsupported guest routes never preload native modules", async (t) => {
  for (const options of [
    { strictCloudMinimization: true },
    { documentPending: true },
    { workletSupported: false },
    { aecVerified: false },
  ]) {
    await t.test(Object.keys(options)[0], async () => {
      const startup = await createVoiceStartupHarness({ guest: true, ...options });
      const pending = startup.start();
      await finishMutedGraph(startup);
      assert.equal(startup.events.includes("worklet-load"), false);
      assert.equal(startup.events.includes("ring-load"), false);
      assert.equal(startup.events.includes("live-start"), false);
      assert.equal(startup.stream.enabled, false);
      startup.credentials.resolve();
      startup.providerReady.resolve();
      assert.equal((await pending).state, "listening");
    });
  }
});

test("a closed graph is never warmed even before its owner epoch changes", async () => {
  const startup = await createVoiceStartupHarness({ guest: true });
  const pending = startup.start();
  startup.context.state = "closed";
  await finishMutedGraph(startup);
  assert.equal(startup.events.includes("worklet-load"), false);
  assert.equal(startup.events.includes("ring-load"), false);
  startup.credentials.reject(new Error("request_cancelled"));
  await assert.rejects(pending, /request_cancelled/u);
  assert.equal(startup.events.includes("live-start"), false);
  assert.equal(startup.events.includes("recording"), false);
});
