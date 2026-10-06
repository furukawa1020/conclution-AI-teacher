import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  createVoiceLiveClientTransport,
  createVoiceLiveServerProtocol,
  VOICE_LIVE_LIMITS,
} from "../web/voice-stream-policy.mjs";
import {
  createNativePreflightInvalidationGate,
  NATIVE_PREFLIGHT_RETIRE_REASONS,
} from "../web/native-preflight-lease-policy.mjs";

const bridge = await readFile(
  new URL("../web/firebase-bridge.js", import.meta.url),
  "utf8",
);
const start = bridge.indexOf("async function startVoiceLiveSession(");
const end = bridge.indexOf("function isNdjsonContentType(", start);
assert.ok(start >= 0 && end > start);
const liveSource = bridge.slice(start, end);
const flushTasks = () => new Promise((resolve) => setImmediate(resolve));

// Execute the complete production listener handoff with the real transport
// and protocol. Only browser devices, module availability, and time are fake.
// Function keeps plain records in the protocol's realm, unlike a VM context.
function createHarness({ nativeAudio = true, captureHandoff = false } = {}) {
  let now = 100;
  let socket;
  let releaseModule;
  let connected = false;
  let adoptions = 0;
  let handoffStops = 0;
  let moduleLoads = 0;
  let resolved = false;
  let nextTimer = 0;
  const timers = new Map();
  const sent = [];
  const closes = [];
  const observations = [];
  const failures = [];
  const moduleReady = new Promise((resolve) => { releaseModule = resolve; });
  const invalidation = createNativePreflightInvalidationGate();

  class FixtureSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSED = 3;
    readyState = 0;
    bufferedAmount = 0;
    listeners = new Map();
    constructor() { socket = this; }
    addEventListener(type, fn, options) {
      const listeners = this.listeners.get(type) ?? [];
      listeners.push({ fn, once: options?.once });
      this.listeners.set(type, listeners);
    }
    removeEventListener(type, fn) {
      this.listeners.set(type,
        (this.listeners.get(type) ?? []).filter((entry) => entry.fn !== fn));
    }
    emit(type, event = {}) {
      for (const entry of [...(this.listeners.get(type) ?? [])]) {
        if (entry.once) this.removeEventListener(type, entry.fn);
        entry.fn(event);
      }
    }
    send(value) { sent.push(JSON.parse(value)); }
    close(code, reason) {
      closes.push({ code, reason });
      this.readyState = FixtureSocket.CLOSED;
    }
  }
  class FixtureWorklet {
    port = { postMessage() {} };
    addEventListener() {}
    disconnect() {}
  }
  const scope = {
    pendingDocument: undefined,
    liveVoiceSupported: () => true,
    liveCredential: () => true,
    SESSION_STATE_MAX_CHARS: 100_000,
    isValidTurnMode: () => true,
    performance: { now: () => now },
    sessionEpoch: 1,
    WebSocket: FixtureSocket,
    VOICE_LIVE_ENDPOINT: "wss://fixture.invalid/",
    createVoiceLiveClientTransport,
    createVoiceLiveServerProtocol,
    safeVoiceResponse: (value) => value,
    nativePreflightInvalidation: invalidation,
    NATIVE_PREFLIGHT_RETIRE_REASONS,
    VOICE_LIVE_LIMITS,
    loadPcmCaptureWorklet: () => { moduleLoads += 1; return moduleReady; },
    audioContext: {
      state: "running",
      createMediaStreamSource: () => ({
        connect() { connected = true; },
        disconnect() {},
      }),
    },
    setTimeout(fn, delay) {
      const id = ++nextTimer;
      timers.set(id, { at: now + delay, fn });
      return id;
    },
    clearTimeout: (id) => timers.delete(id),
    AudioWorkletNode: FixtureWorklet,
    nextPcmCaptureGeneration: () => 1,
    CONFIRMED_SPEECH_PCM_LIMITS: { maximumFrames: 10 },
    preparingLiveSession: undefined,
    guestModeActive: false,
    guestVoiceComparisonRequested: false,
    reportVoiceFailure: (phase, error) => failures.push([phase, error.message]),
    stoppedSessionCode: () => "request_cancelled",
    zeroizeCaptureFrame() {},
    dispatchNativePreflightLatency: (value) => observations.push(value),
    fail(code) { throw new Error(code); },
  };
  const runtime = Function(...Object.keys(scope), `${liveSource}
    return {
      startVoiceLiveSession,
      isPreparing: () => preparingLiveSession !== undefined,
      cancel() {
        nativePreflightInvalidation.retireCurrent(
          NATIVE_PREFLIGHT_RETIRE_REASONS.CANCELLED);
        sessionEpoch += 1;
        preparingLiveSession?.cancel(new Error("request_cancelled"));
      },
    };`)(...Object.values(scope));
  const pending = runtime.startVoiceLiveSession({
    appCheckToken: "a.b.c",
    idToken: "d.e.f",
    expectedEpoch: 1,
    nativeAudio,
    ...(captureHandoff ? {
      captureHandoff: {
        adopt() { adoptions += 1; },
        async seal() {},
        stop() { handoffStops += 1; },
      },
    } : {}),
    sessionState: "",
    stream: {},
    strictCloudMinimization: false,
    turnMode: "foreground",
  });
  void pending.then(() => { resolved = true; }, () => { resolved = true; });
  return {
    pending, sent, closes, observations, failures, invalidation,
    cancel: runtime.cancel,
    isPreparing: runtime.isPreparing,
    isConnected: () => connected,
    isAdopted: () => adoptions > 0,
    adoptionCount: () => adoptions,
    handoffStopCount: () => handoffStops,
    isResolved: () => resolved,
    moduleLoadCount: () => moduleLoads,
    timerCount: () => timers.size,
    loadModules: async () => { releaseModule({}); await flushTasks(); },
    open() { socket.readyState = FixtureSocket.OPEN; socket.emit("open"); },
    connectionFailure(type) {
      if (type === "close") socket.readyState = FixtureSocket.CLOSED;
      socket.emit(type, { code: 1006, reason: "", wasClean: false });
    },
    rawMessage(data) { socket.emit("message", { data }); },
    message(value) {
      socket.emit("message", {
        data: value instanceof ArrayBuffer ? value : JSON.stringify(value),
      });
    },
    preflightReady(overrides = {}) {
      this.message({
        type: "preflight-ready", version: 1, generation: 2,
        leaseId: `knl1_${"a".repeat(43)}`, expiresInMs: 10_000,
        ...overrides,
      });
    },
    strongReady() { this.message({ type: "ready", version: 1 }); },
    advance(milliseconds) {
      now += milliseconds;
      for (const [id, timer] of [...timers]) {
        if (timer.at <= now && timers.delete(id)) timer.fn();
      }
    },
    elapseWithoutTimers(milliseconds) { now += milliseconds; },
  };
}

