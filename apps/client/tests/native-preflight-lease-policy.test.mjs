import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  acceptNativePreflightReady,
  beginNativePreflightLease,
  claimNativePreflightLease,
  createNativePreflightActivateFrame,
  createNativePreflightFrame,
  createNativePreflightInvalidationGate,
  createNativePreflightLatencyObservation,
  createNativePreflightLeaseState,
  NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS,
  NATIVE_PREFLIGHT_LEASE_STATES,
  NATIVE_PREFLIGHT_LATENCY_VERSION,
  NATIVE_PREFLIGHT_PROTOCOL_LIMITS,
  NATIVE_PREFLIGHT_RETIRE_REASONS,
  readyNativePreflightLease,
  retireNativePreflightLease,
} from "../web/native-preflight-lease-policy.mjs";

const BINDING_A = `knb1_${"a".repeat(43)}`;
const BINDING_B = `knb1_${"b".repeat(43)}`;
const LEASE_A = `knl1_${"c".repeat(43)}`;

function begin(generation = 1, startedAt = 1_000, ttl = 10_000) {
  return beginNativePreflightLease(createNativePreflightLeaseState(), {
    binding: BINDING_A,
    expiresAt: startedAt + ttl,
    generation,
    startedAt,
  }).state;
}

function ready(state, readyAt = state.startedAt + 100) {
  return readyNativePreflightLease(state, {
    binding: BINDING_A,
    generation: state.generation,
    leaseId: LEASE_A,
    readyAt,
  }).state;
}

test("preflight lease exposes finite immutable states and a 15 second ceiling", () => {
  assert.equal(NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS, 15_000);
  assert.deepEqual(NATIVE_PREFLIGHT_LEASE_STATES, {
    IDLE: "idle",
    CONNECTING: "connecting",
    READY: "ready",
    CLAIMED: "claimed",
    RETIRED: "retired",
    EXPIRED: "expired",
  });
  assert.equal(Object.isFrozen(NATIVE_PREFLIGHT_LEASE_STATES), true);
  assert.equal(Object.isFrozen(NATIVE_PREFLIGHT_RETIRE_REASONS), true);
});

test("latency observation fixes cold, warm, and zero post-speech wait", () => {
  const observation = createNativePreflightLatencyObservation({
    coldMs: 812.34,
    generation: 9,
    warmMs: 138.26,
  });
  assert.deepEqual(observation, {
    coldMs: 812.3,
    generation: 9,
    speechEndConnectionWaitMs: 0,
    version: NATIVE_PREFLIGHT_LATENCY_VERSION,
    warmMs: 138.3,
  });
  assert.equal(Object.isFrozen(observation), true);
});

test("latency observation rejects impossible clocks and surplus content", () => {
  const valid = { coldMs: 800, generation: 1, warmMs: 120 };
  for (const override of [
    { coldMs: -1 },
    { coldMs: 15_001 },
    { generation: 0 },
    { warmMs: 801 },
    { transcript: "secret" },
  ]) {
    assert.throws(
      () => createNativePreflightLatencyObservation({ ...valid, ...override }),
      /native_preflight_latency_observation_invalid/u,
    );
  }
});

test("browser preflight invalidation cancels one current owner exactly once", () => {
  const cancelled = [];
  const gate = createNativePreflightInvalidationGate();
  const first = gate.begin(1, (reason) => cancelled.push([1, reason]));
  assert.equal(gate.hasActive(), true);
  assert.equal(
    gate.retire(first, NATIVE_PREFLIGHT_RETIRE_REASONS.PAGE_HIDDEN),
    true,
  );
  assert.equal(
    gate.retire(first, NATIVE_PREFLIGHT_RETIRE_REASONS.PAGE_HIDDEN),
    false,
  );
  assert.deepEqual(cancelled, [[1, "page-hidden"]]);
  assert.equal(gate.hasActive(), false);
});

test("a stale callback cannot retire the replacement generation", () => {
  const cancelled = [];
  const gate = createNativePreflightInvalidationGate();
  const first = gate.begin(10, (reason) => cancelled.push([10, reason]));
  const second = gate.begin(11, (reason) => cancelled.push([11, reason]));
  assert.deepEqual(cancelled, [[10, "replaced"]]);
  assert.equal(
    gate.retire(first, NATIVE_PREFLIGHT_RETIRE_REASONS.PROVIDER_CLOSED),
    false,
  );
  assert.equal(gate.release(first), false);
  assert.equal(gate.hasActive(), true);
  assert.equal(gate.release(second), true);
  assert.equal(gate.hasActive(), false);
  assert.deepEqual(cancelled, [[10, "replaced"]]);
});

test("browser invalidation accepts only finite reasons and generations", () => {
  const gate = createNativePreflightInvalidationGate();
  assert.throws(
    () => gate.begin(0, () => {}),
    /native_preflight_invalidation_begin_invalid/u,
  );
  const owner = gate.begin(1, () => {});
  assert.throws(
    () => gate.retire(owner, "transcript-or-device-label"),
    /native_preflight_invalidation_reason_invalid/u,
  );
  assert.equal(gate.hasActive(), true);
});

