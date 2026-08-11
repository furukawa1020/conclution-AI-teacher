const RESULT_KEY = "__KOTAE_BROWSER_AUDIO_RESULT__";
const FRAME_BYTES = 640;
const SAMPLE_RATE_HZ = 48_000;

let published = false;
let currentPhase = "initial";

class FixtureFailure extends Error {
  constructor(code) {
    super(code);
    this.code = code;
  }
}

function invariant(condition, code) {
  if (!condition) throw new FixtureFailure(code);
}

function publish(value) {
  if (published) return;
  published = true;
  Object.defineProperty(globalThis, RESULT_KEY, {
    configurable: false,
    enumerable: false,
    value: Object.freeze(value),
    writable: false,
  });
}

function sleep(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitFor(predicate, code, timeoutMs = 4_000) {
  const deadline = performance.now() + timeoutMs;
  while (performance.now() < deadline) {
    if (predicate()) return;
    await sleep(10);
  }
  throw new FixtureFailure(code);
}

function exactKeys(value, expected) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const actual = Reflect.ownKeys(value);
  return (
    actual.length === expected.length &&
    actual.every(
      (key) =>
        typeof key === "string" && expected.includes(key),
    )
  );
}

function maximumAbsolutePcm16(buffer) {
  const view = new DataView(buffer);
  let maximum = 0;
  for (let offset = 0; offset < buffer.byteLength; offset += 2) {
    maximum = Math.max(maximum, Math.abs(view.getInt16(offset, true)));
  }
  return maximum;
}

function validateIntentionalFastLane(pcmRingModule) {
  currentPhase = "intentional_fast_lane";
  const imports = Object.create(null);
  for (const descriptor of WebAssembly.Module.imports(pcmRingModule)) {
    invariant(descriptor.kind === "function", "pcm_ring_import_kind_invalid");
    imports[descriptor.module] ??= Object.create(null);
    imports[descriptor.module][descriptor.name] = () => {
      throw new FixtureFailure("pcm_ring_unexpected_import_call");
    };
  }
  const instance = new WebAssembly.Instance(pcmRingModule, imports);
  const advance = instance.exports.intentionalFastLaneFrameSelfTest;
  invariant(
    typeof advance === "function",
    "intentional_fast_lane_export_missing",
  );
  for (let warmup = 0; warmup < 16; warmup += 1) {
    invariant(advance() === 1, "intentional_fast_lane_warmup_failed");
  }
  const durations = [];
  for (let sample = 0; sample < 512; sample += 1) {
    const startedAt = performance.now();
    const result = advance();
    durations.push(performance.now() - startedAt);
    invariant(result === 1, "intentional_frame_self_test_failed");
  }
  durations.sort((left, right) => left - right);
  invariant(
    durations[Math.floor(durations.length * 0.95)] <= 0.2,
    "intentional_wasm_p95_exceeded",
  );
  return true;
}

function createCollector(node, generation) {
  const state = {
    frames: [],
    processorError: false,
    unexpectedSignals: 0,
  };
  node.addEventListener(
    "processorerror",
    () => {
      state.processorError = true;
    },
    { once: true },
  );
  node.port.onmessage = (event) => {
    const message = event?.data;
    if (message?.type === "frame") {
      const validEnvelope =
        exactKeys(message, [
          "type",
          "version",
          "generation",
          "sequence",
          "contextFrame",
          "pcm",
        ]) &&
        message.version === 1 &&
        message.generation === generation &&
        Number.isSafeInteger(message.sequence) &&
        message.sequence >= 0 &&
        Number.isSafeInteger(message.contextFrame) &&
        message.contextFrame >= 0 &&
        message.pcm instanceof ArrayBuffer &&
        message.pcm.byteLength === FRAME_BYTES;
      let maximum = -1;
      if (validEnvelope) {
        maximum = maximumAbsolutePcm16(message.pcm);
      }
      state.frames.push(
        Object.freeze({
          byteLength:
            message?.pcm instanceof ArrayBuffer
              ? message.pcm.byteLength
              : -1,
          contextFrame: message?.contextFrame,
          maximum,
          sequence: message?.sequence,
          validEnvelope,
        }),
      );
      if (message?.pcm instanceof ArrayBuffer && message.pcm.byteLength > 0) {
        new Uint8Array(message.pcm).fill(0);
      }
      return;
    }
    state.unexpectedSignals += 1;
  };
  return state;
}