function assertStrongReadyStillPending(harness) {
  assert.equal(harness.isResolved(), false);
  assert.equal(harness.isConnected(), false);
  assert.equal(harness.isAdopted(), false);
  assert.equal(harness.closes.length, 0);
  assert.equal(harness.observations.length, 0);
}

test("Native preflight survives every module/socket readiness ordering", async (t) => {
  for (const moduleStage of ["before-open", "before-preflight-ready", "before-strong-ready", "after-strong-ready"]) {
    await t.test(moduleStage, async () => {
      const harness = createHarness();
      if (moduleStage === "before-open") await harness.loadModules();
      harness.open();
      assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
      harness.advance(25);
      if (moduleStage === "before-preflight-ready") await harness.loadModules();
      assertStrongReadyStillPending(harness);
      harness.preflightReady();
      assert.deepEqual(harness.sent.map((value) => value.type), ["preflight", "activate"]);
      assert.equal(harness.sent[1].generation, 2);
      harness.advance(50);
      if (moduleStage === "before-strong-ready") await harness.loadModules();
      assertStrongReadyStillPending(harness);
      harness.strongReady();
      if (moduleStage === "after-strong-ready") await harness.loadModules();
      const session = await harness.pending;
      assert.equal(session.state(), "ready");
      assert.equal(session.isLiveReady(), true);
      assert.equal(harness.isConnected(), true);
      assert.equal(harness.isPreparing(), false);
      assert.equal(harness.invalidation.hasActive(), false);
      assert.deepEqual(harness.observations, [{ coldMs: 75, generation: 2, warmMs: 50 }]);
      assert.equal(harness.timerCount(), 0);
      session.cancel();
    });
  }
});

test("pending preflight keeps the original absolute ready deadline and bounded fallback", async () => {
  const harness = createHarness();
  harness.open();
  harness.advance(VOICE_LIVE_LIMITS.readyTimeoutMs - 100);
  await harness.loadModules();
  assertStrongReadyStillPending(harness);
  harness.advance(99);
  await flushTasks();
  assertStrongReadyStillPending(harness);
  harness.advance(1);
  const session = await harness.pending;
  assert.equal(session.state(), "fallback-ready");
  assert.equal(session.isLiveReady(), false);
  assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
  assert.deepEqual(harness.closes, [{ code: 1000, reason: "http_fallback" }]);
  assert.equal(harness.observations.length, 0);
  assert.equal(harness.timerCount(), 0);
  session.cancel();
});

test("module handoff preserves cancellation ownership without late activation", async (t) => {
  for (const moduleLoaded of [false, true]) {
    await t.test(moduleLoaded ? "complete session owner" : "preflight owner", async () => {
      const harness = createHarness();
      harness.open();
      if (moduleLoaded) await harness.loadModules();
      assertStrongReadyStillPending(harness);
      assert.equal(harness.invalidation.hasActive(), !moduleLoaded);
      assert.equal(harness.isPreparing(), moduleLoaded);
      harness.cancel();
      harness.preflightReady();
      harness.strongReady();
      if (moduleLoaded) {
        assert.equal(await harness.pending, undefined);
      } else {
        await harness.loadModules();
        await assert.rejects(harness.pending, /request_cancelled/u);
      }
      assert.equal(harness.isConnected(), false);
      assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
      assert.equal(harness.observations.length, 0);
      assert.equal(harness.invalidation.hasActive(), false);
      assert.equal(harness.isPreparing(), false);
      assert.equal(harness.timerCount(), 0);
    });
  }
});

