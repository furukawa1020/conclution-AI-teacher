// Content-free capability policy for one short-lived Native Audio preflight.
//
// The binding and lease identifiers are opaque, non-reversible values. Exact
// input shapes deliberately reject credentials, audio, captions, questions,
// answers, session state, and other conversation content.

export const NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS = 15_000;

export const NATIVE_PREFLIGHT_PROTOCOL_LIMITS = Object.freeze({
  maximumGeneration: Number.MAX_SAFE_INTEGER,
  maximumSessionContextCharacters: 4_096,
  maximumSessionStateCharacters: 16 * 1_024,
  maximumTokenCharacters: 8_192,
  sampleRateHz: 16_000,
});

export const NATIVE_PREFLIGHT_LEASE_STATES = Object.freeze({
  IDLE: "idle",
  CONNECTING: "connecting",
  READY: "ready",
  CLAIMED: "claimed",
  RETIRED: "retired",
  EXPIRED: "expired",
});

export const NATIVE_PREFLIGHT_RETIRE_REASONS = Object.freeze({
  CANCELLED: "cancelled",
  PAGE_HIDDEN: "page-hidden",
  DEVICE_CHANGED: "device-changed",
  IDENTITY_CHANGED: "identity-changed",
  REPLACED: "replaced",
  PROVIDER_CLOSED: "provider-closed",
});

const BINDING_PATTERN = /^knb1_[A-Za-z0-9_-]{43}$/u;
const LEASE_PATTERN = /^knl1_[A-Za-z0-9_-]{43}$/u;
const BEGIN_KEYS = Object.freeze([
  "binding",
  "expiresAt",
  "generation",
  "startedAt",
]);
const READY_KEYS = Object.freeze([
  "binding",
  "generation",
  "leaseId",
  "readyAt",
]);
const CLAIM_KEYS = Object.freeze([
  "binding",
  "claimedAt",
  "generation",
]);
const RETIRE_KEYS = Object.freeze([
  "generation",
  "reason",
  "retiredAt",
]);
const STATE_KEYS = Object.freeze([
  "binding",
  "expiresAt",
  "generation",
  "leaseId",
  "readyAt",
  "startedAt",
  "state",
]);
const STATE_VALUES = Object.freeze(Object.values(NATIVE_PREFLIGHT_LEASE_STATES));
const RETIRE_REASON_VALUES = Object.freeze(
  Object.values(NATIVE_PREFLIGHT_RETIRE_REASONS),
);

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function hasExactKeys(value, expected) {
  return (
    isPlainRecord(value) &&
    Object.keys(value).sort().join("\u0000") === expected.join("\u0000")
  );
}

function finiteTime(value) {
  return Number.isFinite(value) && value >= 0;
}

function validGeneration(value, allowZero = false) {
  return Number.isSafeInteger(value) && (allowZero ? value >= 0 : value > 0);
}

function validBinding(value) {
  return typeof value === "string" && BINDING_PATTERN.test(value);
}

function validLeaseId(value) {
  return typeof value === "string" && LEASE_PATTERN.test(value);
}

function validCredential(value) {
  return (
    typeof value === "string" &&
    value.length <= NATIVE_PREFLIGHT_PROTOCOL_LIMITS.maximumTokenCharacters &&
    /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/u.test(value)
  );
}

function frozenState(value) {
  return Object.freeze({
    binding: value.binding,
    expiresAt: value.expiresAt,
    generation: value.generation,
    leaseId: value.leaseId,
    readyAt: value.readyAt,
    startedAt: value.startedAt,
    state: value.state,
  });
}

