import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

import {
  BARGE_PCM_LIMITS,
  VOICE_LIVE_LIMITS,
} from "../web/voice-stream-policy.mjs";

const publishedWorkletSource = readFileSync(
  new URL("../web/pcm-capture-worklet.js", import.meta.url),
  "utf8",
).replaceAll("\r\n", "\n").replaceAll("\r", "\n");
const runtimeImport =
  'import { createPcmRing } from "./pcm-ring-worklet-runtime.js";';
assert.equal(publishedWorkletSource.startsWith(`${runtimeImport}\n`), true);
const workletSource = publishedWorkletSource.slice(runtimeImport.length + 1);
const TEST_PCM_RING_MODULE = Object.freeze({ test: true });

class TestPcmRing {
  constructor(generation, capacity, overwriteOldest) {
    if (!Number.isSafeInteger(generation) || generation <= 0) {
      throw new Error("invalid_generation");
    }
    if (!Number.isSafeInteger(capacity) || capacity <= 0 || capacity > 200) {
      throw new Error("invalid_capacity");
    }
    this.ownerGeneration = generation;
    this.slots = new Array(capacity).fill(null);
    this.head = 0;
    this.lastContextFrame = undefined;
    this.size = 0;
    this.overwriteOldest = overwriteOldest === true;
    this.freed = false;
  }

  capacity() {
    return this.slots.length;
  }

  generation() {
    return this.ownerGeneration;
  }

  clear(generation) {
    if (generation !== this.ownerGeneration) return false;
    this.wipeAll();
    return true;
  }

  wipeAll() {
    for (let index = 0; index < this.slots.length; index += 1) {
      const entry = this.slots[index];
      if (entry) new Uint8Array(entry.pcm).fill(0);
      this.slots[index] = null;
    }
    this.head = 0;
    this.lastContextFrame = undefined;
    this.size = 0;
  }

  count(generation) {
    return generation === this.ownerGeneration ? this.size : -1;
  }

  free() {
    this.wipeAll();
    this.freed = true;
  }

  push(generation, contextFrame, pcm) {
    if (
      generation !== this.ownerGeneration ||
      !Number.isSafeInteger(contextFrame) ||
      contextFrame < 0 ||
      !(pcm instanceof Uint8Array) ||
      pcm.byteLength !== 640 ||
      (
        this.lastContextFrame !== undefined &&
        contextFrame <= this.lastContextFrame
      )
    ) {
      return 0;
    }
    if (this.size === this.slots.length && !this.overwriteOldest) {
      return 3;
    }
    const owned = Uint8Array.from(pcm).buffer;
    const entry = { contextFrame, pcm: owned };
    if (this.size === this.slots.length) {
      new Uint8Array(this.slots[this.head].pcm).fill(0);
      this.slots[this.head] = entry;
      this.head = (this.head + 1) % this.slots.length;
      this.lastContextFrame = contextFrame;
      return 2;
    }
    const index = (this.head + this.size) % this.slots.length;
    this.slots[index] = entry;
    this.size += 1;
    this.lastContextFrame = contextFrame;
    return 1;
  }

  shiftInto(generation, destination) {
    if (
      generation !== this.ownerGeneration ||
      !(destination instanceof Uint8Array) ||
      destination.byteLength !== 640
    ) {
      return -2;
    }
    if (this.size === 0) return -1;
    const entry = this.slots[this.head];
    destination.set(new Uint8Array(entry.pcm));
    new Uint8Array(entry.pcm).fill(0);
    this.slots[this.head] = null;
    this.head = (this.head + 1) % this.slots.length;
    this.size -= 1;
    if (this.size === 0) this.head = 0;
    return entry.contextFrame;
  }
}

