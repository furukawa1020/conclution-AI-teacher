// Content-free policy for the current turn's first meaningful-audio SLO.
//
// This module deliberately accepts only finite route/timing control values. It
// has no API for PCM, captions, transcripts, credentials, or conversation
// state. Callers own real timers; the pure state machine returns finite actions
// that tell the caller when those timers must be cancelled or reported.

export const VOICE_START_SLO_ROUTES = Object.freeze({
  HTTP_FALLBACK: "http-fallback",
  INITIAL_ANSWER_SUPPORT: "initial-answer-support",
  CONTINUING_COACH: "continuing-coach",
  NATIVE_CONVERSATION: "native-conversation",
  STRICT_LOCAL: "strict-local",
});

export const VOICE_START_SLO_BUDGETS = Object.freeze({
  targetMs: 1_000,
  missedMs: 3_000,
  stalledMs: 10_000,
});

export const VOICE_START_SLO_OUTCOMES = Object.freeze({
  ON_TARGET: "on-target",
  SLOW: "slow",
  MISSED: "missed",
  STALLED: "stalled",
});

export const VOICE_START_SLO_ACTIONS = Object.freeze({
  CANCEL_TIMERS: "cancel-timers",
  THREE_SECOND_MISS: "three-second-miss",
  TEN_SECOND_STALL: "ten-second-stall",
});

const ROUTE_INPUT_KEYS = Object.freeze([
  "coachActive",
  "coachInitiallyActive",
  "nativeAudio",
  "strictLocal",
  "transportKind",
]);
const BEGIN_INPUT_KEYS = Object.freeze([
  "generation",
  "route",
  "startedAt",
]);
const ADVANCE_INPUT_KEYS = Object.freeze([
  "generation",
  "meaningfulAudio",
  "now",
]);
const UPDATE_ROUTE_INPUT_KEYS = Object.freeze(["generation", "route"]);
const CANCEL_INPUT_KEYS = Object.freeze(["generation"]);
const STATE_KEYS = Object.freeze([
  "active",
  "generation",
  "missActionEmitted",
  "route",
  "stallActionEmitted",
  "startedAt",
]);
const ROUTE_VALUES = Object.freeze(Object.values(VOICE_START_SLO_ROUTES));
const EMPTY_ACTIONS = Object.freeze([]);

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function hasExactKeys(value, expectedKeys) {
  return (
    isPlainRecord(value) &&
    Object.keys(value).sort().join("\u0000") === expectedKeys.join("\u0000")
  );
}

function finiteTimestamp(value) {
  return Number.isFinite(value) && value >= 0;
}

function validGeneration(value, allowZero = false) {
  return (
    Number.isSafeInteger(value) &&
    (allowZero ? value >= 0 : value > 0)
  );
}

function validRoute(value) {
  return typeof value === "string" && ROUTE_VALUES.includes(value);
}

function validState(value) {
  if (
    !hasExactKeys(value, STATE_KEYS) ||
    typeof value.active !== "boolean" ||
    !validGeneration(value.generation, true) ||
    typeof value.missActionEmitted !== "boolean" ||
    typeof value.stallActionEmitted !== "boolean" ||
    (value.stallActionEmitted && !value.missActionEmitted)
  ) {
    return false;
  }
  if (value.generation === 0) {
    return (
      !value.active &&
      value.route === null &&
      value.startedAt === null &&
      !value.missActionEmitted &&
      !value.stallActionEmitted
    );
  }
  return validRoute(value.route) && finiteTimestamp(value.startedAt);
}

function freezeState({
  active,
  generation,
  missActionEmitted,
  route,
  stallActionEmitted,
  startedAt,
}) {
  return Object.freeze({
    active,
    generation,
    missActionEmitted,
    route,
    stallActionEmitted,
    startedAt,
  });
}

function transition(state, actions = EMPTY_ACTIONS, measurement = null) {
  return Object.freeze({
    actions:
      actions === EMPTY_ACTIONS
        ? EMPTY_ACTIONS
        : Object.freeze(Array.from(actions)),
    measurement,
    state,
  });
}

export function classifyVoiceStartSloRoute(value) {
  if (
    !hasExactKeys(value, ROUTE_INPUT_KEYS) ||
    !["http", "live"].includes(value.transportKind) ||
    typeof value.nativeAudio !== "boolean" ||
    typeof value.coachInitiallyActive !== "boolean" ||
    typeof value.coachActive !== "boolean" ||
    typeof value.strictLocal !== "boolean"
  ) {
    throw new TypeError("voice_start_slo_route_invalid");
  }

  const {
    coachActive,
    coachInitiallyActive,
    nativeAudio,
    strictLocal,
    transportKind,
  } = value;
  if (coachInitiallyActive && !coachActive) {
    throw new TypeError("voice_start_slo_route_invalid");
  }
  if (strictLocal) {
    // Strict minimization owns the route label even when a previously active
    // Coach snapshot forces the existing transport onto HTTP fallback. The
    // snapshot is lifecycle control, not permission to expose its contents.
    if (nativeAudio) {
      throw new TypeError("voice_start_slo_route_invalid");
    }
    return VOICE_START_SLO_ROUTES.STRICT_LOCAL;
  }
  if (transportKind === "http") {
    if (nativeAudio) {
      throw new TypeError("voice_start_slo_route_invalid");
    }
    return VOICE_START_SLO_ROUTES.HTTP_FALLBACK;
  }
  if (!nativeAudio) {
    return coachInitiallyActive
      ? VOICE_START_SLO_ROUTES.CONTINUING_COACH
      : VOICE_START_SLO_ROUTES.INITIAL_ANSWER_SUPPORT;
  }
  if (coachInitiallyActive) {
    return VOICE_START_SLO_ROUTES.CONTINUING_COACH;
  }
  if (coachActive) {
    return VOICE_START_SLO_ROUTES.INITIAL_ANSWER_SUPPORT;
  }
  return VOICE_START_SLO_ROUTES.NATIVE_CONVERSATION;
}

