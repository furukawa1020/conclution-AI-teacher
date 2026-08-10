// Content-free policy for measuring voice-provider preparation.
//
// Only generation, route, result, and monotonic-clock timestamps may enter this
// module. It deliberately has no API for audio, text, credentials, session
// payloads, or conversation state.

export const VOICE_PREPARE_SLO_ROUTES = Object.freeze({
  NATIVE_READY: "native-ready",
  HTTP_FALLBACK: "http-fallback",
});

export const VOICE_PREPARE_SLO_RESULTS = Object.freeze({
  READY: "ready",
  FALLBACK: "fallback",
  CANCELLED: "cancelled",
  ERROR: "error",
});

export const VOICE_PREPARE_SLO_BUDGETS = Object.freeze({
  targetMs: 1_000,
  missedMs: 3_000,
  timeoutMs: 4_000,
});

export const VOICE_PREPARE_SLO_OUTCOMES = Object.freeze({
  ON_TARGET: "on-target",
  SLOW: "slow",
  MISSED: "missed",
  TIMED_OUT: "timed-out",
});

export const VOICE_PREPARE_SLO_VERSION = 1;
export const VOICE_PREPARE_SLO_MAX_EVENT_MS = Number.MAX_SAFE_INTEGER;

const ROUTE_VALUES = Object.freeze(Object.values(VOICE_PREPARE_SLO_ROUTES));
const RESULT_VALUES = Object.freeze(Object.values(VOICE_PREPARE_SLO_RESULTS));
const OUTCOME_VALUES = Object.freeze(Object.values(VOICE_PREPARE_SLO_OUTCOMES));
const BEGIN_INPUT_KEYS = Object.freeze(["generation", "route", "startedAt"]);
const COMPLETE_INPUT_KEYS = Object.freeze([
  "endedAt",
  "generation",
  "result",
  "route",
]);
const CANCEL_INPUT_KEYS = Object.freeze(["endedAt", "generation"]);
const STATE_KEYS = Object.freeze([
  "active",
  "generation",
  "route",
  "startedAt",
]);
const OBSERVATION_KEYS = Object.freeze([
  "generation",
  "latencyMs",
  "outcome",
  "result",
  "route",
]);

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

function validGeneration(value, allowZero = false) {
  return (
    Number.isSafeInteger(value) &&
    (allowZero ? value >= 0 : value > 0)
  );
}

function finiteTimestamp(value) {
  return Number.isFinite(value) && value >= 0;
}

function validRoute(value) {
  return typeof value === "string" && ROUTE_VALUES.includes(value);
}

function validResult(value) {
  return typeof value === "string" && RESULT_VALUES.includes(value);
}

function validOutcome(value) {
  return typeof value === "string" && OUTCOME_VALUES.includes(value);
}

function validState(value) {
  if (
    !hasExactKeys(value, STATE_KEYS) ||
    typeof value.active !== "boolean" ||
    !validGeneration(value.generation, true)
  ) {
    return false;
  }
  if (value.generation === 0) {
    return !value.active && value.route === null && value.startedAt === null;
  }
  return validRoute(value.route) && finiteTimestamp(value.startedAt);
}

function validRouteTransition(from, to) {
  return (
    from === to ||
    (from === VOICE_PREPARE_SLO_ROUTES.NATIVE_READY &&
      to === VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK)
  );
}

function validTerminalResult(result, route) {
  if (result === VOICE_PREPARE_SLO_RESULTS.READY) {
    return route === VOICE_PREPARE_SLO_ROUTES.NATIVE_READY;
  }
  if (result === VOICE_PREPARE_SLO_RESULTS.FALLBACK) {
    return route === VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK;
  }
  return result === VOICE_PREPARE_SLO_RESULTS.ERROR;
}

function freezeState({ active, generation, route, startedAt }) {
  return Object.freeze({ active, generation, route, startedAt });
}

function transition(state, observation = null) {
  return Object.freeze({ observation, state });
}

function finish(state, { endedAt, generation, result, route }) {
  const latencyMs = endedAt - state.startedAt;
  const observation = Object.freeze({
    generation,
    latencyMs,
    outcome: classifyVoicePrepareSloLatency(latencyMs),
    result,
    route,
  });
  return transition(
    freezeState({ ...state, active: false, route }),
    observation,
  );
}

