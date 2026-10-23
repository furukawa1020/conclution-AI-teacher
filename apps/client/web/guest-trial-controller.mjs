// Not mounted by the guest UI until the committed-voice route is connected.
// Control state is memory-only and must never contain transcripts or audio.
const QUESTION_ID = "rest_activity_v1";
const QUESTION = "少し休めるとしたら、何をしたいですか？";
const KEYS = ["version", "id", "questionId", "question", "generation", "assisted", "status", "expiresAt"];
const CONTROL_PATH = "/api/v1/guest-trials/control";
const VOICE_PATH = "/api/v1/guest-trials/voice";
const MAX_CONTROL_RESPONSE_BYTES = 2048 + 16384;

function invalid() { throw new Error("guest_trial_invalid"); }

function controlPayload(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) invalid();
  const keys = value.action === "begin" ? ["action"] : value.action === "read"
    ? ["action", "id"] : ["help", "stop"].includes(value.action) ? ["action", "id", "generation"] : [];
  if (!keys.length || Object.keys(value).length !== keys.length ||
      keys.some(key => !Object.hasOwn(value, key))) invalid();
  if (value.action !== "begin" && (typeof value.id !== "string" || !/^[a-f0-9]{64}$/u.test(value.id))) invalid();
  if (keys.includes("generation") && ![1, 2].includes(value.generation)) invalid();
  return JSON.stringify(Object.fromEntries(keys.map(key => [key, value[key]])));
}

// Only the composition root supplies origin and credentials. Never accept a
// destination from a trial response. No mutation is automatically retried,
// including after an expired credential or an ambiguous network failure.
export function createGuestTrialTransport(options) {
  return createTrialTransport(options, false);
}

export function createGuestTrialVoiceTransport(options) {
  return createTrialTransport(options, true);
}

// Consumes the provided view, including rejection. Its encoded request is
// transient; neither audio nor tokens are written to browser storage.
function voicePayload(value, now) {
  const audio = value?.audio;
  try {
    if (!value || Object.keys(value).length !== 2 || !Object.hasOwn(value, "trial") ||
        !(audio instanceof Uint8Array) || !(audio.buffer instanceof ArrayBuffer) ||
        audio.byteLength < 2 || audio.byteLength > 960000 || audio.byteLength % 2) invalid();
    const trial = validateGuestTrial(value.trial, now);
    if (trial.status !== "pending" || !trial.state) invalid();
    const chunks = [];
    for (let offset = 0; offset < audio.length; offset += 8192) {
      chunks.push(String.fromCharCode(...audio.subarray(offset, offset + 8192)));
    }
    return { trial, body: JSON.stringify({id: trial.id, generation: trial.generation,
      state: trial.state, pcm16: btoa(chunks.join(""))}) };
  } finally {
    if (audio instanceof Uint8Array) audio.fill(0);
  }
}

