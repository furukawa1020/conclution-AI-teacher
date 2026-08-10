import assert from "node:assert/strict";
import test from "node:test";

import {
  beginVoicePrepareSlo,
  cancelVoicePrepareSlo,
  classifyVoicePrepareSloLatency,
  completeVoicePrepareSlo,
  createVoicePrepareSloState,
  toVoicePrepareSloWireDetail,
  VOICE_PREPARE_SLO_BUDGETS,
  VOICE_PREPARE_SLO_MAX_EVENT_MS,
  VOICE_PREPARE_SLO_OUTCOMES,
  VOICE_PREPARE_SLO_RESULTS,
  VOICE_PREPARE_SLO_ROUTES,
  VOICE_PREPARE_SLO_VERSION,
} from "../web/voice-prepare-slo-policy.mjs";

function begin(
  generation = 1,
  startedAt = 10_000,
  route = VOICE_PREPARE_SLO_ROUTES.NATIVE_READY,
) {
  return beginVoicePrepareSlo(createVoicePrepareSloState(), {
    generation,
    route,
    startedAt,
  });
}

function completeAt(state, elapsedMs, overrides = {}) {
  return completeVoicePrepareSlo(state, {
    endedAt: state.startedAt + elapsedMs,
    generation: state.generation,
    result: VOICE_PREPARE_SLO_RESULTS.READY,
    route: VOICE_PREPARE_SLO_ROUTES.NATIVE_READY,
    ...overrides,
  });
}

test("voice-prepare SLO exposes finite immutable content-free enums", () => {
  assert.deepEqual(VOICE_PREPARE_SLO_ROUTES, {
    NATIVE_READY: "native-ready",
    HTTP_FALLBACK: "http-fallback",
  });
  assert.deepEqual(VOICE_PREPARE_SLO_RESULTS, {
    READY: "ready",
    FALLBACK: "fallback",
    CANCELLED: "cancelled",
    ERROR: "error",
  });
  assert.deepEqual(VOICE_PREPARE_SLO_BUDGETS, {
    targetMs: 1_000,
    missedMs: 3_000,
    timeoutMs: 4_000,
  });
  assert.deepEqual(VOICE_PREPARE_SLO_OUTCOMES, {
    ON_TARGET: "on-target",
    SLOW: "slow",
    MISSED: "missed",
    TIMED_OUT: "timed-out",
  });
  assert.equal(VOICE_PREPARE_SLO_VERSION, 1);
  assert.equal(VOICE_PREPARE_SLO_MAX_EVENT_MS, Number.MAX_SAFE_INTEGER);
  for (const value of [
    VOICE_PREPARE_SLO_ROUTES,
    VOICE_PREPARE_SLO_RESULTS,
    VOICE_PREPARE_SLO_BUDGETS,
    VOICE_PREPARE_SLO_OUTCOMES,
  ]) {
    assert.equal(Object.isFrozen(value), true);
  }
});

test("controlled clock preserves every 1s, 3s, and 4s boundary", () => {
  const cases = [
    [999, "on-target"],
    [1_000, "on-target"],
    [1_001, "slow"],
    [2_999, "slow"],
    [3_000, "missed"],
    [3_999, "missed"],
    [4_000, "timed-out"],
  ];
  for (const [elapsedMs, outcome] of cases) {
    assert.equal(classifyVoicePrepareSloLatency(elapsedMs), outcome);
    const { state } = begin(elapsedMs + 1, 50_000);
    const completed = completeAt(state, elapsedMs);
    assert.deepEqual(completed.observation, {
      generation: elapsedMs + 1,
      latencyMs: elapsedMs,
      outcome,
      result: "ready",
      route: "native-ready",
    });
    assert.equal(Object.isFrozen(completed.observation), true);
    assert.equal(completed.state.active, false);
  }
  for (const value of [-1, Number.NaN, Number.POSITIVE_INFINITY, "1000"]) {
    assert.throws(
      () => classifyVoicePrepareSloLatency(value),
      /voice_prepare_slo_latency_invalid/u,
    );
  }
});