function createHarness(config = {}) {
  const generation = config.generation ?? 7;
  const maximumPreConfirmFrames =
    config.maximumPreConfirmFrames ?? 75;
  const maximumQueuedFrames =
    config.maximumQueuedFrames ?? 8;
  const sampleRateHz = config.sampleRateHz ?? 16_000;
  const processorOptions = Object.hasOwn(config, "processorOptions")
    ? config.processorOptions
    : {
        generation,
        maximumPreConfirmFrames,
        maximumQueuedFrames,
        pcmRingModule: TEST_PCM_RING_MODULE,
      };
  const output = [];
  let ringIndex = 0;
  let Processor;
  class TestAudioWorkletProcessor {
    constructor() {
      this.port = {
        onmessage: null,
        postMessage(message, transfer = []) {
          output.push({
            message: structuredClone(message, { transfer }),
            transferCount: transfer.length,
          });
        },
      };
    }
  }
  const sandbox = {
    Array,
    ArrayBuffer,
    AudioWorkletProcessor: TestAudioWorkletProcessor,
    DataView,
    Error,
    Float32Array,
    Math,
    Number,
    Object,
    Reflect,
    Set,
    createPcmRing(module, ringGeneration, capacity, overwriteOldest) {
      if (module !== TEST_PCM_RING_MODULE) {
        throw new Error("invalid_pcm_ring_module");
      }
      const ring = new TestPcmRing(
        ringGeneration,
        capacity,
        overwriteOldest,
      );
      if (ringIndex === 0 && config.preConfirmCountFault === true) {
        ring.count = () => {
          throw new Error("wasm_trap");
        };
      }
      ringIndex += 1;
      return ring;
    },
    Uint8Array,
    currentFrame: 0,
    registerProcessor(name, constructor) {
      assert.equal(name, "kotae-pcm-capture");
      Processor = constructor;
    },
    sampleRate: sampleRateHz,
  };
  vm.createContext(sandbox);
  new vm.Script(workletSource, {
    filename: "pcm-capture-worklet.js",
  }).runInContext(sandbox);
  assert.equal(typeof Processor, "function");

  const processor = new Processor({ processorOptions });
  let contextFrame = 0;
  function render(sampleCount, value = 0.25) {
    const input = new Float32Array(sampleCount);
    input.fill(value);
    sandbox.currentFrame = contextFrame;
    const keepAlive = processor.process([[input]]);
    contextFrame += sampleCount;
    return keepAlive;
  }
  return {
    control(data) {
      processor.port.onmessage({ data });
    },
    frameMessages() {
      return output
        .map(({ message }) => message)
        .filter(({ type }) => type === "frame");
    },
    generation,
    output,
    processor,
    render,
    renderAt(inputContextFrame, sampleCount, value = 0.25) {
      const input = new Float32Array(sampleCount);
      input.fill(value);
      sandbox.currentFrame = inputContextFrame;
      const keepAlive = processor.process([[input]]);
      contextFrame = inputContextFrame + sampleCount;
      return keepAlive;
    },
    renderFrame(value = 0.25) {
      return render(sampleRateHz / 50, value);
    },
    sampleRateHz,
  };
}

function exactKeys(value, expected) {
  assert.deepEqual(
    Reflect.ownKeys(value).sort(),
    [...expected].sort(),
  );
}

function isZero(value) {
  return [...new Uint8Array(value)].every((byte) => byte === 0);
}

function renderInQuanta(harness, sampleCount, value = 0.25) {
  let remaining = sampleCount;
  while (remaining > 0) {
    const quantum = Math.min(128, remaining);
    harness.render(quantum, value);
    remaining -= quantum;
  }
}

function preConfirmEntries(processor) {
  const entries = [];
  for (
    let offset = 0;
    offset < processor.preConfirmRing.count(processor.generation);
    offset += 1
  ) {
    const index =
      (processor.preConfirmRing.head + offset) %
      processor.maximumPreConfirmFrames;
    entries.push(processor.preConfirmRing.slots[index]);
  }
  return entries;
}

function confirmedEntries(processor) {
  const entries = [];
  for (
    let offset = 0;
    offset < processor.confirmedQueue.count(processor.generation);
    offset += 1
  ) {
    const index =
      (processor.confirmedQueue.head + offset) %
      processor.maximumQueuedFrames;
    entries.push(processor.confirmedQueue.slots[index]);
  }
  return entries;
}

test("processorOptions are exact and strictly bounded", () => {
  const valid = {
    generation: 1,
    maximumPreConfirmFrames: 1,
    maximumQueuedFrames: 1,
    pcmRingModule: TEST_PCM_RING_MODULE,
  };
  assert.doesNotThrow(() => createHarness({ processorOptions: valid }));
  assert.doesNotThrow(() =>
    createHarness({
      processorOptions: {
        ...valid,
        maximumPreConfirmFrames: 125,
      },
    }),
  );

  for (const processorOptions of [
    undefined,
    {},
    { ...valid, generation: 0 },
    { ...valid, generation: 1.5 },
    { ...valid, maximumPreConfirmFrames: 0 },
    { ...valid, maximumPreConfirmFrames: 126 },
    { ...valid, maximumQueuedFrames: 0 },
    { ...valid, maximumQueuedFrames: 201 },
    { ...valid, unexpected: true },
  ]) {
    assert.throws(
      () => createHarness({ processorOptions }),
      /invalid_processor_options/,
    );
  }
});