function validState(value) {
  if (
    !hasExactKeys(value, STATE_KEYS) ||
    !STATE_VALUES.includes(value.state) ||
    !validGeneration(value.generation, true)
  ) {
    return false;
  }
  if (value.state === NATIVE_PREFLIGHT_LEASE_STATES.IDLE) {
    return (
      value.generation === 0 &&
      value.binding === null &&
      value.expiresAt === null &&
      value.leaseId === null &&
      value.readyAt === null &&
      value.startedAt === null
    );
  }
  if (
    !validBinding(value.binding) ||
    !finiteTime(value.startedAt) ||
    !finiteTime(value.expiresAt) ||
    value.expiresAt <= value.startedAt ||
    value.expiresAt - value.startedAt > NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS
  ) {
    return false;
  }
  if (value.state === NATIVE_PREFLIGHT_LEASE_STATES.CONNECTING) {
    return value.readyAt === null && value.leaseId === null;
  }
  if (value.state === NATIVE_PREFLIGHT_LEASE_STATES.READY) {
    return (
      finiteTime(value.readyAt) &&
      value.readyAt >= value.startedAt &&
      value.readyAt <= value.expiresAt &&
      validLeaseId(value.leaseId)
    );
  }
  return value.readyAt === null && value.leaseId === null;
}

function terminalState(state, terminal) {
  return frozenState({
    ...state,
    leaseId: null,
    readyAt: null,
    state: terminal,
  });
}

function transition(state, capability = null) {
  return Object.freeze({ capability, state });
}

export function createNativePreflightLeaseState() {
  return frozenState({
    binding: null,
    expiresAt: null,
    generation: 0,
    leaseId: null,
    readyAt: null,
    startedAt: null,
    state: NATIVE_PREFLIGHT_LEASE_STATES.IDLE,
  });
}

export function beginNativePreflightLease(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, BEGIN_KEYS) ||
    !validGeneration(value.generation) ||
    value.generation <= state.generation ||
    !validBinding(value.binding) ||
    !finiteTime(value.startedAt) ||
    !finiteTime(value.expiresAt) ||
    value.expiresAt <= value.startedAt ||
    value.expiresAt - value.startedAt > NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS ||
    state.state === NATIVE_PREFLIGHT_LEASE_STATES.CONNECTING ||
    state.state === NATIVE_PREFLIGHT_LEASE_STATES.READY
  ) {
    throw new TypeError("native_preflight_lease_begin_invalid");
  }
  return transition(
    frozenState({
      ...value,
      leaseId: null,
      readyAt: null,
      state: NATIVE_PREFLIGHT_LEASE_STATES.CONNECTING,
    }),
  );
}

export function readyNativePreflightLease(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, READY_KEYS) ||
    state.state !== NATIVE_PREFLIGHT_LEASE_STATES.CONNECTING ||
    value.generation !== state.generation ||
    value.binding !== state.binding ||
    !validLeaseId(value.leaseId) ||
    !finiteTime(value.readyAt) ||
    value.readyAt < state.startedAt ||
    value.readyAt > state.expiresAt
  ) {
    throw new TypeError("native_preflight_lease_ready_invalid");
  }
  return transition(
    frozenState({
      ...state,
      leaseId: value.leaseId,
      readyAt: value.readyAt,
      state: NATIVE_PREFLIGHT_LEASE_STATES.READY,
    }),
  );
}

export function claimNativePreflightLease(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, CLAIM_KEYS) ||
    state.state !== NATIVE_PREFLIGHT_LEASE_STATES.READY ||
    value.generation !== state.generation ||
    value.binding !== state.binding ||
    !finiteTime(value.claimedAt) ||
    value.claimedAt < state.readyAt
  ) {
    throw new TypeError("native_preflight_lease_claim_invalid");
  }
  if (value.claimedAt > state.expiresAt) {
    return transition(
      terminalState(state, NATIVE_PREFLIGHT_LEASE_STATES.EXPIRED),
    );
  }
  const capability = Object.freeze({
    generation: state.generation,
    leaseId: state.leaseId,
  });
  return transition(
    terminalState(state, NATIVE_PREFLIGHT_LEASE_STATES.CLAIMED),
    capability,
  );
}

export function retireNativePreflightLease(state, value) {
  if (
    !validState(state) ||
    !hasExactKeys(value, RETIRE_KEYS) ||
    !validGeneration(value.generation) ||
    !RETIRE_REASON_VALUES.includes(value.reason) ||
    !finiteTime(value.retiredAt)
  ) {
    throw new TypeError("native_preflight_lease_retire_invalid");
  }
  if (value.generation < state.generation) return transition(state);
  if (
    value.generation !== state.generation ||
    (state.state !== NATIVE_PREFLIGHT_LEASE_STATES.CONNECTING &&
      state.state !== NATIVE_PREFLIGHT_LEASE_STATES.READY) ||
    value.retiredAt < state.startedAt
  ) {
    throw new TypeError("native_preflight_lease_retire_invalid");
  }
  return transition(
    terminalState(state, NATIVE_PREFLIGHT_LEASE_STATES.RETIRED),
  );
}