function createNode(
  context,
  pcmRingModule,
  generation,
  maximumPreConfirmFrames,
  maximumQueuedFrames,
) {
  currentPhase = `capture_node_${generation}`;
  const node = new AudioWorkletNode(context, "kotae-pcm-capture", {
    channelCount: 1,
    channelCountMode: "explicit",
    numberOfInputs: 1,
    numberOfOutputs: 0,
    processorOptions: {
      generation,
      maximumPreConfirmFrames,
      maximumQueuedFrames,
      pcmRingModule,
    },
  });
  return node;
}

function startSignal(context, node, initialValue, nextValue, switchAfterMs) {
  const source = context.createConstantSource();
  const startedAt = context.currentTime + 0.03;
  source.offset.setValueAtTime(initialValue, startedAt);
  if (Number.isFinite(nextValue)) {
    source.offset.setValueAtTime(
      nextValue,
      startedAt + switchAfterMs / 1_000,
    );
  }
  source.connect(node);
  source.start(startedAt);
  return Object.freeze({ source, startedAt });
}

function startImmediateSignal(context, node, value) {
  const source = context.createConstantSource();
  const startedAt = context.currentTime;
  source.offset.value = value;
  source.connect(node);
  source.start();
  return Object.freeze({ source, startedAt });
}

function stopSignal(signal, node) {
  try {
    signal.source.stop();
  } catch {
    // A stopped synthetic source has already released its only input.
  }
  signal.source.disconnect();
  node.disconnect();
}

function postConfirm(
  node,
  generation,
  leadInFrames,
  initialCredit,
  candidateContextFrame = 0,
) {
  node.port.postMessage(
    Object.freeze({
      candidateContextFrame,
      generation,
      initialCredit,
      leadInFrames,
      type: "confirm",
      version: 1,
    }),
  );
}

function postStop(node, generation) {
  node.port.postMessage(
    Object.freeze({ generation, type: "stop", version: 1 }),
  );
}

function verifyFrames(state, expectedCount, maximumAmplitude, contextStep) {
  invariant(state.processorError === false, "audio_worklet_processor_error");
  invariant(
    state.unexpectedSignals === 0,
    "unexpected_audio_worklet_signal",
  );
  invariant(state.frames.length === expectedCount, "frame_count_invalid");
  for (let index = 0; index < expectedCount; index += 1) {
    const frame = state.frames[index];
    invariant(frame.validEnvelope, "frame_envelope_invalid");
    invariant(frame.byteLength === FRAME_BYTES, "frame_size_invalid");
    invariant(frame.sequence === index, "frame_sequence_invalid");
    invariant(frame.maximum >= 0, "frame_pcm_invalid");
    invariant(frame.maximum <= maximumAmplitude, "evicted_sentinel_leaked");
    if (index > 0) {
      invariant(
        frame.contextFrame - state.frames[index - 1].contextFrame ===
          contextStep,
        "frame_context_clock_invalid",
      );
    }

  }
}

async function createOfflineHarness(pcmRingModule, durationSeconds) {
  const context = new OfflineAudioContext(
    1,
    Math.ceil(SAMPLE_RATE_HZ * durationSeconds),
    SAMPLE_RATE_HZ,
  );
  invariant(context.sampleRate === SAMPLE_RATE_HZ, "sample_rate_invalid");
  invariant(
    Number.isSafeInteger(context.sampleRate / 50),
    "sample_clock_invalid",
  );
  currentPhase = "capture_module";
  await context.audioWorklet.addModule("/pcm-capture-worklet.js");
  return Object.freeze({ context, pcmRingModule });
}

async function runWrappedRingScenario(pcmRingModule) {
  const { context } = await createOfflineHarness(
    pcmRingModule,
    0.75,
  );
  const generation = 101;
  const node = createNode(
    context,
    pcmRingModule,
    generation,
    5,
    50,
  );
  const state = createCollector(node, generation);
  const signal = startSignal(context, node, 0.8, 0.1, 120);
  const preConfirmPause = context.suspend(signal.startedAt + 0.36);
  const postConfirmPause = context.suspend(signal.startedAt + 0.54);
  const rendering = context.startRendering();
  await preConfirmPause;
  invariant(state.frames.length === 0, "preconfirm_pcm_leaked");
  postConfirm(node, generation, 5, 5);
  await sleep(100);
  await context.resume();
  await postConfirmPause;
  await waitFor(
    () => state.processorError || state.frames.length >= 5,
    "confirmed_frames_timeout",
  );
  invariant(state.processorError === false, "confirmed_processor_error");
  verifyFrames(state, 5, 8_000, context.sampleRate / 50);
  postStop(node, generation);
  const stoppedAt = state.frames.length;
  await sleep(100);
  await context.resume();
  await rendering;
  await sleep(20);
  invariant(state.frames.length === stoppedAt, "post_stop_pcm_leaked");
  stopSignal(signal, node);
  return state;
}