test("handoff never accepts invalid preflight capabilities or early audio", async (t) => {
  for (const invalid of ["generation", "lease", "expiry", "surplus", "binary", "malformed", "strong-ready", "server-error"]) {
    await t.test(invalid, async () => {
      const harness = createHarness();
      harness.open();
      await harness.loadModules();
      assertStrongReadyStillPending(harness);
      if (invalid === "generation") harness.preflightReady({ generation: 3 });
      if (invalid === "lease") harness.preflightReady({ leaseId: "invalid" });
      if (invalid === "expiry") harness.preflightReady({ expiresInMs: 15_001 });
      if (invalid === "surplus") harness.preflightReady({ extra: true });
      if (invalid === "binary") harness.message(new ArrayBuffer(640));
      if (invalid === "malformed") harness.rawMessage("{");
      if (invalid === "strong-ready") harness.strongReady();
      if (invalid === "server-error") harness.message({ type: "error", version: 1, code: "voice_api_unavailable" });
      assert.equal(await harness.pending, undefined);
      assert.equal(harness.isConnected(), false);
      assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
      assert.equal(harness.observations.length, 0);
      assert.equal(harness.timerCount(), 0);
    });
  }
});

test("a duplicate preflight-ready never activates twice or reaches strong ready", async () => {
  const harness = createHarness();
  harness.open();
  await harness.loadModules();
  harness.preflightReady();
  harness.preflightReady();
  harness.strongReady();
  assert.equal(await harness.pending, undefined);
  assert.equal(harness.isConnected(), false);
  assert.deepEqual(harness.sent.map((value) => value.type), ["preflight", "activate"]);
  assert.equal(harness.observations.length, 0);
  assert.equal(harness.timerCount(), 0);
});

test("a duplicate preflight-ready after strong-ready retires the active capture", async () => {
  const harness = createHarness({ captureHandoff: true });
  harness.open();
  harness.preflightReady();
  harness.strongReady();
  const session = await harness.pending;
  assert.equal(session.state(), "ready");
  harness.preflightReady();
  assert.equal(session.state(), "failed");
  assert.equal(harness.adoptionCount(), 1);
  assert.equal(harness.handoffStopCount(), 1);
  assert.deepEqual(harness.sent.map((value) => value.type), ["preflight", "activate"]);
  assert.equal(harness.observations.length, 1);
  assert.equal(harness.timerCount(), 0);
});

test("pending preflight close and error select bounded HTTP fallback", async (t) => {
  for (const type of ["close", "error"]) {
    await t.test(type, async () => {
      const harness = createHarness();
      harness.open();
      await harness.loadModules();
      assertStrongReadyStillPending(harness);
      harness.connectionFailure(type);
      const session = await harness.pending;
      assert.equal(session.state(), "fallback-ready");
      assert.equal(session.isLiveReady(), false);
      harness.preflightReady();
      harness.strongReady();
      assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
      assert.equal(harness.observations.length, 0);
      assert.equal(harness.timerCount(), 0);
      session.cancel();
    });
  }
});

test("late strong-ready is rejected even before the delayed timer executes", async () => {
  const harness = createHarness();
  harness.open();
  await harness.loadModules();
  harness.preflightReady();
  harness.elapseWithoutTimers(VOICE_LIVE_LIMITS.readyTimeoutMs);
  harness.strongReady();
  assert.equal(await harness.pending, undefined);
  assert.equal(harness.isConnected(), false);
  assert.equal(harness.observations.length, 0);
  assert.equal(harness.timerCount(), 0);
});

test("an existing capture handoff is adopted only after Native strong-ready", async () => {
  const harness = createHarness({ captureHandoff: true });
  assert.equal(harness.moduleLoadCount(), 0);
  harness.open();
  assert.deepEqual(harness.sent.map((value) => value.type), ["preflight"]);
  harness.preflightReady();
  assertStrongReadyStillPending(harness);
  harness.strongReady();
  const session = await harness.pending;
  assert.equal(session.state(), "ready");
  assert.equal(harness.isAdopted(), true);
  assert.equal(harness.adoptionCount(), 1);
  assert.equal(harness.handoffStopCount(), 0);
  assert.equal(harness.isConnected(), false);
  assert.equal(harness.moduleLoadCount(), 0);
  assert.equal(harness.observations.length, 1);
  assert.equal(harness.timerCount(), 0);
  session.cancel();
  assert.equal(harness.handoffStopCount(), 1);
});

test("non-Native live startup retains its ordinary start and strong-ready gate", async () => {
  const harness = createHarness({ nativeAudio: false });
  await harness.loadModules();
  harness.open();
  assert.deepEqual(harness.sent.map((value) => value.type), ["start"]);
  assertStrongReadyStillPending(harness);
  harness.strongReady();
  const session = await harness.pending;
  assert.equal(session.state(), "ready");
  assert.equal(harness.isConnected(), true);
  assert.equal(harness.observations.length, 0);
  session.cancel();
});
