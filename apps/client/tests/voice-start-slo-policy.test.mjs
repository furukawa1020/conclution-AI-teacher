import assert from "node:assert/strict";
import test from "node:test";

import {
  advanceVoiceStartSlo,
  beginVoiceStartSlo,
  cancelVoiceStartSlo,
  classifyVoiceStartSloLatency,
  classifyVoiceStartSloRoute,
  createVoiceStartSloState,
  updateVoiceStartSloRoute,
  VOICE_START_SLO_ACTIONS,
  VOICE_START_SLO_BUDGETS,
  VOICE_START_SLO_OUTCOMES,
  VOICE_START_SLO_ROUTES,
} from "../web/voice-start-slo-policy.mjs";

const routeInput = (overrides = {}) => ({
  coachActive: false,
  coachInitiallyActive: false,
  nativeAudio: true,
  strictLocal: false,
  transportKind: "live",
  ...overrides,
});

function begin(generation = 1, startedAt = 10_000, route = "native-conversation") {
  return beginVoiceStartSlo(createVoiceStartSloState(), {
    generation,
    route,
    startedAt,
  });
}

test("voice-start SLO constants are finite, immutable enums", () => {
  assert.deepEqual(VOICE_START_SLO_ROUTES, {
    HTTP_FALLBACK: "http-fallback",
    INITIAL_ANSWER_SUPPORT: "initial-answer-support",
    CONTINUING_COACH: "continuing-coach",
    NATIVE_CONVERSATION: "native-conversation",
    STRICT_LOCAL: "strict-local",
  });
  assert.deepEqual(VOICE_START_SLO_BUDGETS, {
    targetMs: 1_000,
    missedMs: 3_000,
    stalledMs: 10_000,
  });
  assert.deepEqual(Object.values(VOICE_START_SLO_OUTCOMES).sort(), [
    "missed",
    "on-target",
    "slow",
    "stalled",
  ]);
  assert.deepEqual(Object.values(VOICE_START_SLO_ACTIONS).sort(), [
    "cancel-timers",
    "ten-second-stall",
    "three-second-miss",
  ]);
  for (const value of [
    VOICE_START_SLO_ROUTES,
    VOICE_START_SLO_BUDGETS,
    VOICE_START_SLO_OUTCOMES,
    VOICE_START_SLO_ACTIONS,
  ]) {
    assert.equal(Object.isFrozen(value), true);
  }
});

test("route classifier distinguishes the five content-free lanes", () => {
  assert.equal(
    classifyVoiceStartSloRoute(routeInput()),
    "native-conversation",
  );
  assert.equal(
    classifyVoiceStartSloRoute(routeInput({ coachActive: true })),
    "initial-answer-support",
  );
  assert.equal(
    classifyVoiceStartSloRoute(
      routeInput({ coachActive: true, coachInitiallyActive: true }),
    ),
    "continuing-coach",
  );
  assert.equal(
    classifyVoiceStartSloRoute(
      routeInput({ nativeAudio: false, transportKind: "http" }),
    ),
    "http-fallback",
  );
  assert.equal(
    classifyVoiceStartSloRoute(routeInput({ nativeAudio: false })),
    "initial-answer-support",
  );
  assert.equal(
    classifyVoiceStartSloRoute(
      routeInput({
        nativeAudio: false,
        strictLocal: true,
        transportKind: "live",
      }),
    ),
    "strict-local",
  );
  assert.equal(
    classifyVoiceStartSloRoute(
      routeInput({
        coachActive: true,
        coachInitiallyActive: true,
        nativeAudio: false,
        strictLocal: true,
        transportKind: "http",
      }),
    ),
    "strict-local",
  );
  assert.equal(
    classifyVoiceStartSloRoute(
      routeInput({
        coachActive: true,
        coachInitiallyActive: true,
        nativeAudio: false,
        transportKind: "http",
      }),
    ),
    "http-fallback",
  );
});

test("route classifier rejects wrong types, impossible lanes, and extra content", () => {
  const invalid = [
    null,
    routeInput({ transportKind: "local" }),
    routeInput({ nativeAudio: "true" }),
    routeInput({ coachInitiallyActive: true }),
    routeInput({ nativeAudio: true, transportKind: "http" }),
    routeInput({ nativeAudio: true, strictLocal: true }),
    { ...routeInput(), transcript: "content must not enter policy" },
    { ...routeInput(), token: "secret" },
    { ...routeInput(), sessionState: "opaque-state" },
  ];
  for (const value of invalid) {
    assert.throws(
      () => classifyVoiceStartSloRoute(value),
      /voice_start_slo_route_invalid/u,
    );
  }
});

