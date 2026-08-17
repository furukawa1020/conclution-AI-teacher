// Content-free, current-run acceptance contract for the guest A-first sprint.
// Raw times are consumed synchronously and never appear in the returned event.

export const GUEST_A_FIRST_SLO_VERSION = 1;
export const GUEST_A_FIRST_SLO_BUDGETS = Object.freeze({
  completionMs: 30_000,
  listeningStartMs: 1_000,
  responseStartMs: 1_000,
});

export const GUEST_A_FIRST_SLO_OUTCOMES = Object.freeze({
  aiSubstitutionDetected: "detected",
  aiSubstitutionZero: "zero",
  completionNotVerified: "not_verified",
  completionOverTarget: "over_target",
  completionWithinTarget: "within_target",
  counterexampleRejected: "rejected",
  counterexampleVerifiedRun: "verified_run",
  startMissed: "missed",
  startNotObserved: "not_observed",
  startOnTarget: "on_target",
});

export function createGuestStartGate() {
  let generation = 0;
  let activeGeneration = null;

  function invalidate() {
    generation = generation >= Number.MAX_SAFE_INTEGER ? 1 : generation + 1;
    activeGeneration = null;
  }

  return Object.freeze({
    begin() {
      if (activeGeneration !== null) return null;
      generation = generation >= Number.MAX_SAFE_INTEGER ? 1 : generation + 1;
      activeGeneration = generation;
      return generation;
    },
    clear() {
      invalidate();
    },
    finish(candidate) {
      if (candidate !== activeGeneration) return false;
      activeGeneration = null;
      return true;
    },
    isCurrent(candidate) {
      return Number.isSafeInteger(candidate) && candidate > 0 && candidate === activeGeneration;
    },
  });
}

const EVENT_KEYS = Object.freeze([
  "aiSubstitution",
  "completion",
  "counterexample",
  "listeningStart",
  "responseStart",
  "version",
]);

function finiteTime(value) {
  return Number.isFinite(value) && value >= 0 && value <= Number.MAX_SAFE_INTEGER;
}

function classifyStart(startedAt, observedAt, budget) {
  if (observedAt === undefined) return GUEST_A_FIRST_SLO_OUTCOMES.startNotObserved;
  if (!finiteTime(startedAt) || !finiteTime(observedAt) || observedAt < startedAt) {
    return GUEST_A_FIRST_SLO_OUTCOMES.startNotObserved;
  }
  return observedAt - startedAt <= budget
    ? GUEST_A_FIRST_SLO_OUTCOMES.startOnTarget
    : GUEST_A_FIRST_SLO_OUTCOMES.startMissed;
}

function exactProof(input) {
  if (!input || typeof input !== "object") return false;
  const keys = Object.keys(input).sort();
  const expected = [
    "aiOutputBeforeAnswer",
    "answerProof",
    "coachAction",
    "coachPhase",
    "guestAFirstOutcome",
    "transitionProof",
  ];
  if (keys.length !== expected.length || keys.some((key, index) => key !== expected[index])) {
    return false;
  }
  if (
    input.aiOutputBeforeAnswer !== false ||
    input.coachPhase !== "complete" ||
    input.coachAction !== "complete" ||
    input.answerProof !== "question_bound_input_answer_first"
  ) {
    return false;
  }
  if (input.guestAFirstOutcome === "changed_to_answer_first") {
    return input.transitionProof === "question_bound_input_clause_later_to_first";
  }
  return input.guestAFirstOutcome === "stayed_answer_first" && input.transitionProof === "none";
}

function immutableEvent(fields) {
  return Object.freeze({
    aiSubstitution: fields.aiSubstitution,
    completion: fields.completion,
    counterexample: fields.counterexample,
    listeningStart: fields.listeningStart,
    responseStart: fields.responseStart,
    version: GUEST_A_FIRST_SLO_VERSION,
  });
}