test("worklet pre-confirm ceiling matches the 2.5 second 80 KB barge ring", () => {
  assert.equal(BARGE_PCM_LIMITS.frameDurationMs, 20);
  assert.equal(BARGE_PCM_LIMITS.historyMs, 2_500);
  assert.equal(BARGE_PCM_LIMITS.maximumFrames, 125);
  assert.equal(
    BARGE_PCM_LIMITS.maximumBytes,
    BARGE_PCM_LIMITS.maximumFrames * VOICE_LIVE_LIMITS.inputFrameBytes,
  );
  assert.match(
    publishedWorkletSource,
    /const MAXIMUM_PRE_CONFIRM_FRAMES = 125;/u,
  );
  assert.match(publishedWorkletSource, /finite 2\.5 s total/u);
  assert.match(publishedWorkletSource, /hard 80 KB PCM16 ceiling/u);
});

test("one thousand pre-confirm frames post no PCM and stay in a zeroizing fixed ring", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 75,
    maximumQueuedFrames: 8,
  });
  harness.renderFrame(0.1);
  const firstPcm = harness.processor.preConfirmRing.slots[0].pcm;

  for (let index = 1; index < 1_000; index += 1) {
    harness.renderFrame((index % 8 + 1) / 10);
  }

  assert.equal(harness.frameMessages().length, 0);
  assert.equal(harness.output.length, 0);
  assert.equal(harness.processor.preConfirmRing.count(harness.generation), 75);
  assert.equal(harness.processor.preConfirmRing.capacity(), 75);
  assert.equal(isZero(firstPcm), true, "evicted PCM was not zeroized");

  const retained = preConfirmEntries(harness.processor)
    .map(({ pcm }) => pcm);
  harness.control({
    type: "stop",
    version: 1,
    generation: harness.generation,
  });
  assert.equal(harness.processor.state, "stopped");
  assert.equal(retained.every(isZero), true);
  assert.equal(harness.renderFrame(), false);
});

test("finite quiet-candidate ring preserves 300 ms pre-roll through the latest valid confirmation", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 75,
    maximumQueuedFrames: 100,
  });
  harness.renderFrame(0.1);
  const evicted = harness.processor.preConfirmRing.slots[0].pcm;
  for (let frame = 1; frame < 76; frame += 1) {
    harness.renderFrame((frame % 8 + 1) / 10);
  }

  assert.equal(harness.processor.preConfirmRing.count(harness.generation), 75);
  assert.equal(isZero(evicted), true);
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    // Frame 16 starts the quiet candidate. Confirmation at frame 75 is 1.18
    // seconds later, immediately before the finite 1.2 second privacy limit.
    candidateContextFrame: 16 * 320,
    leadInFrames: 15,
    initialCredit: 75,
  });

  const frames = harness.frameMessages();
  assert.equal(frames.length, 75);
  assert.equal(frames[0].contextFrame, 320);
  assert.equal(frames.at(-1).contextFrame, 75 * 320);
  assert.deepEqual(
    frames.map(({ sequence }) => sequence),
    Array.from({ length: 75 }, (_, sequence) => sequence),
  );
});