export function classifyVoicePrepareSloLatency(milliseconds) {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) {
    throw new TypeError("voice_prepare_slo_latency_invalid");
  }
  if (milliseconds <= VOICE_PREPARE_SLO_BUDGETS.targetMs) {
    return VOICE_PREPARE_SLO_OUTCOMES.ON_TARGET;
  }
  if (milliseconds < VOICE_PREPARE_SLO_BUDGETS.missedMs) {
    return VOICE_PREPARE_SLO_OUTCOMES.SLOW;
  }
  if (milliseconds < VOICE_PREPARE_SLO_BUDGETS.timeoutMs) {
    return VOICE_PREPARE_SLO_OUTCOMES.MISSED;
  }
  return VOICE_PREPARE_SLO_OUTCOMES.TIMED_OUT;
}

export function createVoicePrepareSloState() {
  return freezeState({
    active: false,
    generation: 0,
    route: null,
    startedAt: null,
  });
}

export function beginVoicePrepareSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, BEGIN_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    state.active ||
    value.generation <= state.generation ||
    !validRoute(value.route) ||
    !finiteTimestamp(value.startedAt)
  ) {
    throw new TypeError("voice_prepare_slo_begin_invalid");
  }
  return transition(
    freezeState({
      active: true,
      generation: value.generation,
      route: value.route,
      startedAt: value.startedAt,
    }),
  );
}

export function completeVoicePrepareSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, COMPLETE_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    !validRoute(value.route) ||
    !validResult(value.result) ||
    value.result === VOICE_PREPARE_SLO_RESULTS.CANCELLED ||
    !finiteTimestamp(value.endedAt)
  ) {
    throw new TypeError("voice_prepare_slo_complete_invalid");
  }
  if (value.generation < state.generation) {
    return transition(state);
  }
  if (
    value.generation > state.generation ||
    !state.active ||
    value.endedAt < state.startedAt ||
    !validRouteTransition(state.route, value.route) ||
    !validTerminalResult(value.result, value.route)
  ) {
    throw new TypeError("voice_prepare_slo_complete_invalid");
  }
  return finish(state, value);
}

export function cancelVoicePrepareSlo(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, CANCEL_INPUT_KEYS) ||
    !validGeneration(value.generation) ||
    !finiteTimestamp(value.endedAt)
  ) {
    throw new TypeError("voice_prepare_slo_cancel_invalid");
  }
  if (value.generation < state.generation) {
    return transition(state);
  }
  if (
    value.generation > state.generation ||
    !state.active ||
    value.endedAt < state.startedAt
  ) {
    throw new TypeError("voice_prepare_slo_cancel_invalid");
  }
  return finish(state, {
    ...value,
    result: VOICE_PREPARE_SLO_RESULTS.CANCELLED,
    route: state.route,
  });
}

export function toVoicePrepareSloWireDetail(observation) {
  if (
    !hasExactKeys(observation, OBSERVATION_KEYS) ||
    !validGeneration(observation.generation) ||
    !Number.isFinite(observation.latencyMs) ||
    observation.latencyMs < 0 ||
    observation.latencyMs > VOICE_PREPARE_SLO_MAX_EVENT_MS ||
    !validOutcome(observation.outcome) ||
    observation.outcome !==
      classifyVoicePrepareSloLatency(observation.latencyMs) ||
    !validResult(observation.result) ||
    !validRoute(observation.route) ||
    (observation.result === VOICE_PREPARE_SLO_RESULTS.READY &&
      observation.route !== VOICE_PREPARE_SLO_ROUTES.NATIVE_READY) ||
    (observation.result === VOICE_PREPARE_SLO_RESULTS.FALLBACK &&
      observation.route !== VOICE_PREPARE_SLO_ROUTES.HTTP_FALLBACK)
  ) {
    throw new TypeError("voice_prepare_slo_observation_invalid");
  }
  const latencyMs = Math.round(observation.latencyMs);
  if (!Number.isSafeInteger(latencyMs)) {
    throw new TypeError("voice_prepare_slo_observation_invalid");
  }
  return Object.freeze({
    generation: observation.generation,
    latency_ms: latencyMs,
    outcome: classifyVoicePrepareSloLatency(latencyMs),
    result: observation.result,
    route: observation.route,
    version: VOICE_PREPARE_SLO_VERSION,
  });
}
