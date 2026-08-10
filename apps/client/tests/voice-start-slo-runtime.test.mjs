import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  advanceVoiceStartSlo,
  beginVoiceStartSlo,
  cancelVoiceStartSlo,
  classifyVoiceStartSloLatency,
  createVoiceStartSloState,
  updateVoiceStartSloRoute,
  VOICE_START_SLO_ACTIONS,
  VOICE_START_SLO_BUDGETS,
} from "../web/voice-start-slo-policy.mjs";

const bridgeSource = (
  await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8")
).replace(/\r\n/gu, "\n");
const controllerStart = bridgeSource.indexOf(
  "function nextVoiceStartSloGeneration()",
);
const controllerEnd = bridgeSource.indexOf(
  "\nfunction isPlainRecord(value)",
  controllerStart,
);

assert.notEqual(controllerStart, -1, "voice-start SLO controller start moved");
assert.notEqual(controllerEnd, -1, "voice-start SLO controller end moved");

const controllerSource = bridgeSource.slice(controllerStart, controllerEnd);

// Execute the production controller helpers in a small dependency boundary. The
// policy itself is imported above; neither half is copied into this harness.
const createController = new Function(
  "dependencies",
  `
    "use strict";
    const {
      CustomEvent,
      VOICE_START_SLO_ACTIONS,
      VOICE_START_SLO_BUDGETS,
      advanceVoiceStartSlo,
      beginVoiceStartSlo,
      boundedLatency,
      cancelVoiceStartSlo,
      classifyVoiceStartSloLatency,
      clearTimeout,
      createVoiceStartSloState,
      dispatchVoiceStartLatency,
      eventTarget,
      fail,
      performance,
      setTimeout,
      updateVoiceStartSloRoute,
    } = dependencies;
    const globalThis = eventTarget;
    let voiceStartSloGeneration = 0;
    let voiceStartSloDeferredMissTimer;
    let voiceStartSloHandlers;
    let voiceStartSloMissTimer;
    let voiceStartSloStallTimer;
    let voiceStartSloState = createVoiceStartSloState();

    ${controllerSource}

    return Object.freeze({
      advanceCurrentVoiceStartSlo,
      beginCurrentVoiceStartSlo,
      cancelCurrentVoiceStartSlo,
      inspect() {
        const timer = (value) => value === undefined
          ? null
          : Object.freeze({ generation: value.generation, id: value.id });
        return Object.freeze({
          handlerGeneration: voiceStartSloHandlers?.generation ?? null,
          state: voiceStartSloState,
          timers: Object.freeze({
            deferredMiss: timer(voiceStartSloDeferredMissTimer),
            miss: timer(voiceStartSloMissTimer),
            stall: timer(voiceStartSloStallTimer),
          }),
        });
      },
      nextVoiceStartSloGeneration,
      updateCurrentVoiceStartSloRoute,
    });
  `,
);

class FakeClock {
  constructor(now = 0) {
    this.currentTime = now;
    this.nextTimerId = 1;
    this.queued = [];
    this.scheduled = new Map();
  }

  get now() {
    return this.currentTime;
  }

  clearTimeout(id) {
    // A browser cannot retract a task that has already left the timer queue.
    // Keeping `queued` intact lets the tests exercise that stale callback race.
    this.scheduled.delete(id);
  }

  setTimeout(callback, delay = 0) {
    assert.equal(typeof callback, "function");
    const finiteDelay = Number.isFinite(delay) ? Math.max(0, delay) : 0;
    const id = this.nextTimerId;
    this.nextTimerId += 1;
    this.scheduled.set(id, {
      callback,
      dueAt: this.currentTime + finiteDelay,
      id,
    });
    return id;
  }

  nextScheduledAtOrBefore(target) {
    let selected = null;
    for (const timer of this.scheduled.values()) {
      if (timer.dueAt > target) continue;
      if (
        selected === null ||
        timer.dueAt < selected.dueAt ||
        (timer.dueAt === selected.dueAt && timer.id < selected.id)
      ) {
        selected = timer;
      }
    }
    return selected;
  }

  advanceTo(target) {
    assert.equal(Number.isFinite(target), true);
    assert.equal(target >= this.currentTime, true);
    while (this.queued.length > 0) {
      this.queued.shift().callback();
    }
    for (;;) {
      const timer = this.nextScheduledAtOrBefore(target);
      if (timer === null) break;
      this.scheduled.delete(timer.id);
      this.currentTime = Math.max(this.currentTime, timer.dueAt);
      timer.callback();
      while (this.queued.length > 0) {
        this.queued.shift().callback();
      }
    }
    this.currentTime = target;
  }

