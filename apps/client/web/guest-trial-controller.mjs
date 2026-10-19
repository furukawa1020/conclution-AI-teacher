// Not mounted by the guest UI until the committed-voice route is connected.
// Control state is memory-only and must never contain transcripts or audio.
const QUESTION_ID = "rest_activity_v1";
const QUESTION = "少し休めるとしたら、何をしたいですか？";
const KEYS = ["version", "id", "questionId", "question", "generation", "assisted", "status", "expiresAt"];

function invalid() { throw new Error("guest_trial_invalid"); }

export function validateGuestTrial(value, now = Date.now()) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
      Object.keys(value).length !== KEYS.length || KEYS.some(key => !Object.hasOwn(value, key)) ||
      value.version !== 1 || typeof value.id !== "string" || !/^[a-f0-9]{64}$/u.test(value.id) ||
      value.questionId !== QUESTION_ID || value.question !== QUESTION ||
      typeof value.assisted !== "boolean" ||
      !["pending", "stopped", "answer_first", "unverified"].includes(value.status) ||
      typeof value.expiresAt !== "string") invalid();
  const expected = 1 + Number(value.assisted) + Number(value.status !== "pending");
  const expires = Date.parse(value.expiresAt);
  if (value.generation !== expected || !Number.isFinite(now) || !Number.isFinite(expires) ||
      expires <= now || expires > now + 16 * 60_000) invalid();
  return Object.freeze(Object.fromEntries(KEYS.map(key => [key, value[key]])));
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