test("generation is safe, strictly increasing, and one preparation is active", () => {
  const empty = createVoicePrepareSloState();
  assert.deepEqual(empty, {
    active: false,
    generation: 0,
    route: null,
    startedAt: null,
  });
  assert.equal(Object.isFrozen(empty), true);

  const first = beginVoicePrepareSlo(empty, {
    generation: 7,
    route: "native-ready",
    startedAt: 100,
  });
  assert.deepEqual(first.observation, null);
  assert.deepEqual(first.state, {
    active: true,
    generation: 7,
    route: "native-ready",
    startedAt: 100,
  });

  for (const generation of [0, 7, 8, Number.MAX_SAFE_INTEGER + 1]) {
    assert.throws(
      () =>
        beginVoicePrepareSlo(first.state, {
          generation,
          route: "native-ready",
          startedAt: 200,
        }),
      /voice_prepare_slo_begin_invalid/u,
    );
  }

  const completed = completeAt(first.state, 20).state;
  assert.equal(
    beginVoicePrepareSlo(completed, {
      generation: 9,
      route: "native-ready",
      startedAt: 300,
    }).state.generation,
    9,
  );
});

test("completion permits only Native-to-HTTP fallback route movement", () => {
  const native = begin(10, 1_000).state;
  const fallback = completeAt(native, 900, {
    result: "fallback",
    route: "http-fallback",
  });
  assert.deepEqual(fallback.observation, {
    generation: 10,
    latencyMs: 900,
    outcome: "on-target",
    result: "fallback",
    route: "http-fallback",
  });
  assert.equal(fallback.state.route, "http-fallback");

  const http = begin(11, 2_000, "http-fallback").state;
  assert.equal(
    completeAt(http, 3_000, {
      result: "fallback",
      route: "http-fallback",
    }).observation.outcome,
    "missed",
  );
  assert.throws(
    () =>
      completeAt(http, 10, {
        result: "ready",
        route: "native-ready",
      }),
    /voice_prepare_slo_complete_invalid/u,
  );
  assert.throws(
    () =>
      completeAt(native, 10, {
        result: "ready",
        route: "http-fallback",
      }),
    /voice_prepare_slo_complete_invalid/u,
  );
  assert.throws(
    () => completeAt(native, 10, { result: "cancelled" }),
    /voice_prepare_slo_complete_invalid/u,
  );
});

test("complete and cancel are exactly once for the current turn", () => {
  const readyState = begin(20, 10_000).state;
  const ready = completeAt(readyState, 1_001);
  assert.equal(ready.observation.result, "ready");
  assert.throws(
    () => completeAt(ready.state, 1_002),
    /voice_prepare_slo_complete_invalid/u,
  );
  assert.throws(
    () =>
      cancelVoicePrepareSlo(ready.state, {
        endedAt: 11_002,
        generation: 20,
      }),
    /voice_prepare_slo_cancel_invalid/u,
  );

  const cancelState = begin(21, 20_000, "http-fallback").state;
  const cancelled = cancelVoicePrepareSlo(cancelState, {
    endedAt: 20_999,
    generation: 21,
  });
  assert.deepEqual(cancelled.observation, {
    generation: 21,
    latencyMs: 999,
    outcome: "on-target",
    result: "cancelled",
    route: "http-fallback",
  });
  assert.throws(
    () =>
      cancelVoicePrepareSlo(cancelled.state, {
        endedAt: 21_000,
        generation: 21,
      }),
    /voice_prepare_slo_cancel_invalid/u,
  );
});

test("error is terminal and retains the measured route", () => {
  const native = begin(30, 30_000).state;
  const errored = completeAt(native, 4_000, {
    result: "error",
  });
  assert.deepEqual(errored.observation, {
    generation: 30,
    latencyMs: 4_000,
    outcome: "timed-out",
    result: "error",
    route: "native-ready",
  });

  const switchedError = completeAt(begin(31, 40_000).state, 3_999, {
    result: "error",
    route: "http-fallback",
  });
  assert.equal(switchedError.observation.route, "http-fallback");
  assert.equal(switchedError.observation.result, "error");
});

