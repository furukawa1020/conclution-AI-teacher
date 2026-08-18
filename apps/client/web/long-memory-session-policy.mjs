const CAPABILITY_PREFIX = "kmc1.";
const SESSION_CONTEXT_PREFIX = "kms1.";
const MAX_TOKEN_CHARACTERS = 4096;
const SESSION_CONTEXT_TTL_SECONDS = 900;

export const LONG_MEMORY_SESSION_STATES = Object.freeze({
  CANCELLED: "cancelled",
  EXPIRED: "expired",
  FAILED: "failed",
  IDLE: "idle",
  PREPARING: "preparing",
  READY: "ready",
  UNAVAILABLE: "unavailable",
});

function isGeneration(value) {
  return Number.isSafeInteger(value) && value >= 0;
}

function isOpaqueToken(value, prefix) {
  return (
    typeof value === "string" &&
    value.startsWith(prefix) &&
    value.length > prefix.length &&
    value.length <= MAX_TOKEN_CHARACTERS &&
    !/\s/u.test(value)
  );
}

function exactKeys(value, keys) {
  return (
    value !== null &&
    typeof value === "object" &&
    !Array.isArray(value) &&
    Object.keys(value).sort().join("\u0000") === [...keys].sort().join("\u0000")
  );
}

function decodeBegin(value) {
  if (exactKeys(value, ["available"]) && value.available === false) {
    return Object.freeze({ available: false });
  }
  if (
    exactKeys(value, ["available", "capability"]) &&
    value.available === true &&
    isOpaqueToken(value.capability, CAPABILITY_PREFIX)
  ) {
    return Object.freeze({ available: true, capability: value.capability });
  }
  throw new TypeError("long_memory_begin_invalid");
}

function decodeConsume(value) {
  if (
    !exactKeys(value, ["expiresInSeconds", "sessionContext"]) ||
    value.expiresInSeconds !== SESSION_CONTEXT_TTL_SECONDS ||
    !isOpaqueToken(value.sessionContext, SESSION_CONTEXT_PREFIX)
  ) {
    throw new TypeError("long_memory_consume_invalid");
  }
  return value.sessionContext;
}

async function responseJSON(response) {
  if (
    response?.ok !== true ||
    !/^application\/json(?:\s*;|$)/iu.test(response.headers?.get("Content-Type") ?? "")
  ) {
    throw new TypeError("long_memory_response_invalid");
  }
  const encoded = await response.text();
  if (encoded.length === 0 || encoded.length > MAX_TOKEN_CHARACTERS + 128) {
    throw new TypeError("long_memory_response_invalid");
  }
  return JSON.parse(encoded);
}