test("confirm releases only the bounded lead-in in exact FIFO and credit order", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 5,
    maximumQueuedFrames: 5,
  });
  harness.renderFrame(0.1);
  harness.renderFrame(0.2);
  harness.renderFrame(0.3);
  assert.equal(harness.output.length, 0);

  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 640,
    leadInFrames: 2,
    initialCredit: 1,
  });
  assert.equal(harness.processor.state, "confirmed");
  assert.equal(harness.frameMessages().length, 1);
  assert.equal(harness.processor.confirmedQueue.count(harness.generation), 2);

  harness.control({
    type: "credit",
    version: 1,
    generation: harness.generation,
    frames: 1,
  });
  harness.control({
    type: "credit",
    version: 1,
    generation: harness.generation,
    frames: 1,
  });
  harness.renderFrame(0.4);
  assert.equal(harness.processor.confirmedQueue.count(harness.generation), 1);
  harness.control({
    type: "credit",
    version: 1,
    generation: harness.generation,
    frames: 1,
  });

  const frames = harness.frameMessages();
  assert.equal(frames.length, 4);
  assert.deepEqual(
    frames.map(({ sequence }) => sequence),
    [0, 1, 2, 3],
  );
  assert.deepEqual(
    frames.map(({ contextFrame }) => contextFrame),
    [0, 320, 640, 960],
  );
  assert.deepEqual(
    frames.map(({ pcm }) => new DataView(pcm).getInt16(0, true)),
    [3_277, 6_553, 9_830, 13_107],
  );
  for (const frame of frames) {
    exactKeys(frame, [
      "type",
      "version",
      "generation",
      "sequence",
      "contextFrame",
      "pcm",
    ]);
    assert.equal(frame.type, "frame");
    assert.equal(frame.version, 1);
    assert.equal(frame.generation, harness.generation);
    assert.equal(frame.pcm.byteLength, 640);
  }
  assert.equal(
    harness.output
      .filter(({ message }) => message.type === "frame")
      .every(({ transferCount }) => transferCount === 1),
    true,
  );
});

test("20 ms PCM frames retain exact 44.1 kHz context sample-clock boundaries", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 4,
    maximumQueuedFrames: 4,
    sampleRateHz: 44_100,
  });
  renderInQuanta(harness, 44_100 * 3 / 50, 0.1);
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 1_764,
    leadInFrames: 2,
    initialCredit: 3,
  });
  assert.deepEqual(
    harness.frameMessages().map(({ contextFrame }) => contextFrame),
    [0, 882, 1_764],
  );
});

test("20 ms PCM frames retain exact 48 kHz context sample-clock boundaries", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 4,
    maximumQueuedFrames: 4,
    sampleRateHz: 48_000,
  });
  renderInQuanta(harness, 48_000 * 3 / 50, 0.1);
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 1_920,
    leadInFrames: 2,
    initialCredit: 3,
  });
  assert.deepEqual(
    harness.frameMessages().map(({ contextFrame }) => contextFrame),
    [0, 960, 1_920],
  );
});

test("stale controls are ignored while confirm drops and zeroizes pre-cutoff PCM", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 4,
    maximumQueuedFrames: 4,
  });
  harness.renderFrame(0.1);
  harness.renderFrame(0.2);
  harness.renderFrame(0.3);
  const [dropped] = preConfirmEntries(harness.processor);

  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation + 1,
    candidateContextFrame: 640,
    leadInFrames: 4,
    initialCredit: 4,
  });
  harness.control({
    type: "stop",
    version: 1,
    generation: harness.generation + 1,
  });
  assert.equal(harness.processor.state, "preconfirm");
  assert.equal(harness.output.length, 0);

  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 640,
    leadInFrames: 1,
    initialCredit: 2,
  });
  assert.equal(isZero(dropped.pcm), true);
  assert.deepEqual(
    harness.frameMessages().map(({ contextFrame }) => contextFrame),
    [320, 640],
  );
});

test("a backward AudioContext sample clock fails closed and zeroizes history", () => {
  const harness = createHarness();
  harness.renderFrame(0.1);
  const retained = preConfirmEntries(harness.processor)[0].pcm;

  assert.equal(harness.renderAt(0, 320, 0.2), false);
  assert.equal(harness.processor.state, "stopped");
  assert.equal(isZero(retained), true);
  assert.equal(harness.output.length, 1);
  assert.equal(harness.output[0].message.code, "capture_invalid");
});

test("same-generation malformed control fails closed and zeroizes retained PCM", () => {
  const harness = createHarness();
  harness.renderFrame(0.2);
  const retained = preConfirmEntries(harness.processor)[0].pcm;
  harness.control({
    type: "stop",
    version: 1,
    generation: harness.generation,
    extra: true,
  });
  assert.equal(harness.processor.state, "stopped");
  assert.equal(isZero(retained), true);
  assert.equal(harness.output.length, 1);
  assert.equal(harness.output[0].message.code, "capture_invalid");
  assert.equal(harness.renderFrame(), false);
});