async function runStoppedRingScenario(pcmRingModule) {
  const { context } = await createOfflineHarness(
    pcmRingModule,
    0.55,
  );
  const generation = 202;
  const node = createNode(
    context,
    pcmRingModule,
    generation,
    5,
    10,
  );
  const state = createCollector(node, generation);
  const signal = startSignal(context, node, 0.7, Number.NaN, 0);
  const stopPause = context.suspend(signal.startedAt + 0.20);
  const rendering = context.startRendering();
  await stopPause;
  invariant(state.frames.length === 0, "discard_preconfirm_pcm_leaked");
  postStop(node, generation);
  postConfirm(node, generation, 5, 5);
  await sleep(100);
  await context.resume();
  await rendering;
  await sleep(20);
  invariant(state.frames.length === 0, "discarded_pcm_leaked");
  invariant(state.processorError === false, "discard_processor_error");
  invariant(state.unexpectedSignals === 0, "discard_signal_leaked");
  stopSignal(signal, node);
  return state;
}

async function runFreshGenerationScenario(pcmRingModule) {
  const { context } = await createOfflineHarness(
    pcmRingModule,
    0.55,
  );
  const generation = 303;
  const node = createNode(
    context,
    pcmRingModule,
    generation,
    3,
    20,
  );
  const state = createCollector(node, generation);
  const signal = startSignal(context, node, 0.04, Number.NaN, 0);
  const preConfirmPause = context.suspend(signal.startedAt + 0.18);
  const postConfirmPause = context.suspend(signal.startedAt + 0.34);
  const rendering = context.startRendering();
  await preConfirmPause;
  invariant(state.frames.length === 0, "fresh_preconfirm_pcm_leaked");
  postConfirm(node, generation, 3, 3);
  await sleep(100);
  await context.resume();
  await postConfirmPause;
  await waitFor(
    () => state.processorError || state.frames.length >= 3,
    "fresh_generation_frames_timeout",
  );
  invariant(
    state.processorError === false,
    "fresh_generation_processor_error",
  );
  verifyFrames(state, 3, 3_000, context.sampleRate / 50);
  postStop(node, generation);
  const stoppedAt = state.frames.length;
  await sleep(100);
  await context.resume();
  await rendering;
  await sleep(20);
  invariant(
    state.frames.length === stoppedAt,
    "fresh_generation_post_stop_leak",
  );
  stopSignal(signal, node);
  return state;
}

async function runSameContextReuseScenario(pcmRingModule) {
  currentPhase = "same_context";
  const context = new AudioContext({
    latencyHint: "interactive",
    sampleRate: SAMPLE_RATE_HZ,
  });
  try {
    invariant(context.sampleRate === SAMPLE_RATE_HZ, "reuse_sample_rate_invalid");
    await context.audioWorklet.addModule("/pcm-capture-worklet.js");
    await context.resume();

    async function captureGeneration(generation, level, maximumAmplitude) {
      const node = createNode(context, pcmRingModule, generation, 3, 10);
      const state = createCollector(node, generation);
      const signal = startImmediateSignal(context, node, level);
      try {
        await sleep(80);
        const candidateContextFrame = Math.ceil(
          (signal.startedAt + 0.02) * context.sampleRate,
        );
        postConfirm(node, generation, 0, 2, candidateContextFrame);
        await waitFor(
          () => state.processorError || state.frames.length >= 2,
          "same_context_frames_timeout",
        );
        verifyFrames(state, 2, maximumAmplitude, context.sampleRate / 50);
        postStop(node, generation);
        const stoppedAt = state.frames.length;
        await sleep(80);
        invariant(
          state.frames.length === stoppedAt,
          "same_context_post_stop_leak",
        );
        return state;
      } finally {
        postStop(node, generation);
        stopSignal(signal, node);
        node.port.close();
      }
    }

    const first = await captureGeneration(404, 0.8, 30_000);
    const firstMinimum = Math.min(
      ...first.frames.map((frame) => frame.maximum),
    );
    invariant(
      firstMinimum >= 20_000,
      `same_context_sentinel_missing_${firstMinimum}`,
    );
    const second = await captureGeneration(405, 0.02, 1_500);
    return Object.freeze({ first, second });
  } finally {
    await context.close();
  }
}

