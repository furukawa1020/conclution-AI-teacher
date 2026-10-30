import {parseGuestTrialServerTiming} from "./guest-trial-timing.mjs";

// Mounted only when the authenticated availability endpoint permits it.
// Control state is memory-only and must never contain transcripts or audio.
const QUESTION_ID = "rest_activity_v1";
const QUESTION = "少し休めるとしたら、何をしたいですか？";
const KEYS = ["version", "id", "questionId", "question", "generation", "assisted", "status", "expiresAt"];
const CONTROL_PATH = "/api/v1/guest-trials/control";
const VOICE_PATH = "/api/v1/guest-trials/voice";
const MAX_CONTROL_RESPONSE_BYTES = 2048 + 16384;

// Dedicated explicit recording: no loudness threshold, VAD admission, Native
// transport or answer generation. The root owns the microphone and worklet.
export function createGuestTrialPcmCollector({port, generation, sampleRate, contextFrame = 0,
  maximumMs = 30000, sealTimeoutMs = 1000, onCancel = () => {}}) {
  if (!port || typeof port.postMessage !== "function" || typeof onCancel !== "function" ||
      !Number.isSafeInteger(generation) || generation < 1 ||
      !Number.isSafeInteger(sampleRate) || sampleRate < 16000 || sampleRate > 192000 || sampleRate % 50 ||
      !Number.isSafeInteger(contextFrame) || contextFrame < 0 ||
      !Number.isInteger(maximumMs) || maximumMs < 1 || maximumMs > 30000 ||
      !Number.isInteger(sealTimeoutMs) || sealTimeoutMs < 1 || sealTimeoutMs > 1000) invalid();
  const frames = [];
  let phase = "recording", previousFrame, sealTimer, finishPromise, resolveFinish, rejectFinish;
  const wipe = value => { if (value instanceof ArrayBuffer) new Uint8Array(value).fill(0); };
  const send = (type, fields = {}) => port.postMessage({type, version: 1, generation, ...fields});
  const clearFrames = () => { for (const frame of frames) frame.fill(0); frames.length = 0; };
  const deadline = setTimeout(() => cancel(), maximumMs);
  function cancel() {
    if (phase === "closed") return;
    phase = "closed";
    clearTimeout(deadline); clearTimeout(sealTimer); clearFrames();
    try { send("stop"); } catch { /* local erasure still occurs */ }
    rejectFinish?.(new Error("guest_trial_capture_unavailable"));
    // Notify only after erasure and rejection; reentrant disposal is harmless.
    try { onCancel(); } catch { /* observer failure cannot retain audio */ }
  }
  port.onmessage = ({data: message}) => {
    const pcm = message?.pcm;
    if (phase === "closed") { wipe(pcm); return; }
    if (message?.version !== 1 || message?.generation !== generation) {
      wipe(pcm); cancel(); return;
    }
    if (message.type === "frame") {
      if (!(pcm instanceof ArrayBuffer) || pcm.byteLength !== 640 || frames.length >= 1500 ||
          message.sequence !== frames.length || !Number.isSafeInteger(message.contextFrame) ||
          message.contextFrame < contextFrame ||
          (previousFrame !== undefined && message.contextFrame !== previousFrame + sampleRate / 50)) {
        wipe(pcm); cancel(); return;
      }
      previousFrame = message.contextFrame;
      frames.push(new Uint8Array(pcm));
      try { send("credit", {frames: 1}); } catch { cancel(); }
      return;
    }
    if (message.type !== "sealed" || phase !== "sealing" || frames.length === 0 ||
        message.lastSequence !== frames.length - 1) { wipe(pcm); cancel(); return; }
    const audio = new Uint8Array(frames.length * 640);
    frames.forEach((frame, i) => audio.set(frame, i * 640));
    clearFrames(); clearTimeout(deadline); clearTimeout(sealTimer);
    phase = "closed";
    resolveFinish(audio);
  };
  try {
    send("confirm", {candidateContextFrame: contextFrame, leadInFrames: 0,
      initialCredit: 1, aecVerified: false, quietConfirmed: false});
  } catch { cancel(); throw new Error("guest_trial_capture_unavailable"); }
  return Object.freeze({
    cancel,
    state: () => phase,
    finish() {
      if (finishPromise) return finishPromise;
      if (phase !== "recording") return Promise.reject(new Error("guest_trial_capture_unavailable"));
      phase = "sealing";
      finishPromise = new Promise((resolve, reject) => { resolveFinish = resolve; rejectFinish = reject; });
      sealTimer = setTimeout(cancel, sealTimeoutMs);
      try { send("seal"); } catch { cancel(); }
      return finishPromise;
    },
  });
}

function invalid() { throw new Error("guest_trial_invalid"); }