test("a Wasm count trap clears and frees both rings with one content-free error", () => {
  const harness = createHarness({ preConfirmCountFault: true });
  harness.renderFrame(0.2);
  const retained = harness.processor.preConfirmRing.slots[0].pcm;
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 0,
    leadInFrames: 0,
    initialCredit: 1,
  });

  assert.equal(harness.processor.state, "stopped");
  assert.equal(isZero(retained), true);
  assert.equal(harness.processor.ringsReleased, true);
  assert.equal(harness.processor.preConfirmRing.freed, true);
  assert.equal(harness.processor.confirmedQueue.freed, true);
  assert.equal(harness.output.length, 1);
  assert.equal(harness.output[0].message.code, "capture_invalid");
  harness.control({
    type: "stop",
    version: 1,
    generation: harness.generation,
  });
  assert.equal(harness.output.length, 1);
});

test("credit starvation overflows once, zeroizes the FIFO, and stops", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 2,
    maximumQueuedFrames: 2,
  });
  harness.renderFrame(0.1);
  const preConfirm = preConfirmEntries(harness.processor)[0].pcm;
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 320,
    leadInFrames: 0,
    initialCredit: 0,
  });
  assert.equal(isZero(preConfirm), true);

  harness.renderFrame(0.2);
  harness.renderFrame(0.3);
  const queued = confirmedEntries(harness.processor)
    .map(({ pcm }) => pcm);
  assert.equal(harness.processor.confirmedQueue.count(harness.generation), 2);
  harness.renderFrame(0.4);

  assert.equal(harness.processor.state, "stopped");
  assert.equal(queued.every(isZero), true);
  assert.equal(harness.frameMessages().length, 0);
  assert.equal(harness.output.length, 1);
  const error = harness.output[0].message;
  exactKeys(error, [
    "type",
    "version",
    "generation",
    "code",
  ]);
  assert.deepEqual(
    {
      type: error.type,
      version: error.version,
      generation: error.generation,
      code: error.code,
    },
    {
      type: "error",
      version: 1,
      generation: harness.generation,
      code: "capture_overflow",
    },
  );
  assert.equal(harness.renderFrame(), false);
  assert.equal(harness.output.length, 1, "overflow metadata repeated");
});

test("seal zeroizes partial audio and posts sealed only after credited FIFO drain", () => {
  const harness = createHarness({
    maximumPreConfirmFrames: 4,
    maximumQueuedFrames: 4,
    sampleRateHz: 48_000,
  });
  harness.renderFrame(0.1);
  harness.renderFrame(0.2);
  harness.control({
    type: "confirm",
    version: 1,
    generation: harness.generation,
    candidateContextFrame: 960,
    leadInFrames: 1,
    initialCredit: 1,
  });
  assert.equal(harness.frameMessages().length, 1);
  assert.equal(harness.processor.confirmedQueue.count(harness.generation), 1);

  harness.render(481, 0.3);
  const partial = harness.processor.frame;
  const carry = harness.processor.carry;
  assert.equal(harness.processor.frameOffset, 320);
  assert.equal(carry.length, 1);

  harness.control({
    type: "seal",
    version: 1,
    generation: harness.generation,
  });
  assert.equal(harness.processor.state, "sealing");
  assert.equal(isZero(partial), true);
  assert.equal([...carry].every((sample) => sample === 0), true);
  assert.deepEqual(
    harness.output.map(({ message }) => message.type),
    ["frame"],
  );

  assert.equal(harness.renderFrame(0.8), true);
  assert.equal(harness.processor.confirmedQueue.count(harness.generation), 1);
  harness.control({
    type: "credit",
    version: 1,
    generation: harness.generation,
    frames: 1,
  });

  assert.deepEqual(
    harness.output.map(({ message }) => message.type),
    ["frame", "frame", "sealed"],
  );
  const sealed = harness.output.at(-1).message;
  exactKeys(sealed, [
    "type",
    "version",
    "generation",
    "lastSequence",
  ]);
  assert.deepEqual(
    {
      type: sealed.type,
      version: sealed.version,
      generation: sealed.generation,
      lastSequence: sealed.lastSequence,
    },
    {
      type: "sealed",
      version: 1,
      generation: harness.generation,
      lastSequence: 1,
    },
  );
  assert.equal(harness.processor.state, "stopped");
  assert.equal(harness.renderFrame(), false);
});
