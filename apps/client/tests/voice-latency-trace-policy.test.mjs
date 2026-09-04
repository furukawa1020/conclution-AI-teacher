import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  buildVoiceLatencyTrace,
  classifyVoiceLatencyDevice,
  classifyVoiceLatencyNetwork,
  loadVoiceLatencyRevision,
  VOICE_LATENCY_DEVICE_CLASSES,
  VOICE_LATENCY_NETWORK_CLASSES,
  VOICE_LATENCY_TRACE_VERSION,
  VOICE_LATENCY_TRANSPORTS,
} from "../web/voice-latency-trace-policy.mjs";

const revision = "0123456789abcdef0123456789abcdef01234567";

function validInput(overrides = {}) {
  return {
    commitAcknowledgedAt: 10_300,
    commitSentAt: 10_200,
    deviceClass: "desktop",
    firstBinaryAt: 10_500,
    gestureToListeningMs: 240,
    networkClass: "typical",
    revision,
    route: "native-conversation",
    speakerWriteAt: 10_650,
    speechEndedAt: 10_000,
    transport: "native-live",
    ...overrides,
  };
}

test("client trace exactly matches the Go release-gate schema", () => {
  const trace = buildVoiceLatencyTrace(validInput());
  assert.equal(trace.schemaVersion, VOICE_LATENCY_TRACE_VERSION);
  assert.deepEqual(Object.keys(trace).sort(), [
    "deviceClass",
    "gestureToListeningMs",
    "networkClass",
    "revision",
    "route",
    "schemaVersion",
    "speechEndToSpeakerWriteMs",
    "stages",
    "transport",
  ]);
  assert.deepEqual(trace.stages, {
    speechEndToCommitSendMs: 200,
    commitSendToAckMs: 100,
    commitAckToFirstBinaryMs: 200,
    firstBinaryToSpeakerWriteMs: 150,
    serverCommitToDrainMs: null,
    serverDrainToActivityEndMs: null,
    activityEndToFinalMs: null,
    finalToControlCommitMs: null,
    controlCommitToFirstPcmMs: null,
  });
  assert.equal(trace.speechEndToSpeakerWriteMs, 650);
  assert.equal(Object.isFrozen(trace), true);
  assert.equal(Object.isFrozen(trace.stages), true);
});

test("all three transports and five finite routes are accepted", () => {
  for (const transport of Object.values(VOICE_LATENCY_TRANSPORTS)) {
    for (const route of [
      "http-fallback",
      "initial-answer-support",
      "continuing-coach",
      "native-conversation",
      "strict-local",
    ]) {
      assert.equal(buildVoiceLatencyTrace(validInput({ route, transport })).transport, transport);
    }
  }
});

test("monotonic inversions, stale revisions, and raw classifications fail closed", () => {
  for (const overrides of [
    { commitAcknowledgedAt: 10_100 },
    { firstBinaryAt: 10_250 },
    { speakerWriteAt: 10_400 },
    { revision: "main" },
    { deviceClass: "iPhone" },
    { networkClass: "wifi-home" },
    { gestureToListeningMs: 120_001 },
  ]) {
    assert.throws(
      () => buildVoiceLatencyTrace(validInput(overrides)),
      /voice_latency_trace_invalid/u,
    );
  }
});

test("device classification uses coarse capability, never User-Agent", () => {
  assert.equal(
    classifyVoiceLatencyDevice({ coarsePointer: true, touchPoints: 5 }),
    VOICE_LATENCY_DEVICE_CLASSES.MOBILE,
  );
  assert.equal(
    classifyVoiceLatencyDevice({ coarsePointer: false, touchPoints: 0 }),
    VOICE_LATENCY_DEVICE_CLASSES.DESKTOP,
  );
  assert.equal(classifyVoiceLatencyDevice(), VOICE_LATENCY_DEVICE_CLASSES.UNKNOWN);
});

test("network classification collapses browser hints to fixed non-identifying buckets", () => {
  assert.equal(classifyVoiceLatencyNetwork("4g"), VOICE_LATENCY_NETWORK_CLASSES.FAST);
  assert.equal(classifyVoiceLatencyNetwork("3g"), VOICE_LATENCY_NETWORK_CLASSES.TYPICAL);
  assert.equal(classifyVoiceLatencyNetwork("2g"), VOICE_LATENCY_NETWORK_CLASSES.CONSTRAINED);
  assert.equal(classifyVoiceLatencyNetwork("slow-2g"), VOICE_LATENCY_NETWORK_CLASSES.CONSTRAINED);
  assert.equal(classifyVoiceLatencyNetwork("wifi"), VOICE_LATENCY_NETWORK_CLASSES.UNKNOWN);
});

test("release revision loads off the hot path from the immutable Hosting manifest", async () => {
  const calls = [];
  const loaded = await loadVoiceLatencyRevision(async (...args) => {
    calls.push(args);
    return {
      ok: true,
      text: async () => JSON.stringify({ schemaVersion: 2, sourceCommit: revision }),
    };
  });
  assert.equal(loaded, revision);
  assert.deepEqual(calls, [["/.kotae-release-manifest.json", {
    cache: "no-store",
    credentials: "omit",
    redirect: "error",
    referrerPolicy: "no-referrer",
  }]]);
});

test("missing, malformed, and stale release manifests disable tracing", async () => {
  for (const response of [
    { ok: false, text: async () => "" },
    { ok: true, text: async () => "not-json" },
    { ok: true, text: async () => JSON.stringify({ schemaVersion: 2, sourceCommit: "main" }) },
  ]) {
    await assert.rejects(
      () => loadVoiceLatencyRevision(async () => response),
      /voice_latency_revision_invalid/u,
    );
  }
});

test("runtime binds HTTP and live milestones without awaiting diagnostics", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  assert.match(bridge, /void loadVoiceLatencyRevision\(/u);
  assert.doesNotMatch(bridge, /await loadVoiceLatencyRevision\(/u);
  assert.match(bridge, /playback\.markLatencyCommitSent\(performance\.now\(\)\)/u);
  assert.match(bridge, /playback\.markLatencyCommitAcknowledged\(performance\.now\(\)\)/u);
  assert.match(bridge, /playback\.markLatencyFirstBinary\(performance\.now\(\)\)/u);
  assert.match(bridge, /session\.playback\.markLatencyCommitAcknowledged\(acknowledgedAt\)/u);
  assert.match(bridge, /session\.playback\.markLatencyFirstBinary\(performance\.now\(\)\)/u);
  for (const transport of Object.values(VOICE_LATENCY_TRANSPORTS)) {
    assert.match(bridge, new RegExp(`"${transport}"`, "u"));
  }
  assert.match(bridge, /new CustomEvent\("kotae:voice-latency-trace", \{ detail: trace \}\)/u);
});