// Microphone ownership is separate from normal dialogue. Dependencies are
// supplied by the browser root and can be exercised without recording people.
export async function openGuestTrialCapture({getUserMedia, createContext, loadWorklet, createNode,
  signal, generation, setupTimeoutMs = 8000, onInterrupted = () => {}}) {
  if ([getUserMedia, createContext, loadWorklet, createNode].some(fn => typeof fn !== "function") ||
      typeof onInterrupted !== "function" || !signal || typeof signal.addEventListener !== "function" ||
      !Number.isSafeInteger(generation) || generation < 1 ||
      !Number.isInteger(setupTimeoutMs) || setupTimeoutMs < 1 || setupTimeoutMs > 8000) invalid();
  let context, stream, source, node, collector, disposed = false, timer, captureTimer, rejectSetup;
  function stopTracks(value) {
    for (const track of value?.getTracks() ?? []) {
      try { track.removeEventListener?.("ended", interrupt); } catch {}
      try { track.stop(); } catch { /* one failed device cannot retain others */ }
    }
  }
  function interrupt() {
    if (disposed) return;
    dispose();
    try { onInterrupted(); } catch { /* cleanup has already completed */ }
  }
  function contextChanged() {
    if (context?.state !== undefined && context.state !== "running") interrupt();
  }
  function dispose() {
    if (disposed) return;
    disposed = true;
    clearTimeout(timer); clearTimeout(captureTimer);
    signal.removeEventListener("abort", dispose);
    context?.removeEventListener?.("statechange", contextChanged);
    collector?.cancel();
    try { source?.disconnect(); } catch {}
    try { node?.disconnect(); } catch {}
    if (node) node.onprocessorerror = null;
    stopTracks(stream);
    try { void context?.close().catch(() => {}); } catch {}
    rejectSetup?.(new Error("guest_trial_capture_unavailable"));
  }
  signal.addEventListener("abort", dispose, {once: true});
  try {
    if (signal.aborted) { dispose(); throw new Error("guest_trial_capture_unavailable"); }
    context = createContext();
    const cancelled = new Promise((_, reject) => { rejectSetup = reject; });
    timer = setTimeout(dispose, setupTimeoutMs);
    const setup = (async () => {
      const media = Promise.resolve().then(() => getUserMedia({audio: {
        channelCount: 1, echoCancellation: true, noiseSuppression: false, autoGainControl: true,
      }})).then(value => {
        if (disposed) { stopTracks(value); throw new Error("guest_trial_capture_unavailable"); }
        stream = value;
      });
      const [, module] = await Promise.all([media,
        Promise.resolve().then(() => loadWorklet(context)), Promise.resolve().then(() => context.resume())]);
      if (disposed || signal.aborted) throw new Error("guest_trial_capture_unavailable");
      if ((context.state !== undefined && context.state !== "running") ||
          stream.getTracks().some(track => track.readyState === "ended")) {
        throw new Error("guest_trial_capture_unavailable");
      }
      context.addEventListener?.("statechange", contextChanged);
      node = createNode(context, {channelCount: 1, channelCountMode: "explicit", numberOfInputs: 1,
        numberOfOutputs: 0, processorOptions: {generation, maximumPreConfirmFrames: 1,
          maximumQueuedFrames: 200, pcmRingModule: module}});
      collector = createGuestTrialPcmCollector({port: node.port, generation,
        sampleRate: context.sampleRate, onCancel: interrupt});
      if (disposed) throw new Error("guest_trial_capture_unavailable");
      node.onprocessorerror = interrupt;
      for (const track of stream.getTracks()) track.addEventListener?.("ended", interrupt, {once: true});
      source = context.createMediaStreamSource(stream);
      source.connect(node);
    })();
    await Promise.race([setup, cancelled]);
    clearTimeout(timer);
    captureTimer = setTimeout(interrupt, 30000);
    return Object.freeze({cancel: dispose, async finish() {
      if (disposed) throw new Error("guest_trial_capture_unavailable");
      let audio;
      try {
        audio = await collector.finish();
        if (disposed || signal.aborted) throw new Error("guest_trial_capture_unavailable");
        return audio;
      } catch (error) { audio?.fill(0); throw error; }
      finally { dispose(); }
    }});
  } catch (error) { dispose(); throw error; }
}

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
    let pcm16;
    if (typeof audio.toBase64 === "function") {
      pcm16 = audio.toBase64();
    } else {
      const chunks = [];
      for (let offset = 0; offset < audio.length; offset += 8192) {
        chunks.push(String.fromCharCode(...audio.subarray(offset, offset + 8192)));
      }
      pcm16 = btoa(chunks.join(""));
    }
    return { trial, body: JSON.stringify({id: trial.id, generation: trial.generation,
      state: trial.state, pcm16}) };
  } finally {
    if (audio instanceof Uint8Array) audio.fill(0);
  }
}