function createTrialTransport({
  origin, credentials, isCurrentGuest, request = (...args) => fetch(...args),
  now = Date.now, timeoutMs,
}, voice) {
  timeoutMs ??= voice ? 15000 : 5000;
  let url;
  try { url = new URL(origin); } catch { invalid(); }
  if (url.protocol !== "https:" || url.origin !== origin ||
      typeof credentials !== "function" || typeof isCurrentGuest !== "function" ||
      typeof request !== "function" || typeof now !== "function" ||
      !Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > (voice ? 15000 : 10000)) invalid();
  const endpoint = `${origin}${voice ? VOICE_PATH : CONTROL_PATH}`;

  return async (payload, { signal } = {}) => {
    const encoded = voice ? voicePayload(payload, now()) : {body: controlPayload(payload)};
    const body = encoded.body;
    if (!signal || typeof signal.addEventListener !== "function") invalid();
    const owned = new AbortController();
    const cancel = () => owned.abort();
    signal.addEventListener("abort", cancel, { once: true });
    if (signal.aborted) cancel();
    const timer = setTimeout(cancel, timeoutMs);
    let rejectAbort;
    const cancelled = new Promise((_, reject) => {
      rejectAbort = () => reject(new Error("guest_trial_cancelled"));
      owned.signal.addEventListener("abort", rejectAbort, { once: true });
      if (owned.signal.aborted) rejectAbort();
    });
    const check = () => {
      if (owned.signal.aborted) throw new Error("guest_trial_cancelled");
      if (isCurrentGuest() !== true) throw new Error("guest_trial_session_changed");
    };
    const send = async () => {
      check();
      const tokens = await credentials({ signal: owned.signal });
      check();
      for (const key of ["idToken", "appCheckToken"]) {
        if (typeof tokens?.[key] !== "string" || tokens[key].length < 1 ||
            tokens[key].length > 16384 || /[^\x21-\x7e]/u.test(tokens[key])) {
          throw new Error("guest_trial_authentication_failed");
        }
      }
      const response = await request(endpoint, {
        method: "POST", mode: "cors", credentials: "omit", cache: "no-store",
        redirect: "error", referrerPolicy: "no-referrer", signal: owned.signal,
        headers: {
          Authorization: `Bearer ${tokens.idToken}`,
          "X-Firebase-AppCheck": tokens.appCheckToken,
          "Content-Type": "application/json",
        },
        body,
      });
      try {
        check();
      } catch (error) {
        void response?.body?.cancel().catch(() => {});
        throw error;
      }
      if (!response || response.redirected || response.url !== endpoint ||
          response.status !== 200 ||
          !/^application\/json(?:\s*;|$)/iu.test(response.headers?.get("Content-Type") ?? "") ||
          !response.body || typeof response.body.getReader !== "function") {
        void response?.body?.cancel().catch(() => {});
        const code = response?.status === 401 || response?.status === 403
          ? "guest_trial_authentication_failed" : response?.status === 409
          ? "guest_trial_conflict" : response?.status === 410
          ? "guest_trial_expired" : "guest_trial_unavailable";
        throw new Error(code);
      }
      const reader = response.body.getReader();
      const cancelRead = () => { void reader.cancel().catch(() => {}); };
      owned.signal.addEventListener("abort", cancelRead, { once: true });
      try {
        const decoder = new TextDecoder("utf-8", { fatal: true });
        let bytes = 0;
        let text = "";
        while (true) {
          check();
          const chunk = await reader.read();
          check();
          if (chunk.done) break;
          if (!(chunk.value instanceof Uint8Array)) invalid();
          bytes += chunk.value.byteLength;
          if (bytes > MAX_CONTROL_RESPONSE_BYTES) invalid();
          text += decoder.decode(chunk.value, { stream: true });
        }
        text += decoder.decode();
        const result = validateGuestTrial(JSON.parse(text), now());
        if (voice && (result.id !== encoded.trial.id ||
            result.expiresAt !== encoded.trial.expiresAt || result.assisted !== encoded.trial.assisted ||
            result.generation !== encoded.trial.generation + 1 ||
            !["answer_first", "unverified"].includes(result.status))) invalid();
        return result;
      } finally {
        owned.signal.removeEventListener("abort", cancelRead);
        cancelRead();
        reader.releaseLock();
      }
    };
    try {
      return await Promise.race([send(), cancelled]);
    } catch (error) {
      // Provider/body failures may contain credential-bearing URLs or content.
      // Only finite local errors leave this boundary.
      const allowed = [
        "guest_trial_cancelled", "guest_trial_session_changed", "guest_trial_authentication_failed",
        "guest_trial_conflict", "guest_trial_expired", "guest_trial_unavailable", "guest_trial_invalid",
      ];
      throw new Error(allowed.includes(error?.message) ? error.message : "guest_trial_unavailable");
    } finally {
      clearTimeout(timer);
      signal.removeEventListener("abort", cancel);
      owned.signal.removeEventListener("abort", rejectAbort);
    }
  };
}

export function validateGuestTrial(value, now = Date.now()) {
  const keys = value && Object.hasOwn(value, "state") ? [...KEYS, "state"] : KEYS;
  if (!value || typeof value !== "object" || Array.isArray(value) ||
      Object.keys(value).length !== keys.length || keys.some(key => !Object.hasOwn(value, key)) ||
      value.version !== 1 || typeof value.id !== "string" || !/^[a-f0-9]{64}$/u.test(value.id) ||
      value.questionId !== QUESTION_ID || value.question !== QUESTION ||
      typeof value.assisted !== "boolean" ||
      !["pending", "stopped", "answer_first", "unverified"].includes(value.status) ||
      typeof value.expiresAt !== "string") invalid();
  const expected = 1 + Number(value.assisted) + Number(value.status !== "pending");
  const expires = Date.parse(value.expiresAt);
  if (value.generation !== expected || !Number.isFinite(now) || !Number.isFinite(expires) ||
      expires <= now || expires > now + 16 * 60_000) invalid();
  if (keys.includes("state") && (value.status !== "pending" || typeof value.state !== "string" ||
      value.state.length > 16384 || !/^v2\.[A-Za-z0-9_-]+$/u.test(value.state))) invalid();
  return Object.freeze(Object.fromEntries(keys.map(key => [key, value[key]])));
}