test("latency outcomes use exact 1s, 3s, and 10s boundaries", () => {
  for (const milliseconds of [0, 999, 1_000]) {
    assert.equal(classifyVoiceStartSloLatency(milliseconds), "on-target");
  }
  for (const milliseconds of [1_000.1, 1_001, 2_999, 2_999.9]) {
    assert.equal(classifyVoiceStartSloLatency(milliseconds), "slow");
  }
  for (const milliseconds of [3_000, 3_001, 9_999, 9_999.9]) {
    assert.equal(classifyVoiceStartSloLatency(milliseconds), "missed");
  }
  for (const milliseconds of [10_000, 10_001, 1_000_000]) {
    assert.equal(classifyVoiceStartSloLatency(milliseconds), "stalled");
  }
  for (const value of [-1, Number.NaN, Number.POSITIVE_INFINITY, "1000", null]) {
    assert.throws(
      () => classifyVoiceStartSloLatency(value),
      /voice_start_slo_latency_invalid/u,
    );
  }
});

test("a generation starts once and superseding it cancels old timers", () => {
  const empty = createVoiceStartSloState();
  assert.equal(Object.isFrozen(empty), true);
  assert.deepEqual(empty, {
    active: false,
    generation: 0,
    missActionEmitted: false,
    route: null,
    stallActionEmitted: false,
    startedAt: null,
  });

  const first = beginVoiceStartSlo(empty, {
    generation: 4,
    route: "native-conversation",
    startedAt: 100,
  });
  assert.deepEqual(first.actions, []);
  assert.equal(first.state.active, true);
  assert.equal(first.state.generation, 4);

  const replacement = beginVoiceStartSlo(first.state, {
    generation: 5,
    route: "http-fallback",
    startedAt: 200,
  });
  assert.deepEqual(replacement.actions, ["cancel-timers"]);
  assert.equal(replacement.state.generation, 5);
  assert.equal(replacement.state.route, "http-fallback");

  for (const generation of [0, 4, 5]) {
    assert.throws(
      () =>
        beginVoiceStartSlo(replacement.state, {
          generation,
          route: "native-conversation",
          startedAt: 300,
        }),
      /voice_start_slo_begin_invalid/u,
    );
  }
});

test("dynamic Native Coach relabels one active generation without resetting time", () => {
  let { state } = begin(6, 10_000, "native-conversation");
  const missed = advanceVoiceStartSlo(state, {
    generation: 6,
    meaningfulAudio: false,
    now: 13_000,
  });
  assert.deepEqual(missed.actions, ["three-second-miss"]);
  state = missed.state;

  const updated = updateVoiceStartSloRoute(state, {
    generation: 6,
    route: "initial-answer-support",
  });
  assert.notEqual(updated, state);
  assert.equal(updated.route, "initial-answer-support");
  assert.equal(updated.generation, state.generation);
  assert.equal(updated.startedAt, state.startedAt);
  assert.equal(updated.active, state.active);
  assert.equal(updated.missActionEmitted, state.missActionEmitted);
  assert.equal(updated.stallActionEmitted, state.stallActionEmitted);

  const stalled = advanceVoiceStartSlo(updated, {
    generation: 6,
    meaningfulAudio: false,
    now: 20_000,
  });
  assert.deepEqual(stalled.actions, ["ten-second-stall"]);
  assert.equal(stalled.state.startedAt, 10_000);
});

test("route updates are same-state for identical and stale generations", () => {
  const { state } = begin(20, 200_000, "native-conversation");
  assert.equal(
    updateVoiceStartSloRoute(state, {
      generation: 20,
      route: "native-conversation",
    }),
    state,
  );
  assert.equal(
    updateVoiceStartSloRoute(state, {
      generation: 19,
      route: "continuing-coach",
    }),
    state,
  );

  const completed = advanceVoiceStartSlo(state, {
    generation: 20,
    meaningfulAudio: true,
    now: 200_500,
  }).state;
  assert.equal(
    updateVoiceStartSloRoute(completed, {
      generation: 20,
      route: "native-conversation",
    }),
    completed,
  );
});