function createTrialTransport({
  origin, credentials, isCurrentGuest, getSessionEpoch, request = (...args) => fetch(...args),
  now = Date.now, timeoutMs,
  clock = () => performance.now(), onTiming = () => {},
}, voice) {
  timeoutMs ??= voice ? 15000 : 5000;
  let url;
  try { url = new URL(origin); } catch { invalid(); }
  if (url.protocol !== "https:" || url.origin !== origin ||
      typeof credentials !== "function" || typeof isCurrentGuest !== "function" ||
      typeof getSessionEpoch !== "function" ||
      typeof request !== "function" || typeof now !== "function" ||
      typeof clock !== "function" || typeof onTiming !== "function" ||
      !Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > (voice ? 15000 : 10000)) invalid();
  const endpoint = `${origin}${voice ? VOICE_PATH : CONTROL_PATH}`;

  return async (payload, { signal } = {}) => {
    let encoded, sessionEpoch, started;
    try {
      started = clock();
      if (!Number.isFinite(started)) invalid();
      if (!signal || typeof signal.addEventListener !== "function") invalid();
      if (signal.aborted) throw new Error("guest_trial_cancelled");
      if (isCurrentGuest() !== true) throw new Error("guest_trial_session_changed");
      sessionEpoch = getSessionEpoch();
      if (!Number.isSafeInteger(sessionEpoch) || sessionEpoch < 0) invalid();
      encoded = voice ? voicePayload(payload, now()) : {body: controlPayload(payload)};
    } catch (error) {
      if (voice && payload?.audio instanceof Uint8Array) payload.audio.fill(0);
      const codes = ["guest_trial_invalid", "guest_trial_cancelled", "guest_trial_session_changed"];
      throw new Error(codes.includes(error?.message) ? error.message : "guest_trial_unavailable");
    }
    const body = encoded.body;
    // The root increments this on sign-out, account replacement and guest
    // replacement, not ordinary token refresh. It never crosses the network.
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
      if (getSessionEpoch() !== sessionEpoch) throw new Error("guest_trial_session_changed");
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
        const code = voice && response?.status === 422 && response.url === endpoint && !response.redirected
          ? "guest_trial_not_recognized" : response?.status === 401 || response?.status === 403
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
        if (voice) {
          // Observability cannot fail the operation or disclose response content.
          try {
            check();
            const elapsed = clock() - started;
            if (Number.isFinite(elapsed) && elapsed >= 0 && elapsed <= 60000) {
              onTiming(Object.freeze({roundtripMs: Math.floor(elapsed),
                server: parseGuestTrialServerTiming(response.headers.get("Server-Timing"))}));
            }
          } catch { /* ignore diagnostic failures */ }
        }
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
        "guest_trial_not_recognized",
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
export function createGuestTrialController({ send, sendVoice, discardPrevious, now = Date.now,
  timeoutMs = 5000, voiceTimeoutMs = 15000 }) {
  if (typeof send !== "function" || typeof discardPrevious !== "function" ||
      (sendVoice !== undefined && typeof sendVoice !== "function") ||
      typeof now !== "function" || !Number.isInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 10000 ||
      !Number.isInteger(voiceTimeoutMs) || voiceTimeoutMs < 1 || voiceTimeoutMs > 15000) invalid();
  let epoch = 0;
  let current = null;
  let phase = "idle";
  let active = null;
  let activeKind = null;
  let disposalTail = Promise.resolve();

  function state() { return Object.freeze({ phase, trial: current }); }
  function reset() {
    epoch++;
    active?.abort();
    active = null;
    activeKind = null;
    current = null;
    phase = "idle";
  }

  async function operation(action, audio) {
    if (action === "stop" && activeKind === "submit") {
      epoch++;
      active?.abort();
      active = null;
      activeKind = null;
      phase = "active";
    }
    if (active) throw new Error("guest_trial_busy");
    if (action === "submit" && typeof sendVoice !== "function") invalid();
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
    activeKind = action;
    phase = "busy";
    let timer;
    let abortListener;
    try {
      const cancelled = new Promise((_, reject) => {
        abortListener = () => reject(new Error("guest_trial_cancelled"));
        controller.signal.addEventListener("abort", abortListener, { once: true });
        timer = setTimeout(() => controller.abort(), action === "submit" ? voiceTimeoutMs : timeoutMs);
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
        if (action === "submit") {
          return sendVoice(Object.freeze({trial:previous, audio}), {signal:controller.signal});
        }
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
      if (action === "submit" && (!["answer_first", "unverified"].includes(next.status) ||
          next.assisted !== previous.assisted || next.generation !== previous.generation + 1)) invalid();
      // A read may observe an independently committed result but may never
      // reopen or replace an already terminal result in this controller.
      if (previous && previous.status !== "pending" &&
          (next.status !== previous.status || next.generation !== previous.generation)) invalid();
      current = next;
      phase = next.status === "pending" ? "active" : "ended";
      return state();
    } catch (error) {
      if (ownedEpoch === epoch) {
        // Only this explicit server rejection guarantees no verdict was committed.
        phase = action === "submit" && !controller.signal.aborted &&
          error?.message === "guest_trial_not_recognized" ? "active" : "uncertain";
      }
      throw error;
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener("abort", abortListener);
      if (active === controller) { active = null; activeKind = null; }
    }
  }

  return Object.freeze({ state, reset,
    begin: () => operation("begin"),
    read: () => operation("read"),
    help: () => operation("help"),
    stop: () => operation("stop"),
    submit: async audio => {
      try { return await operation("submit", audio); }
      finally { if (audio instanceof Uint8Array) audio.fill(0); }
    },
  });
}