export function createNativePreflightFrame(value) {
  if (
    !hasExactKeys(value, ["appCheckToken", "generation", "idToken"]) ||
    !validCredential(value.appCheckToken) ||
    !validGeneration(value.generation) ||
    !validCredential(value.idToken)
  ) {
    throw new TypeError("native_preflight_frame_invalid");
  }
  return Object.freeze({
    appCheckToken: value.appCheckToken,
    generation: value.generation,
    idToken: value.idToken,
    type: "preflight",
    version: 1,
  });
}

export function acceptNativePreflightReady(value, receivedAt) {
  if (
    !hasExactKeys(value, [
      "expiresInMs",
      "generation",
      "leaseId",
      "type",
      "version",
    ]) ||
    value.type !== "preflight-ready" ||
    value.version !== 1 ||
    !validLeaseId(value.leaseId) ||
    !validGeneration(value.generation) ||
    !Number.isSafeInteger(value.expiresInMs) ||
    value.expiresInMs <= 0 ||
    value.expiresInMs > NATIVE_PREFLIGHT_LEASE_MAX_TTL_MS ||
    !finiteTime(receivedAt)
  ) {
    throw new TypeError("native_preflight_ready_invalid");
  }
  return Object.freeze({
    expiresAt: receivedAt + value.expiresInMs,
    generation: value.generation,
    leaseId: value.leaseId,
  });
}

export function createNativePreflightActivateFrame(value) {
  const expectedKeys = [
    "generation",
    "leaseId",
    ...(value?.latencyProofVersion === 1 ? ["latencyProofVersion"] : []),
    "nativeAudio",
    "nativeCoachControl",
    "sampleRateHz",
    ...(value?.sessionContext === undefined ? [] : ["sessionContext"]),
    "sessionState",
    "strictCloudMinimization",
    "turnMode",
  ].sort();
  if (
    !hasExactKeys(value, expectedKeys) ||
    !validGeneration(value.generation) ||
    !validLeaseId(value.leaseId) ||
    value.nativeAudio !== true ||
    value.nativeCoachControl !== true ||
    value.strictCloudMinimization !== false ||
    value.sampleRateHz !== NATIVE_PREFLIGHT_PROTOCOL_LIMITS.sampleRateHz ||
    typeof value.sessionState !== "string" ||
    value.sessionState.length >
      NATIVE_PREFLIGHT_PROTOCOL_LIMITS.maximumSessionStateCharacters ||
    value.sessionState.trim() !== value.sessionState ||
    (value.sessionContext !== undefined &&
      (typeof value.sessionContext !== "string" ||
        !value.sessionContext.startsWith("kms1.") ||
        value.sessionContext.length >
          NATIVE_PREFLIGHT_PROTOCOL_LIMITS.maximumSessionContextCharacters ||
        /\s/u.test(value.sessionContext))) ||
    !["ambient", "foreground", "intentional"].includes(value.turnMode) ||
    (value.latencyProofVersion !== undefined &&
      value.latencyProofVersion !== 1)
  ) {
    throw new TypeError("native_preflight_activate_invalid");
  }
  return Object.freeze({
    generation: value.generation,
    leaseId: value.leaseId,
    ...(value.latencyProofVersion === undefined
      ? {}
      : { latencyProofVersion: value.latencyProofVersion }),
    nativeAudio: true,
    nativeCoachControl: true,
    sampleRateHz: value.sampleRateHz,
    ...(value.sessionContext === undefined
      ? {}
      : { sessionContext: value.sessionContext }),
    sessionState: value.sessionState,
    strictCloudMinimization: false,
    turnMode: value.turnMode,
    type: "activate",
    version: 1,
  });
}