  queueThrough(target) {
    assert.equal(Number.isFinite(target), true);
    assert.equal(target >= this.currentTime, true);
    this.currentTime = target;
    for (;;) {
      const timer = this.nextScheduledAtOrBefore(target);
      if (timer === null) break;
      this.scheduled.delete(timer.id);
      this.queued.push(timer);
    }
  }

  flushQueued() {
    while (this.queued.length > 0) {
      this.queued.shift().callback();
    }
  }

  snapshot() {
    return {
      queued: this.queued.map(({ dueAt, id }) => ({ dueAt, id })),
      scheduled: [...this.scheduled.values()]
        .sort((left, right) => left.dueAt - right.dueAt || left.id - right.id)
        .map(({ dueAt, id }) => ({ dueAt, id })),
    };
  }
}

class FakeCustomEvent {
  constructor(type, options = {}) {
    this.detail = options.detail;
    this.type = type;
  }
}

class FakeEventTarget {
  constructor() {
    this.events = [];
    this.listeners = new Map();
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  dispatchEvent(event) {
    this.events.push(event);
    for (const listener of [...(this.listeners.get(event.type) ?? [])]) {
      listener(event);
    }
    return true;
  }

  eventsOf(type) {
    return this.events.filter((event) => event.type === type);
  }
}

function boundedLatency(value) {
  if (!Number.isFinite(value) || value < 0) return 0;
  return Math.min(120_000, Math.round(value * 10) / 10);
}

function harness(now = 0) {
  const clock = new FakeClock(now);
  const eventTarget = new FakeEventTarget();
  const voiceStartLatencies = [];
  const controller = createController({
    CustomEvent: FakeCustomEvent,
    VOICE_START_SLO_ACTIONS,
    VOICE_START_SLO_BUDGETS,
    advanceVoiceStartSlo,
    beginVoiceStartSlo,
    boundedLatency,
    cancelVoiceStartSlo,
    classifyVoiceStartSloLatency,
    clearTimeout: (id) => clock.clearTimeout(id),
    createVoiceStartSloState,
    dispatchVoiceStartLatency: (latency) => voiceStartLatencies.push(latency),
    eventTarget,
    fail(code) {
      throw new Error(code);
    },
    performance: Object.freeze({ now: () => clock.now }),
    setTimeout: (callback, delay) => clock.setTimeout(callback, delay),
    updateVoiceStartSloRoute,
  });
  return { clock, controller, eventTarget, voiceStartLatencies };
}

function beginController(controller, overrides = {}) {
  const generation =
    overrides.generation ?? controller.nextVoiceStartSloGeneration();
  controller.beginCurrentVoiceStartSlo({
    generation,
    onMiss: overrides.onMiss,
    onStall: overrides.onStall,
    operationalStartedAt: overrides.operationalStartedAt ?? 0,
    route: overrides.route ?? "native-conversation",
    startedAt: overrides.startedAt ?? 0,
  });
  return generation;
}

test("harness evaluates the production controller block and imported policy", () => {
  assert.match(controllerSource, /function clearVoiceStartSloTimers\(/u);
  assert.match(controllerSource, /operationalStartedAt/u);
  assert.match(controllerSource, /missNotBefore/u);
  assert.match(controllerSource, /generation/u);
  assert.equal(controllerSource.includes("function isPlainRecord"), false);
});

test("speech-end +3s miss is observable but Native miss waits for commit +3s", () => {
  const { clock, controller, eventTarget, voiceStartLatencies } = harness(10_000);
  let misses = 0;
  let stalls = 0;
  const generation = beginController(controller, {
    onMiss: () => {
      misses += 1;
    },
    onStall: () => {
      stalls += 1;
    },
    operationalStartedAt: 10_000,
    startedAt: 5_000,
  });

  clock.advanceTo(10_000);

  assert.equal(misses, 0);
  assert.deepEqual(
    eventTarget.eventsOf("kotae:voice-start-slo-milestone").map((event) =>
      event.detail.milestone
    ),
    ["three-second-miss"],
  );
  assert.equal(controller.inspect().timers.deferredMiss?.generation, generation);
  assert.deepEqual(clock.snapshot().scheduled.map(({ dueAt }) => dueAt), [
    13_000,
    15_000,
  ]);

  clock.advanceTo(12_999);
  controller.advanceCurrentVoiceStartSlo(generation, true, clock.now);
  clock.advanceTo(20_000);

  assert.equal(misses, 0);
  assert.equal(stalls, 0);
  assert.deepEqual(clock.snapshot(), { queued: [], scheduled: [] });
  assert.deepEqual(voiceStartLatencies, [7_999]);
  assert.deepEqual(
    eventTarget.eventsOf("kotae:voice-start-slo").map((event) => event.detail),
    [
      {
        generation,
        latency_ms: 7_999,
        outcome: "missed",
        route: "native-conversation",
        version: 1,
      },
    ],
  );
});

test("3s and 10s milestones and their handlers fire exactly once", () => {
  const startedAt = 20_000;
  const { clock, controller, eventTarget } = harness(startedAt);
  let misses = 0;
  let stalls = 0;
  const generation = beginController(controller, {
    onMiss: () => {
      misses += 1;
    },
    onStall: () => {
      stalls += 1;
    },
    operationalStartedAt: startedAt,
    startedAt,
  });

  clock.advanceTo(startedAt + 3_000);
  assert.equal(misses, 1);
  assert.equal(stalls, 0);

  controller.advanceCurrentVoiceStartSlo(generation, false, clock.now);
  clock.advanceTo(startedAt + 10_000);
  assert.equal(misses, 1);
  assert.equal(stalls, 1);

  controller.advanceCurrentVoiceStartSlo(generation, false, clock.now);
  clock.advanceTo(startedAt + 30_000);
  assert.equal(misses, 1);
  assert.equal(stalls, 1);
  assert.deepEqual(
    eventTarget.eventsOf("kotae:voice-start-slo-milestone").map((event) =>
      event.detail.milestone
    ),
    ["three-second-miss", "ten-second-stall"],
  );
});

test("a callback already queued before cancel is harmless", () => {
  const { clock, controller, eventTarget } = harness();
  let misses = 0;
  let stalls = 0;
  const generation = beginController(controller, {
    onMiss: () => {
      misses += 1;
    },
    onStall: () => {
      stalls += 1;
    },
  });

  clock.queueThrough(3_000);
  assert.equal(clock.snapshot().queued.length, 1);
  controller.cancelCurrentVoiceStartSlo(generation);
  clock.flushQueued();
  clock.advanceTo(20_000);

  assert.equal(misses, 0);
  assert.equal(stalls, 0);
  assert.equal(controller.inspect().state.active, false);
  assert.deepEqual(clock.snapshot(), { queued: [], scheduled: [] });
  assert.deepEqual(eventTarget.events, []);
});

test("CustomEvent re-entry cannot let an old generation erase replacement timers", () => {
  const { clock, controller, eventTarget } = harness();
  let oldMisses = 0;
  let replacementGeneration;
  let replacementMisses = 0;
  const oldGeneration = beginController(controller, {
    onMiss: () => {
      oldMisses += 1;
    },
  });

  eventTarget.addEventListener(
    "kotae:voice-start-slo-milestone",
    (event) => {
      if (
        event.detail.generation !== oldGeneration ||
        event.detail.milestone !== "three-second-miss" ||
        replacementGeneration !== undefined
      ) {
        return;
      }
      replacementGeneration = controller.nextVoiceStartSloGeneration();
      beginController(controller, {
        generation: replacementGeneration,
        onMiss: () => {
          replacementMisses += 1;
        },
        operationalStartedAt: clock.now,
        startedAt: clock.now,
      });
    },
  );

  // Put the old 3s timer on the task queue, then let meaningful audio win the
  // race. Its miss milestone synchronously starts the replacement generation.
  clock.queueThrough(3_000);
  controller.advanceCurrentVoiceStartSlo(oldGeneration, true, clock.now);
  clock.flushQueued();

  assert.equal(oldMisses, 0);
  assert.equal(controller.inspect().state.generation, replacementGeneration);
  assert.equal(controller.inspect().handlerGeneration, replacementGeneration);
  assert.deepEqual(clock.snapshot().scheduled.map(({ dueAt }) => dueAt), [
    6_000,
    13_000,
  ]);

  clock.advanceTo(6_000);
  assert.equal(replacementMisses, 1);
  assert.equal(controller.inspect().state.generation, replacementGeneration);
  assert.equal(controller.inspect().timers.stall?.generation, replacementGeneration);
});

test("milestone-only re-entry transfers handler ownership before onMiss", () => {
  const { clock, controller, eventTarget } = harness();
  let oldMisses = 0;
  let replacementGeneration;
  let replacementMisses = 0;
  const oldGeneration = beginController(controller, {
    onMiss: () => {
      oldMisses += 1;
    },
  });

  eventTarget.addEventListener(
    "kotae:voice-start-slo-milestone",
    (event) => {
      if (
        event.detail.generation !== oldGeneration ||
        replacementGeneration !== undefined
      ) {
        return;
      }
      replacementGeneration = controller.nextVoiceStartSloGeneration();
      beginController(controller, {
        generation: replacementGeneration,
        onMiss: () => {
          replacementMisses += 1;
        },
        operationalStartedAt: clock.now,
        startedAt: clock.now,
      });
    },
  );

  clock.advanceTo(3_000);

  assert.equal(oldMisses, 0);
  assert.equal(controller.inspect().state.generation, replacementGeneration);
  assert.deepEqual(clock.snapshot().scheduled.map(({ dueAt }) => dueAt), [
    6_000,
    13_000,
  ]);

  clock.advanceTo(6_000);
  assert.equal(replacementMisses, 1);
});

test("a dynamic coach route relabels milestones and observation without resetting time", () => {
  const startedAt = 100;
  const { clock, controller, eventTarget } = harness(startedAt);
  const generation = beginController(controller, {
    operationalStartedAt: startedAt,
    startedAt,
  });

  clock.advanceTo(500);
  controller.updateCurrentVoiceStartSloRoute(
    generation,
    "initial-answer-support",
  );
  assert.equal(controller.inspect().state.startedAt, startedAt);

  clock.advanceTo(startedAt + 3_000);
  clock.advanceTo(startedAt + 3_500);
  controller.advanceCurrentVoiceStartSlo(generation, true, clock.now);

  assert.deepEqual(
    eventTarget.eventsOf("kotae:voice-start-slo-milestone").map((event) =>
      event.detail.route
    ),
    ["initial-answer-support"],
  );
  assert.deepEqual(
    eventTarget.eventsOf("kotae:voice-start-slo").map((event) => event.detail),
    [
      {
        generation,
        latency_ms: 3_500,
        outcome: "missed",
        route: "initial-answer-support",
        version: 1,
      },
    ],
  );
});

test("SLO events have exact content-free keys and frozen details", () => {
  const { clock, controller, eventTarget } = harness();
  const generation = beginController(controller);
  clock.advanceTo(3_000);
  clock.advanceTo(3_500);
  controller.advanceCurrentVoiceStartSlo(generation, true, clock.now);

  const [milestone] = eventTarget.eventsOf(
    "kotae:voice-start-slo-milestone",
  );
  const [observation] = eventTarget.eventsOf("kotae:voice-start-slo");

  assert.deepEqual(Object.keys(milestone.detail).sort(), [
    "generation",
    "milestone",
    "route",
    "version",
  ]);
  assert.deepEqual(Object.keys(observation.detail).sort(), [
    "generation",
    "latency_ms",
    "outcome",
    "route",
    "version",
  ]);
  assert.deepEqual(milestone.detail, {
    generation,
    milestone: "three-second-miss",
    route: "native-conversation",
    version: 1,
  });
  assert.deepEqual(observation.detail, {
    generation,
    latency_ms: 3_500,
    outcome: "missed",
    route: "native-conversation",
    version: 1,
  });
  assert.equal(Object.isFrozen(milestone.detail), true);
  assert.equal(Object.isFrozen(observation.detail), true);

  const forbiddenContentKeys = new Set([
    "audio",
    "caption",
    "content",
    "credential",
    "prompt",
    "session",
    "text",
    "token",
    "transcript",
  ]);
  for (const event of [milestone, observation]) {
    assert.equal(
      Object.keys(event.detail).some((key) => forbiddenContentKeys.has(key)),
      false,
    );
  }
});

test("wire outcomes are canonical at rounded latency boundaries", () => {
  const { controller, eventTarget } = harness();
  const samples = [
    [1_000.04, 1_000, "on-target"],
    [2_999.96, 3_000, "missed"],
    [9_999.96, 10_000, "stalled"],
  ];
  for (const [now, latencyMs, outcome] of samples) {
    const generation = beginController(controller);
    controller.advanceCurrentVoiceStartSlo(generation, true, now);
    const detail = eventTarget.eventsOf("kotae:voice-start-slo").at(-1).detail;
    assert.equal(detail.latency_ms, latencyMs);
    assert.equal(detail.outcome, outcome);
  }
});