export function createLongMemorySessionController({
  clearTimer = (timer) => clearTimeout(timer),
  now = () => performance.now(),
  request = (...args) => fetch(...args),
  setTimer = (callback, delay) => setTimeout(callback, delay),
} = {}) {
  if (
    typeof clearTimer !== "function" ||
    typeof now !== "function" ||
    typeof request !== "function" ||
    typeof setTimer !== "function"
  ) {
    throw new TypeError("long_memory_controller_invalid");
  }

  let abortController;
  let expiresAt;
  let expiryTimer;
  let generation;
  let sessionContext;
  let state = LONG_MEMORY_SESSION_STATES.IDLE;

  const snapshot = () =>
    Object.freeze({ generation: generation ?? null, state });

  const dropSecrets = () => {
    sessionContext = undefined;
    expiresAt = undefined;
    if (expiryTimer !== undefined) {
      clearTimer(expiryTimer);
      expiryTimer = undefined;
    }
  };

  const isCurrent = (expectedGeneration, expectedController, stillCurrent) =>
    generation === expectedGeneration &&
    abortController === expectedController &&
    expectedController.signal.aborted !== true &&
    stillCurrent();

  const clear = (reason = LONG_MEMORY_SESSION_STATES.CANCELLED) => {
    if (
      reason !== LONG_MEMORY_SESSION_STATES.CANCELLED &&
      reason !== LONG_MEMORY_SESSION_STATES.EXPIRED
    ) {
      throw new TypeError("long_memory_clear_reason_invalid");
    }
    abortController?.abort();
    abortController = undefined;
    dropSecrets();
    state = reason;
    return snapshot();
  };

  const start = ({
    appCheckToken,
    beginEndpoint,
    consumeEndpoint,
    guest,
    idToken,
    isStillCurrent = () => true,
    voiceGeneration,
  }) => {
    if (
      !isGeneration(voiceGeneration) ||
      typeof guest !== "boolean" ||
      typeof isStillCurrent !== "function"
    ) {
      throw new TypeError("long_memory_start_invalid");
    }
    if (generation === voiceGeneration) return snapshot();

    abortController?.abort();
    dropSecrets();
    generation = voiceGeneration;
    if (guest) {
      abortController = undefined;
      state = LONG_MEMORY_SESSION_STATES.UNAVAILABLE;
      return snapshot();
    }
    if (
      typeof idToken !== "string" ||
      idToken.length === 0 ||
      /\s/u.test(idToken) ||
      typeof appCheckToken !== "string" ||
      appCheckToken.length === 0 ||
      /\s/u.test(appCheckToken) ||
      typeof beginEndpoint !== "string" ||
      typeof consumeEndpoint !== "string"
    ) {
      state = LONG_MEMORY_SESSION_STATES.FAILED;
      return snapshot();
    }

    const controller = new AbortController();
    abortController = controller;
    state = LONG_MEMORY_SESSION_STATES.PREPARING;
    const headers = Object.freeze({
      Accept: "application/json",
      Authorization: `Bearer ${idToken}`,
      "X-Firebase-AppCheck": appCheckToken,
    });

    void (async () => {
      let capability;
      try {
        const begin = decodeBegin(
          await responseJSON(
            await request(beginEndpoint, {
              cache: "no-store",
              credentials: "omit",
              headers,
              method: "POST",
              redirect: "error",
              referrerPolicy: "no-referrer",
              signal: controller.signal,
            }),
          ),
        );
        if (!isCurrent(voiceGeneration, controller, isStillCurrent)) return;
        if (!begin.available) {
          abortController = undefined;
          state = LONG_MEMORY_SESSION_STATES.UNAVAILABLE;
          return;
        }
        capability = begin.capability;
        const consumed = decodeConsume(
          await responseJSON(
            await request(consumeEndpoint, {
              body: JSON.stringify(Object.freeze({ capability })),
              cache: "no-store",
              credentials: "omit",
              headers: Object.freeze({
                ...headers,
                "Content-Type": "application/json",
              }),
              method: "POST",
              redirect: "error",
              referrerPolicy: "no-referrer",
              signal: controller.signal,
            }),
          ),
        );
        capability = undefined;
        if (!isCurrent(voiceGeneration, controller, isStillCurrent)) return;

        sessionContext = consumed;
        expiresAt = now() + SESSION_CONTEXT_TTL_SECONDS * 1000;
        state = LONG_MEMORY_SESSION_STATES.READY;
        abortController = undefined;
        const expire = () => {
          expiryTimer = undefined;
          if (generation !== voiceGeneration || expiresAt === undefined) return;
          const remaining = expiresAt - now();
          if (remaining <= 0) {
            clear(LONG_MEMORY_SESSION_STATES.EXPIRED);
            return;
          }
          expiryTimer = setTimer(expire, remaining);
        };
        expiryTimer = setTimer(expire, SESSION_CONTEXT_TTL_SECONDS * 1000);
      } catch {
        capability = undefined;
        if (generation === voiceGeneration && abortController === controller) {
          abortController = undefined;
          dropSecrets();
          state = controller.signal.aborted
            ? LONG_MEMORY_SESSION_STATES.CANCELLED
            : LONG_MEMORY_SESSION_STATES.FAILED;
        }
      }
    })();

    return snapshot();
  };

  return Object.freeze({ clear, snapshot, start });
}