test("stale terminal callbacks are harmless; future callbacks are rejected", () => {
  const first = completeAt(begin(40, 100_000).state, 1).state;
  const current = beginVoicePrepareSlo(first, {
    generation: 42,
    route: "native-ready",
    startedAt: 110_000,
  }).state;

  const staleComplete = completeVoicePrepareSlo(current, {
    endedAt: 1,
    generation: 40,
    result: "ready",
    route: "native-ready",
  });
  assert.equal(staleComplete.state, current);
  assert.equal(staleComplete.observation, null);
  const staleCancel = cancelVoicePrepareSlo(current, {
    endedAt: 1,
    generation: 41,
  });
  assert.equal(staleCancel.state, current);
  assert.equal(staleCancel.observation, null);

  assert.throws(
    () =>
      completeVoicePrepareSlo(current, {
        endedAt: 110_001,
        generation: 43,
        result: "ready",
        route: "native-ready",
      }),
    /voice_prepare_slo_complete_invalid/u,
  );
  assert.throws(
    () =>
      cancelVoicePrepareSlo(current, {
        endedAt: 110_001,
        generation: 43,
      }),
    /voice_prepare_slo_cancel_invalid/u,
  );
});

test("timestamps are finite and completion never precedes preparation", () => {
  const state = begin(50, 1_000).state;
  for (const endedAt of [999, Number.NaN, Number.POSITIVE_INFINITY, -1]) {
    assert.throws(
      () =>
        completeVoicePrepareSlo(state, {
          endedAt,
          generation: 50,
          result: "ready",
          route: "native-ready",
        }),
      /voice_prepare_slo_complete_invalid/u,
    );
  }
  assert.throws(
    () =>
      beginVoicePrepareSlo(createVoicePrepareSloState(), {
        generation: 1,
        route: "native-ready",
        startedAt: Number.NaN,
      }),
    /voice_prepare_slo_begin_invalid/u,
  );
});

test("every state-machine input rejects extra content-bearing fields", () => {
  const empty = createVoicePrepareSloState();
  assert.throws(
    () =>
      beginVoicePrepareSlo(empty, {
        generation: 1,
        route: "native-ready",
        startedAt: 0,
        transcript: "forbidden",
      }),
    /voice_prepare_slo_begin_invalid/u,
  );

  const state = begin(60, 1_000).state;
  assert.throws(
    () =>
      completeVoicePrepareSlo(state, {
        audio: new ArrayBuffer(0),
        endedAt: 1_001,
        generation: 60,
        result: "ready",
        route: "native-ready",
      }),
    /voice_prepare_slo_complete_invalid/u,
  );
  assert.throws(
    () =>
      cancelVoicePrepareSlo(state, {
        endedAt: 1_001,
        generation: 60,
        token: "forbidden",
      }),
    /voice_prepare_slo_cancel_invalid/u,
  );
  assert.throws(
    () =>
      completeVoicePrepareSlo(
        { ...state, session: "forbidden" },
        {
          endedAt: 1_001,
          generation: 60,
          result: "ready",
          route: "native-ready",
        },
      ),
    /voice_prepare_slo_complete_invalid/u,
  );
});

test("wire detail has exact keys and canonical outcome from rounded latency", () => {
  const raw = completeAt(begin(70, 0).state, 2_999.6).observation;
  assert.equal(raw.outcome, "slow");
  const detail = toVoicePrepareSloWireDetail(raw);
  assert.deepEqual(detail, {
    generation: 70,
    latency_ms: 3_000,
    outcome: "missed",
    result: "ready",
    route: "native-ready",
    version: 1,
  });
  assert.deepEqual(Object.keys(detail).sort(), [
    "generation",
    "latency_ms",
    "outcome",
    "result",
    "route",
    "version",
  ]);
  assert.equal(Object.isFrozen(detail), true);

  const timedOut = toVoicePrepareSloWireDetail(
    completeAt(begin(71, 0).state, 3_999.5).observation,
  );
  assert.equal(timedOut.latency_ms, 4_000);
  assert.equal(timedOut.outcome, "timed-out");

  assert.throws(
    () => toVoicePrepareSloWireDetail({ ...raw, transcript: "forbidden" }),
    /voice_prepare_slo_observation_invalid/u,
  );
  assert.throws(
    () => toVoicePrepareSloWireDetail({ ...raw, outcome: "missed" }),
    /voice_prepare_slo_observation_invalid/u,
  );
  assert.throws(
    () =>
      toVoicePrepareSloWireDetail({
        ...raw,
        latencyMs: VOICE_PREPARE_SLO_MAX_EVENT_MS + 1,
        outcome: "timed-out",
      }),
    /voice_prepare_slo_observation_invalid/u,
  );
});
