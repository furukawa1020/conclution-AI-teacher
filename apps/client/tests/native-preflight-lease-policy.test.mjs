import assert from "node:assert/strict";
import test from "node:test";

import {
  beginNativePreflightLease,
  claimNativePreflightLease,
  createNativePreflightLeaseState,
  NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS,
  NATIVE_PREFLIGHT_LEASE_STATES,
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