test("route updates reject reverse, cross-lane, future, and inactive changes", () => {
  const native = begin(30, 300_000, "native-conversation").state;
  for (const route of [
    "continuing-coach",
    "http-fallback",
    "strict-local",
  ]) {
    assert.throws(
      () => updateVoiceStartSloRoute(native, { generation: 30, route }),
      /voice_start_slo_route_update_invalid/u,
    );
  }
  assert.throws(
    () =>
      updateVoiceStartSloRoute(native, {
        generation: 31,
        route: "initial-answer-support",
      }),
    /voice_start_slo_route_update_invalid/u,
  );

  const initial = updateVoiceStartSloRoute(native, {
    generation: 30,
    route: "initial-answer-support",
  });
  assert.throws(
    () =>
      updateVoiceStartSloRoute(initial, {
        generation: 30,
        route: "native-conversation",
      }),
    /voice_start_slo_route_update_invalid/u,
  );

  for (const route of ["continuing-coach", "http-fallback", "strict-local"]) {
    const lane = begin(40, 400_000, route).state;
    assert.throws(
      () =>
        updateVoiceStartSloRoute(lane, {
          generation: 40,
          route: "initial-answer-support",
        }),
      /voice_start_slo_route_update_invalid/u,
    );
  }

  const completed = advanceVoiceStartSlo(native, {
    generation: 30,
    meaningfulAudio: true,
    now: 300_500,
  }).state;
  assert.throws(
    () =>
      updateVoiceStartSloRoute(completed, {
        generation: 30,
        route: "initial-answer-support",
      }),
    /voice_start_slo_route_update_invalid/u,
  );
});

test("route update input is exact and content-free", () => {
  const { state } = begin(50, 500_000, "native-conversation");
  for (const value of [
    null,
    { generation: "50", route: "initial-answer-support" },
    { generation: 50, route: "unknown" },
    {
      generation: 50,
      route: "initial-answer-support",
      transcript: "forbidden",
    },
    {
      generation: 50,
      route: "initial-answer-support",
      sessionState: "forbidden",
    },
  ]) {
    assert.throws(
      () => updateVoiceStartSloRoute(state, value),
      /voice_start_slo_route_update_invalid/u,
    );
  }
});

test("explicit cancellation stops only the current active generation", () => {
  let { state } = begin(60, 600_000, "continuing-coach");
  state = advanceVoiceStartSlo(state, {
    generation: 60,
    meaningfulAudio: false,
    now: 603_000,
  }).state;
  const cancelled = cancelVoiceStartSlo(state, { generation: 60 });
  assert.deepEqual(cancelled.actions, ["cancel-timers"]);
  assert.equal(cancelled.measurement, null);
  assert.equal(cancelled.state.active, false);
  assert.equal(cancelled.state.generation, state.generation);
  assert.equal(cancelled.state.route, state.route);
  assert.equal(cancelled.state.startedAt, state.startedAt);
  assert.equal(cancelled.state.missActionEmitted, state.missActionEmitted);
  assert.equal(cancelled.state.stallActionEmitted, state.stallActionEmitted);

  const duplicate = cancelVoiceStartSlo(cancelled.state, { generation: 60 });
  assert.equal(duplicate.state, cancelled.state);
  assert.deepEqual(duplicate.actions, []);
  assert.equal(duplicate.measurement, null);
});

test("stale and future cancellation callbacks are harmless no-ops", () => {
  const { state } = begin(70, 700_000);
  for (const generation of [69, 71]) {
    const ignored = cancelVoiceStartSlo(state, { generation });
    assert.equal(ignored.state, state);
    assert.deepEqual(ignored.actions, []);
    assert.equal(ignored.measurement, null);
  }
});

test("cancellation input is exact and content-free", () => {
  const { state } = begin(80, 800_000);
  for (const value of [
    null,
    { generation: 0 },
    { generation: "80" },
    { generation: 80, transcript: "forbidden" },
    { generation: 80, token: "forbidden" },
  ]) {
    assert.throws(
      () => cancelVoiceStartSlo(state, value),
      /voice_start_slo_cancel_invalid/u,
    );
  }
});

test("3s and 10s timer actions are emitted exactly once", () => {
  let { state } = begin(7, 20_000);

  let advanced = advanceVoiceStartSlo(state, {
    generation: 7,
    meaningfulAudio: false,
    now: 22_999,
  });
  assert.deepEqual(advanced.actions, []);
  assert.equal(advanced.state, state);

  advanced = advanceVoiceStartSlo(state, {
    generation: 7,
    meaningfulAudio: false,
    now: 23_000,
  });
  assert.deepEqual(advanced.actions, ["three-second-miss"]);
  state = advanced.state;

  advanced = advanceVoiceStartSlo(state, {
    generation: 7,
    meaningfulAudio: false,
    now: 29_999,
  });
  assert.deepEqual(advanced.actions, []);
  assert.equal(advanced.state, state);

  advanced = advanceVoiceStartSlo(state, {
    generation: 7,
    meaningfulAudio: false,
    now: 30_000,
  });
  assert.deepEqual(advanced.actions, ["ten-second-stall"]);
  state = advanced.state;

  advanced = advanceVoiceStartSlo(state, {
    generation: 7,
    meaningfulAudio: false,
    now: 40_000,
  });
  assert.deepEqual(advanced.actions, []);
  assert.equal(advanced.state, state);
});