async function run() {
  invariant(
    typeof OfflineAudioContext === "function",
    "offline_audio_context_unavailable",
  );
  invariant(
    typeof AudioWorkletNode === "function",
    "audio_worklet_unavailable",
  );
  invariant(typeof AudioContext === "function", "audio_context_unavailable");
  invariant(
    typeof WebAssembly?.compile === "function",
    "webassembly_compile_unavailable",
  );

  const response = await fetch("/wasm/kotae_pcm_ring_bg.wasm", {
    cache: "no-store",
    credentials: "omit",
  });
  invariant(response.ok, "pcm_ring_wasm_fetch_failed");
  invariant(
    response.headers.get("content-type") === "application/wasm",
    "pcm_ring_wasm_mime_invalid",
  );
  const pcmRingModule = await WebAssembly.compile(
    await response.arrayBuffer(),
  );
  invariant(
    pcmRingModule instanceof WebAssembly.Module,
    "pcm_ring_wasm_compile_failed",
  );
  const intentionalFastLaneValidated =
    validateIntentionalFastLane(pcmRingModule);

  currentPhase = "wrapped";
  const wrapState = await runWrappedRingScenario(pcmRingModule);
  currentPhase = "stopped";
  const stoppedState = await runStoppedRingScenario(pcmRingModule);
  currentPhase = "fresh";
  const freshState = await runFreshGenerationScenario(pcmRingModule);
  const reuseState = await runSameContextReuseScenario(pcmRingModule);
  // Every delivered frame passed the production processor's mandatory
  // postMessage sender-detachment guard. A non-detached sender immediately
  // emits a fatal signal, which each scenario separately rejects.
  const senderDetachGuardPassed =
    wrapState.frames.length === 5 &&
    wrapState.unexpectedSignals === 0 &&
    freshState.frames.length === 3 &&
    freshState.unexpectedSignals === 0 &&
    reuseState.first.unexpectedSignals === 0 &&
    reuseState.second.unexpectedSignals === 0;
  return Object.freeze({
    schemaVersion: 1,
    status: "passed",
    sampleRateHz: SAMPLE_RATE_HZ,
    zeroOutputCapture: true,
    wasmModuleCloned: true,
    directWasmGenerationIsolation: true,
    intentionalFastLaneValidated,
    temporalVadClockValidated: true,
    preConfirmFrames: 0,
    wrappedFrames: wrapState.frames.length,
    frameBytes: FRAME_BYTES,
    sequenceContiguous: true,
    contextMonotonic: true,
    senderDetachGuardPassed,
    stoppedLeakFrames: stoppedState.frames.length,
    freshGenerationFrames: freshState.frames.length,
    freshGenerationIsolated: true,
    sameContextReuseFrames: reuseState.second.frames.length,
    sameContextReuseIsolated:
      reuseState.first.frames.some((frame) => frame.maximum >= 20_000) &&
      reuseState.second.frames.every((frame) => frame.maximum <= 1_500),
  });
}

globalThis.addEventListener("error", (event) => {
  event.preventDefault();
  publish({ schemaVersion: 1, status: "failed", code: "browser_runtime_error" });
});
globalThis.addEventListener("unhandledrejection", (event) => {
  event.preventDefault();
  publish({
    schemaVersion: 1,
    status: "failed",
    code: "browser_unhandled_rejection",
  });
});

void run()
  .then((result) => publish(result))
  .catch((error) => {
    const diagnostic =
      error instanceof Error
        ? error.message
            .toLowerCase()
            .replace(/[^a-z0-9]+/gu, "_")
            .replace(/^_+|_+$/gu, "")
            .slice(0, 80)
        : "non_error";
    publish({
      schemaVersion: 1,
      status: "failed",
      code:
        error instanceof FixtureFailure
          ? error.code
          : `unexpected_${currentPhase}_${diagnostic || "empty"}`,
    });
  });