// send receives a finite control payload and an AbortSignal. The composition
// root supplies authenticated transport. discardPrevious MUST stop capture and
// destroy comparison audio before the new trial request is sent.
export function createGuestTrialController({ send, discardPrevious, now = Date.now, timeoutMs = 5000 }) {
  if (typeof send !== "function" || typeof discardPrevious !== "function" ||
      typeof now !== "function" || !Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 10000) invalid();
  let epoch = 0;
  let current = null;
  let phase = "idle";
  let active = null;
  let disposalTail = Promise.resolve();

  function state() { return Object.freeze({ phase, trial: current }); }
  function reset() {
    epoch++;
    active?.abort();
    active = null;
    current = null;
    phase = "idle";
  }

  async function operation(action) {
    if (active) throw new Error("guest_trial_busy");
    if (action === "begin") {
      if (current || phase !== "idle") throw new Error("guest_trial_reset_required");
    } else if (!current || (phase === "uncertain" && action !== "read") ||
               (action !== "read" && current.status !== "pending") ||
               (action === "help" && current.assisted)) {
      throw new Error("guest_trial_reconcile_required");
    }
    const previous = current;
    const ownedEpoch = epoch;
    const controller = new AbortController();
    active = controller;
    phase = "busy";
    let timer;
    let abortListener;
    try {
      const cancelled = new Promise((_, reject) => {
        abortListener = () => reject(new Error("guest_trial_cancelled"));
        controller.signal.addEventListener("abort", abortListener, { once: true });
        timer = setTimeout(() => controller.abort(), timeoutMs);
      });
      const request = (async () => {
        if (action === "begin") {
          // A non-cooperative old cleanup must finish before a new trial can
          // start; otherwise it could erase the new trial's capture later.
          const disposal = disposalTail.catch(() => {}).then(() => {
            if (controller.signal.aborted || ownedEpoch !== epoch) throw new Error("guest_trial_cancelled");
            return discardPrevious({ signal: controller.signal });
          });
          disposalTail = disposal;
          await disposal;
        }
        if (controller.signal.aborted || ownedEpoch !== epoch) throw new Error("guest_trial_cancelled");
        const payload = action === "begin" ? { action } : action === "read"
          ? { action, id: previous.id }
          : { action, id: previous.id, generation: previous.generation };
        return send(Object.freeze(payload), { signal: controller.signal });
      })();
      const raw = await Promise.race([request, cancelled]);
      if (controller.signal.aborted || ownedEpoch !== epoch) throw new Error("guest_trial_cancelled");
      const next = validateGuestTrial(raw, now());
      if (previous && (next.id !== previous.id || next.expiresAt !== previous.expiresAt ||
          next.generation < previous.generation || (previous.assisted && !next.assisted))) invalid();
      if (action === "begin" && (next.generation !== 1 || next.status !== "pending" || next.assisted)) invalid();
      if (action === "help" && (!next.assisted || next.status !== "pending" || next.generation !== previous.generation + 1)) invalid();
      if (action === "stop" && (next.status !== "stopped" || next.assisted !== previous.assisted || next.generation !== previous.generation + 1)) invalid();
      // A read may observe an independently committed result but may never
      // reopen or replace an already terminal result in this controller.
      if (previous && previous.status !== "pending" &&
          (next.status !== previous.status || next.generation !== previous.generation)) invalid();
      current = next;
      phase = next.status === "pending" ? "active" : "ended";
      return state();
    } catch (error) {
      if (ownedEpoch === epoch) phase = "uncertain";
      throw error;
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener("abort", abortListener);
      if (active === controller) active = null;
    }
  }

  return Object.freeze({ state, reset,
    begin: () => operation("begin"),
    read: () => operation("read"),
    help: () => operation("help"),
    stop: () => operation("stop"),
  });
}