export function createGuestAFirstSprintSlo() {
  let startedAt;
  let listeningAt;
  let questionEndedAt;
  let responseStartedAt;
  let state = "idle";

  function clear() {
    startedAt = undefined;
    listeningAt = undefined;
    questionEndedAt = undefined;
    responseStartedAt = undefined;
    state = "idle";
  }

  return Object.freeze({
    begin(at) {
      clear();
      if (!finiteTime(at)) return false;
      startedAt = at;
      state = "running";
      return true;
    },
    clear,
    markListening(at) {
      if (state !== "running" || !finiteTime(at)) {
        clear();
        return false;
      }
      if (at < startedAt || listeningAt !== undefined) {
        return false;
      }
      listeningAt = at;
      return true;
    },
    markQuestionEnded(at) {
      if (questionEndedAt !== undefined) {
        return at === questionEndedAt;
      }
      if (
        state !== "running" ||
        listeningAt === undefined ||
        !finiteTime(at) ||
        at < listeningAt
      ) {
        clear();
        return false;
      }
      questionEndedAt = at;
      return true;
    },
    markResponseStarted(at) {
      if (
        state !== "running" ||
        questionEndedAt === undefined ||
        responseStartedAt !== undefined ||
        !finiteTime(at) ||
        at < questionEndedAt
      ) {
        return false;
      }
      responseStartedAt = at;
      return true;
    },
    finish(at, proof) {
      const proofVerified = exactProof(proof);
      const clockValid =
        state === "running" && finiteTime(at) && at >= startedAt;
      const aiSubstitution = proof?.aiOutputBeforeAnswer === true
        ? GUEST_A_FIRST_SLO_OUTCOMES.aiSubstitutionDetected
        : GUEST_A_FIRST_SLO_OUTCOMES.aiSubstitutionZero;
      const listeningStart = classifyStart(
        startedAt,
        listeningAt,
        GUEST_A_FIRST_SLO_BUDGETS.listeningStartMs,
      );
      const responseStart = classifyStart(
        questionEndedAt,
        responseStartedAt,
        GUEST_A_FIRST_SLO_BUDGETS.responseStartMs,
      );
      const verified =
        clockValid && proofVerified &&
        listeningStart !== GUEST_A_FIRST_SLO_OUTCOMES.startNotObserved &&
        responseStart !== GUEST_A_FIRST_SLO_OUTCOMES.startNotObserved;
      const completion = !verified
        ? GUEST_A_FIRST_SLO_OUTCOMES.completionNotVerified
        : at - startedAt <= GUEST_A_FIRST_SLO_BUDGETS.completionMs
          ? GUEST_A_FIRST_SLO_OUTCOMES.completionWithinTarget
          : GUEST_A_FIRST_SLO_OUTCOMES.completionOverTarget;
      const event = immutableEvent({
        aiSubstitution,
        completion,
        counterexample: verified
          ? GUEST_A_FIRST_SLO_OUTCOMES.counterexampleVerifiedRun
          : GUEST_A_FIRST_SLO_OUTCOMES.counterexampleRejected,
        listeningStart,
        responseStart,
      });
      clear();
      return event;
    },
  });
}

export function validateGuestAFirstSloBatch(events) {
  if (!Array.isArray(events) || events.length !== 100) return false;
  let listeningOnTarget = 0;
  let responseOnTarget = 0;
  for (const event of events) {
    if (
      !event || typeof event !== "object" ||
      Object.keys(event).sort().some((key, index) => key !== EVENT_KEYS[index]) ||
      Object.keys(event).length !== EVENT_KEYS.length ||
      event.version !== GUEST_A_FIRST_SLO_VERSION ||
      event.aiSubstitution !== GUEST_A_FIRST_SLO_OUTCOMES.aiSubstitutionZero ||
      event.counterexample !== GUEST_A_FIRST_SLO_OUTCOMES.counterexampleVerifiedRun ||
      event.completion !== GUEST_A_FIRST_SLO_OUTCOMES.completionWithinTarget
    ) {
      return false;
    }
    if (event.listeningStart === GUEST_A_FIRST_SLO_OUTCOMES.startOnTarget) listeningOnTarget += 1;
    if (event.responseStart === GUEST_A_FIRST_SLO_OUTCOMES.startOnTarget) responseOnTarget += 1;
  }
  return listeningOnTarget >= 95 && responseOnTarget >= 95;
}