test("release transfers ownership without invoking cancellation", () => {
  let cancellations = 0;
  const gate = createNativePreflightInvalidationGate();
  const owner = gate.begin(4, () => {
    cancellations += 1;
  });
  assert.equal(gate.release(owner), true);
  assert.equal(gate.release(owner), false);
  assert.equal(
    gate.retireCurrent(NATIVE_PREFLIGHT_RETIRE_REASONS.CANCELLED),
    false,
  );
  assert.equal(cancellations, 0);
});

test("browser lifecycle has one centralized preflight invalidation path", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  assert.equal(
    bridge.match(/addEventListener\?\.\("devicechange"/gu)?.length,
    1,
  );
  assert.match(
    bridge,
    /onIdTokenChanged\(auth,[\s\S]*hasIdentityBoundVoiceSession\(\)[\s\S]*stopSession\("identity_changed"\)/u,
  );
  assert.match(
    bridge,
    /observedAuthInstances\.has\(auth\)[\s\S]*observedAuthInstances\.add\(auth\)/u,
  );
  assert.match(
    bridge,
    /function stopSession\([\s\S]*nativePreflightInvalidation\.retireCurrent\(preflightRetireReason\)/u,
  );
  assert.match(
    bridge,
    /preparingLiveSession = session;\s*releasePreflightOwnership\(\);\s*try/u,
  );
  assert.match(
    bridge,
    /new CustomEvent\("kotae:native-preflight-latency", \{ detail \}\)/u,
  );
  assert.match(
    bridge,
    /coldMs: preflightAuthReadyMs,[\s\S]*warmMs: strongReadyAt - preflightActivatedAt/u,
  );
});

test("one lease moves connecting to ready to exactly-once claimed", () => {
  const connecting = begin(7);
  assert.equal(connecting.state, "connecting");
  const prepared = ready(connecting);
  const claimed = claimNativePreflightLease(prepared, {
    binding: BINDING_A,
    claimedAt: 1_500,
    generation: 7,
  });
  assert.deepEqual(claimed.capability, {
    generation: 7,
    leaseId: LEASE_A,
  });
  assert.equal(Object.isFrozen(claimed.capability), true);
  assert.equal(claimed.state.state, "claimed");
  assert.equal(claimed.state.leaseId, null);
  assert.throws(
    () =>
      claimNativePreflightLease(claimed.state, {
        binding: BINDING_A,
        claimedAt: 1_501,
        generation: 7,
      }),
    /native_preflight_lease_claim_invalid/u,
  );
});

test("an active connecting or ready lease prevents a second connection", () => {
  for (const state of [begin(2), ready(begin(3))]) {
    assert.throws(
      () =>
        beginNativePreflightLease(state, {
          binding: BINDING_A,
          expiresAt: 20_000,
          generation: state.generation + 1,
          startedAt: 10_000,
        }),
      /native_preflight_lease_begin_invalid/u,
    );
  }
});

test("identity and generation mismatches cannot ready or claim a lease", () => {
  const connecting = begin(9);
  assert.throws(
    () =>
      readyNativePreflightLease(connecting, {
        binding: BINDING_B,
        generation: 9,
        leaseId: LEASE_A,
        readyAt: 1_100,
      }),
    /native_preflight_lease_ready_invalid/u,
  );
  const prepared = ready(connecting);
  for (const value of [
    { binding: BINDING_B, claimedAt: 1_200, generation: 9 },
    { binding: BINDING_A, claimedAt: 1_200, generation: 10 },
  ]) {
    assert.throws(
      () => claimNativePreflightLease(prepared, value),
      /native_preflight_lease_claim_invalid/u,
    );
  }
});

test("expired claim returns no capability and destroys the lease identifier", () => {
  const prepared = ready(begin(11, 5_000, 500), 5_100);
  const expired = claimNativePreflightLease(prepared, {
    binding: BINDING_A,
    claimedAt: 5_501,
    generation: 11,
  });
  assert.equal(expired.capability, null);
  assert.equal(expired.state.state, "expired");
  assert.equal(expired.state.leaseId, null);
});

test("TTL boundaries reject zero, negative, and over 15 seconds", () => {
  for (const ttl of [0, -1, 15_001]) {
    assert.throws(
      () => begin(1, 1_000, ttl),
      /native_preflight_lease_begin_invalid/u,
    );
  }
  assert.equal(begin(1, 1_000, 15_000).expiresAt, 16_000);
});

test("retirement clears capabilities and stale callbacks are harmless", () => {
  const prepared = ready(begin(20));
  const retired = retireNativePreflightLease(prepared, {
    generation: 20,
    reason: NATIVE_PREFLIGHT_RETIRE_REASONS.PAGE_HIDDEN,
    retiredAt: 1_300,
  });
  assert.equal(retired.state.state, "retired");
  assert.equal(retired.state.leaseId, null);

  const next = beginNativePreflightLease(retired.state, {
    binding: BINDING_A,
    expiresAt: 30_000,
    generation: 21,
    startedAt: 20_000,
  }).state;
  assert.equal(
    retireNativePreflightLease(next, {
      generation: 20,
      reason: NATIVE_PREFLIGHT_RETIRE_REASONS.PROVIDER_CLOSED,
      retiredAt: 20_100,
    }).state,
    next,
  );
  assert.throws(
    () =>
      readyNativePreflightLease(next, {
        binding: BINDING_A,
        generation: 20,
        leaseId: LEASE_A,
        readyAt: 20_200,
      }),
    /native_preflight_lease_ready_invalid/u,
  );
});

test("exact input shapes reject content and credential smuggling", () => {
  for (const extra of [
    { audio: new ArrayBuffer(1) },
    { caption: "秘密" },
    { idToken: "secret" },
    { question: "答えは？" },
    { sessionState: "state" },
  ]) {
    assert.throws(
      () =>
        beginNativePreflightLease(createNativePreflightLeaseState(), {
          binding: BINDING_A,
          expiresAt: 2_000,
          generation: 1,
          startedAt: 1_000,
          ...extra,
        }),
      /native_preflight_lease_begin_invalid/u,
    );
  }
});

test("wire preflight can contain only credentials and generation", () => {
  const frame = createNativePreflightFrame({
    appCheckToken: "app.check.token",
    generation: 12,
    idToken: "identity.jwt.token",
  });
  assert.deepEqual(frame, {
    appCheckToken: "app.check.token",
    generation: 12,
    idToken: "identity.jwt.token",
    type: "preflight",
    version: 1,
  });
  assert.equal(Object.isFrozen(frame), true);
  for (const extra of [
    { audio: "AA" },
    { caption: "秘密" },
    { question: "答えは？" },
    { sessionState: "state" },
    { turnMode: "intentional" },
  ]) {
    assert.throws(
      () =>
        createNativePreflightFrame({
          appCheckToken: "app.check.token",
          generation: 12,
          idToken: "identity.jwt.token",
          ...extra,
        }),
      /native_preflight_frame_invalid/u,
    );
  }
});

test("browser and server share finite protocol limits", () => {
  assert.deepEqual(NATIVE_PREFLIGHT_PROTOCOL_LIMITS, {
    maximumGeneration: Number.MAX_SAFE_INTEGER,
    maximumSessionContextCharacters: 4_096,
    maximumSessionStateCharacters: 16_384,
    maximumTokenCharacters: 8_192,
    sampleRateHz: 16_000,
  });
  for (const credential of [
    "secret",
    "header..signature",
    "header.pay+load.signature",
    `a.${"b".repeat(8_190)}.c`,
  ]) {
    assert.throws(
      () =>
        createNativePreflightFrame({
          appCheckToken: credential,
          generation: 1,
          idToken: "identity.jwt.token",
        }),
      /native_preflight_frame_invalid/u,
    );
  }
});

test("preflight ready is exact, opaque, and bounded to 15 seconds", () => {
  const ready = acceptNativePreflightReady(
    {
      expiresInMs: 15_000,
      generation: 15,
      leaseId: LEASE_A,
      type: "preflight-ready",
      version: 1,
    },
    20_000,
  );
  assert.deepEqual(ready, {
    expiresAt: 35_000,
    generation: 15,
    leaseId: LEASE_A,
  });
  for (const invalid of [0, 15_001, 1.5]) {
    assert.throws(
      () =>
        acceptNativePreflightReady(
          {
            expiresInMs: invalid,
            generation: 15,
            leaseId: LEASE_A,
            type: "preflight-ready",
            version: 1,
          },
          20_000,
        ),
      /native_preflight_ready_invalid/u,
    );
  }
});

test("activation sends no credential and binds the exact lease generation", () => {
  const activation = createNativePreflightActivateFrame({
    generation: 18,
    leaseId: LEASE_A,
    latencyProofVersion: 1,
    nativeAudio: true,
    nativeCoachControl: true,
    sampleRateHz: 16_000,
    sessionState: "",
    strictCloudMinimization: false,
    turnMode: "intentional",
  });
  assert.deepEqual(Object.keys(activation).sort(), [
    "generation",
    "latencyProofVersion",
    "leaseId",
    "nativeAudio",
    "nativeCoachControl",
    "sampleRateHz",
    "sessionState",
    "strictCloudMinimization",
    "turnMode",
    "type",
    "version",
  ]);
  assert.equal(activation.type, "activate");
  assert.equal("idToken" in activation, false);
  assert.equal("appCheckToken" in activation, false);
});

test("activation rejects strict mode, non-Native routes, and surplus content", () => {
  const valid = {
    generation: 20,
    leaseId: LEASE_A,
    nativeAudio: true,
    nativeCoachControl: true,
    sampleRateHz: 16_000,
    sessionState: "",
    strictCloudMinimization: false,
    turnMode: "foreground",
  };
  for (const override of [
    { strictCloudMinimization: true },
    { nativeAudio: false },
    { nativeCoachControl: false },
    { sampleRateHz: 48_000 },
    { idToken: "identity.jwt.token" },
    { audio: "AA" },
  ]) {
    assert.throws(
      () => createNativePreflightActivateFrame({ ...valid, ...override }),
      /native_preflight_activate_invalid/u,
    );
  }
});