export function classifyVoiceStartSloLatency(milliseconds) {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) {
    throw new TypeError("voice_start_slo_latency_invalid");
  }
  if (milliseconds <= VOICE_START_SLO_BUDGETS.targetMs) {
    return VOICE_START_SLO_OUTCOMES.ON_TARGET;
  }
  if (milliseconds < VOICE_START_SLO_BUDGETS.missedMs) {
    return VOICE_START_SLO_OUTCOMES.SLOW;
  }
  if (milliseconds < VOICE_START_SLO_BUDGETS.stalledMs) {
    return VOICE_START_SLO_OUTCOMES.MISSED;
  }
  return VOICE_START_SLO_OUTCOMES.STALLED;
}

export function createVoiceStartSloState() {
  return freezeState({
    active: false,
    generation: 0,
    missActionEmitted: false,
    route: null,
    stallActionEmitted: false,
    startedAt: null,
  });
}

export function beginVoiceStartSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, BEGIN_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    value.generation <= state.generation ||
    !validRoute(value.route) ||
    !finiteTimestamp(value.startedAt)
  ) {
    throw new TypeError("voice_start_slo_begin_invalid");
  }
  const actions = state.active
    ? [VOICE_START_SLO_ACTIONS.CANCEL_TIMERS]
    : EMPTY_ACTIONS;
  return transition(
    freezeState({
      active: true,
      generation: value.generation,
      missActionEmitted: false,
      route: value.route,
      stallActionEmitted: false,
      startedAt: value.startedAt,
    }),
    actions,
  );
}

export function updateVoiceStartSloRoute(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, UPDATE_ROUTE_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    !validRoute(value.route)
  ) {
    throw new TypeError("voice_start_slo_route_update_invalid");
  }

  // A callback owned by an older turn cannot relabel the current turn. A
  // future generation has not been started and is therefore an integration
  // error rather than another harmless stale callback.
  if (value.generation < state.generation) {
    return state;
  }
  if (value.generation > state.generation) {
    throw new TypeError("voice_start_slo_route_update_invalid");
  }
  if (value.route === state.route) {
    return state;
  }
  if (
    !state.active ||
    state.route !== VOICE_START_SLO_ROUTES.NATIVE_CONVERSATION ||
    value.route !== VOICE_START_SLO_ROUTES.INITIAL_ANSWER_SUPPORT
  ) {
    throw new TypeError("voice_start_slo_route_update_invalid");
  }

  // Route activation happens after commit and before response PCM. Preserve
  // the original speech-end clock and every exactly-once deadline flag.
  return freezeState({ ...state, route: value.route });
}

export function cancelVoiceStartSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, CANCEL_INPUT_KEYS) ||
    !validGeneration(value.generation)
  ) {
    throw new TypeError("voice_start_slo_cancel_invalid");
  }
  if (!state.active || value.generation !== state.generation) {
    return transition(state);
  }
  return transition(
    freezeState({ ...state, active: false }),
    [VOICE_START_SLO_ACTIONS.CANCEL_TIMERS],
  );
}

export function advanceVoiceStartSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, ADVANCE_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    typeof value.meaningfulAudio !== "boolean" ||
    !finiteTimestamp(value.now)
  ) {
    throw new TypeError("voice_start_slo_advance_invalid");
  }

  // Old timer callbacks and future-generation callbacks are harmless. Only
  // the currently active generation may emit an action or measurement.
  if (!state.active || value.generation !== state.generation) {
    return transition(state);
  }
  if (value.now < state.startedAt) {
    throw new TypeError("voice_start_slo_advance_invalid");
  }

  const elapsedMs = value.now - state.startedAt;
  let missActionEmitted = state.missActionEmitted;
  let stallActionEmitted = state.stallActionEmitted;
  const actions = [];
  if (
    elapsedMs >= VOICE_START_SLO_BUDGETS.missedMs &&
    !missActionEmitted
  ) {
    missActionEmitted = true;
    actions.push(VOICE_START_SLO_ACTIONS.THREE_SECOND_MISS);
  }
  if (
    elapsedMs >= VOICE_START_SLO_BUDGETS.stalledMs &&
    !stallActionEmitted
  ) {
    // Crossing ten seconds necessarily crosses three seconds too. The ordered
    // action list preserves both exactly once when the event loop wakes late.
    stallActionEmitted = true;
    actions.push(VOICE_START_SLO_ACTIONS.TEN_SECOND_STALL);
  }

  if (!value.meaningfulAudio) {
    if (
      missActionEmitted === state.missActionEmitted &&
      stallActionEmitted === state.stallActionEmitted
    ) {
      return transition(state);
    }
    return transition(
      freezeState({
        ...state,
        missActionEmitted,
        stallActionEmitted,
      }),
      actions,
    );
  }

  actions.push(VOICE_START_SLO_ACTIONS.CANCEL_TIMERS);
  const measurement = Object.freeze({
    generation: state.generation,
    latencyMs: elapsedMs,
    outcome: classifyVoiceStartSloLatency(elapsedMs),
    route: state.route,
  });
  return transition(
    freezeState({
      ...state,
      active: false,
      missActionEmitted,
      stallActionEmitted,
    }),
    actions,
    measurement,
  );
}