test("a late event-loop wake preserves both finite deadline actions", () => {
  const { state } = begin(8, 1_000);
  const advanced = advanceVoiceStartSlo(state, {
    generation: 8,
    meaningfulAudio: false,
    now: 11_000,
  });
  assert.deepEqual(advanced.actions, [
    "three-second-miss",
    "ten-second-stall",
  ]);
  assert.equal(advanced.state.missActionEmitted, true);
  assert.equal(advanced.state.stallActionEmitted, true);
});

test("first meaningful audio cancels timers and publishes one measurement", () => {
  const { state } = begin(9, 50_000, "initial-answer-support");
  const first = advanceVoiceStartSlo(state, {
    generation: 9,
    meaningfulAudio: true,
    now: 50_999,
  });
  assert.deepEqual(first.actions, ["cancel-timers"]);
  assert.deepEqual(first.measurement, {
    generation: 9,
    latencyMs: 999,
    outcome: "on-target",
    route: "initial-answer-support",
  });
  assert.equal(Object.isFrozen(first.measurement), true);
  assert.equal(first.state.active, false);

  const duplicate = advanceVoiceStartSlo(first.state, {
    generation: 9,
    meaningfulAudio: true,
    now: 51_500,
  });
  assert.deepEqual(duplicate.actions, []);
  assert.equal(duplicate.measurement, null);
  assert.equal(duplicate.state, first.state);
});

test("meaningful audio also closes deadline gaps when a timer callback was late", () => {
  const { state } = begin(10, 70_000, "continuing-coach");
  const missed = advanceVoiceStartSlo(state, {
    generation: 10,
    meaningfulAudio: true,
    now: 73_000,
  });
  assert.deepEqual(missed.actions, [
    "three-second-miss",
    "cancel-timers",
  ]);
  assert.equal(missed.measurement.outcome, "missed");

  const next = beginVoiceStartSlo(missed.state, {
    generation: 11,
    route: "http-fallback",
    startedAt: 80_000,
  });
  const stalled = advanceVoiceStartSlo(next.state, {
    generation: 11,
    meaningfulAudio: true,
    now: 90_000,
  });
  assert.deepEqual(stalled.actions, [
    "three-second-miss",
    "ten-second-stall",
    "cancel-timers",
  ]);
  assert.equal(stalled.measurement.outcome, "stalled");
});

test("stale and future generations cannot mutate the current turn", () => {
  const { state } = begin(12, 100_000);
  for (const generation of [11, 13]) {
    const ignored = advanceVoiceStartSlo(state, {
      generation,
      meaningfulAudio: true,
      now: 110_000,
    });
    assert.equal(ignored.state, state);
    assert.deepEqual(ignored.actions, []);
    assert.equal(ignored.measurement, null);
  }
});

test("state-machine APIs reject content fields and malformed timing control", () => {
  const { state } = begin(14, 120_000);
  const badAdvance = [
    { generation: 14, meaningfulAudio: true, now: 119_999 },
    { generation: 14, meaningfulAudio: 1, now: 120_000 },
    {
      generation: 14,
      meaningfulAudio: true,
      now: 120_000,
      audio: new ArrayBuffer(2),
    },
    {
      generation: 14,
      meaningfulAudio: true,
      now: 120_000,
      transcript: "forbidden",
    },
  ];
  for (const value of badAdvance) {
    assert.throws(
      () => advanceVoiceStartSlo(state, value),
      /voice_start_slo_advance_invalid/u,
    );
  }

  assert.throws(
    () =>
      beginVoiceStartSlo(state, {
        generation: 15,
        route: "native-conversation",
        startedAt: 130_000,
        token: "forbidden",
      }),
    /voice_start_slo_begin_invalid/u,
  );
  assert.throws(
    () =>
      advanceVoiceStartSlo(
        { ...state, sessionState: "forbidden" },
        { generation: 14, meaningfulAudio: false, now: 120_000 },
      ),
    /voice_start_slo_advance_invalid/u,
  );
});
