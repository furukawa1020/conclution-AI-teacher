import {
  createGuestAFirstSprintSlo,
  validateGuestAFirstSloBatch,
} from "/guest-a-first-slo-policy.mjs";

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

function spectralProjectionPcm16(buffer, frequencyHz) {
  const view = new DataView(buffer);
  let projection = 0;
  for (let offset = 0; offset < buffer.byteLength; offset += 2) {
    const sampleIndex = offset / 2;
    projection +=
      view.getInt16(offset, true) *
      Math.sin(2 * Math.PI * frequencyHz * sampleIndex / 16_000);
  }
  return Math.abs(projection);
}

function highToLowSpectralRatio(buffer) {
  const low = spectralProjectionPcm16(buffer, 250);
  const high = spectralProjectionPcm16(buffer, 4_000);
  return low > 0 ? high / low : 0;
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

function validateObservationAdding(pcmRingModule) {
  currentPhase = "observation_adding";
  const imports = Object.create(null);
  for (const descriptor of WebAssembly.Module.imports(pcmRingModule)) {
    invariant(descriptor.kind === "function", "observation_import_kind_invalid");
    imports[descriptor.module] ??= Object.create(null);
    imports[descriptor.module][descriptor.name] = () => {
      throw new FixtureFailure("observation_unexpected_import_call");
    };
  }
  const instance = new WebAssembly.Instance(pcmRingModule, imports);
  const validate = instance.exports.observationAddingSelfTest;
  invariant(typeof validate === "function", "observation_adding_export_missing");
  for (let warmup = 0; warmup < 16; warmup += 1) {
    invariant(validate() === 1, "observation_adding_warmup_failed");
  }
  const durations = [];
  for (let sample = 0; sample < 256; sample += 1) {
    const startedAt = performance.now();
    invariant(validate() === 1, "observation_adding_self_test_failed");
    durations.push(performance.now() - startedAt);
  }
  durations.sort((left, right) => left - right);
  invariant(
    durations[Math.floor(durations.length * 0.95)] <= 0.5,
    "observation_adding_wasm_p95_exceeded",
  );
  return true;
}

function guestAFirstObservation({
  aiOutputBeforeAnswer = false,
  listeningAt = 1_000,
  responseAt = 11_000,
  transitionProof = "question_bound_input_clause_later_to_first",
} = {}) {
  const slo = createGuestAFirstSprintSlo();
  invariant(slo.begin(0), "guest_a_first_begin_rejected");
  invariant(slo.markListening(listeningAt), "guest_a_first_listening_rejected");
  invariant(slo.markQuestionEnded(10_000), "guest_a_first_question_end_rejected");
  invariant(slo.markResponseStarted(responseAt), "guest_a_first_response_rejected");
  return slo.finish(30_000, {
    aiOutputBeforeAnswer,
    answerProof: "question_bound_input_answer_first",
    coachAction: "complete",
    coachPhase: "complete",
    guestAFirstOutcome: "changed_to_answer_first",
    transitionProof,
  });
}

function validateGuestAFirstSprintSlo() {
  currentPhase = "guest_a_first_slo";
  const observations = Array.from({ length: 100 }, (_, index) =>
    guestAFirstObservation({
      listeningAt: index < 95 ? 1_000 : 1_001,
      responseAt: index < 95 ? 11_000 : 11_001,
    }),
  );
  invariant(
    validateGuestAFirstSloBatch(observations),
    "guest_a_first_p95_boundary_invalid",
  );

  const substitution = guestAFirstObservation({ aiOutputBeforeAnswer: true });
  invariant(
    substitution.aiSubstitution === "detected" &&
      substitution.counterexample === "rejected",
    "guest_a_first_substitution_accepted",
  );
  invariant(
    !validateGuestAFirstSloBatch([
      substitution,
      ...observations.slice(1),
    ]),
    "guest_a_first_substitution_batch_accepted",
  );

  const wrongTransition = guestAFirstObservation({
    transitionProof: "question_bound_input_clause_first_to_later",
  });
  invariant(
    wrongTransition.completion === "not_verified" &&
      wrongTransition.counterexample === "rejected",
    "guest_a_first_wrong_transition_accepted",
  );

  const missingAnswer = createGuestAFirstSprintSlo();
  invariant(missingAnswer.begin(0), "guest_a_first_negative_begin_rejected");
  invariant(missingAnswer.markListening(500), "guest_a_first_negative_listen_rejected");
  invariant(missingAnswer.markQuestionEnded(10_000), "guest_a_first_negative_end_rejected");
  invariant(missingAnswer.markResponseStarted(10_500), "guest_a_first_negative_response_rejected");
  const rejected = missingAnswer.finish(20_000, {
    aiOutputBeforeAnswer: false,
    answerProof: "none",
    coachAction: "complete",
    coachPhase: "complete",
    guestAFirstOutcome: "changed_to_answer_first",
    transitionProof: "question_bound_input_clause_later_to_first",
  });
  invariant(
    rejected.counterexample === "rejected",
    "guest_a_first_no_answer_accepted",
  );
  return true;
}

function validateGuestQuietOnset(clientModule) {
  currentPhase = "guest_quiet_onset";
  const imports = Object.create(null);
  for (const descriptor of WebAssembly.Module.imports(clientModule)) {
    invariant(descriptor.kind === "function", "pcm_ring_import_kind_invalid");
    imports[descriptor.module] ??= Object.create(null);
    imports[descriptor.module][descriptor.name] = () => {
      throw new FixtureFailure("pcm_ring_unexpected_import_call");
    };
  }
  const instance = new WebAssembly.Instance(clientModule, imports);
  const validate = instance.exports.guestQuietOnsetSelfTest;
  invariant(typeof validate === "function", "guest_quiet_onset_export_missing");
  invariant(validate() === 1, "guest_quiet_onset_self_test_failed");
  return true;
}

function validateQuietSubbandEvidence(clientModule) {
  currentPhase = "quiet_subband_evidence";
  const imports = Object.create(null);
  for (const descriptor of WebAssembly.Module.imports(clientModule)) {
    invariant(descriptor.kind === "function", "quiet_subband_import_kind_invalid");
    imports[descriptor.module] ??= Object.create(null);
    imports[descriptor.module][descriptor.name] = () => {
      throw new FixtureFailure("quiet_subband_unexpected_import_call");
    };
  }
  const instance = new WebAssembly.Instance(clientModule, imports);
  const validate = instance.exports.quietSubbandEvidenceSelfTest;
  invariant(
    typeof validate === "function",
    "quiet_subband_evidence_export_missing",
  );
  invariant(validate() === 1, "quiet_subband_evidence_self_test_failed");
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
          spectralRatio:
            validEnvelope ? highToLowSpectralRatio(message.pcm) : 0,
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

function startWhisperSignal(context, node) {
  const startedAt = context.currentTime + 0.03;
  const branches = [250, 4_000].map((frequency) => {
    const oscillator = context.createOscillator();
    const gain = context.createGain();
    oscillator.frequency.value = frequency;
    gain.gain.value = 0.012;
    oscillator.connect(gain).connect(node);
    oscillator.start(startedAt);
    return Object.freeze({ gain, oscillator });
  });
  return Object.freeze({
    source: Object.freeze({
      stop() {
        branches.forEach(({ oscillator }) => oscillator.stop());
      },
      disconnect() {
        branches.forEach(({ gain, oscillator }) => {
          oscillator.disconnect();
          gain.disconnect();
        });
      },
    }),
    startedAt,
  });
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
  quietConfirmed = false,
) {
  node.port.postMessage(
    Object.freeze({
      candidateContextFrame,
      generation,
      initialCredit,
      leadInFrames,
      quietConfirmed,
      type: "confirm",
      version: 1,
    }),
  );
}

async function runQuietGainScenario(pcmRingModule) {
  const baseline = await runWhisperBaselineScenario(pcmRingModule);
  const { context } = await createOfflineHarness(pcmRingModule, 0.45);
  const generation = 606;
  const node = createNode(context, pcmRingModule, generation, 3, 10);
  const state = createCollector(node, generation);
  const signal = startWhisperSignal(context, node);
  const pause = context.suspend(signal.startedAt + 0.18);
  const rendering = context.startRendering();
  await pause;
  invariant(state.frames.length === 0, "quiet_gain_preconfirm_pcm_leaked");
  postConfirm(node, generation, 3, 3, 0, true);
  await waitFor(
    () => state.processorError || state.frames.length >= 3,
    "quiet_gain_frames_timeout",
  );
  invariant(state.processorError === false, "quiet_gain_processor_error");
  invariant(
    state.frames.every((frame) => frame.maximum > 500),
    "quiet_observation_gain_floor_invalid",
  );
  invariant(
    state.frames.every((frame) => frame.maximum <= 26_870),
    "quiet_observation_headroom_invalid",
  );
  invariant(
    state.frames.every((frame) => frame.spectralRatio > baseline * 1.1),
    "quiet_observation_spectral_support_invalid",
  );
  const fallback = {
    frames: [],
    sealed: false,
    terminal: false,
  };
  node.port.onmessage = (event) => {
    const message = event?.data;
    if (message?.type === "sealed") {
      fallback.sealed =
        exactKeys(message, [
          "type",
          "version",
          "generation",
          "lastSequence",
        ]) &&
        message.version === 1 &&
        message.generation === generation &&
        message.lastSequence === state.frames.length - 1;
      return;
    }
    if (message?.type === "fallback-frame") {
      const valid =
        exactKeys(message, [
          "type",
          "version",
          "generation",
          "sequence",
          "frameCount",
          "pcm",
        ]) &&
        message.version === 1 &&
        message.generation === generation &&
        message.sequence === fallback.frames.length &&
        Number.isSafeInteger(message.frameCount) &&
        message.frameCount > 0 &&
        message.frameCount <= 50 &&
        message.pcm instanceof ArrayBuffer &&
        message.pcm.byteLength === message.frameCount * FRAME_BYTES;
      fallback.frames.push({
        maximum: valid ? maximumAbsolutePcm16(message.pcm) : -1,
        spectralRatio: valid ? highToLowSpectralRatio(message.pcm) : 0,
        valid,
      });
      if (message?.pcm instanceof ArrayBuffer) {
        new Uint8Array(message.pcm).fill(0);
      }
      return;
    }
    if (message?.type === "fallback-sealed") {
      fallback.terminal =
        exactKeys(message, [
          "type",
          "version",
          "generation",
          "lastSequence",
          "totalFrames",
        ]) &&
        message.version === 1 &&
        message.generation === generation &&
        message.lastSequence === fallback.frames.length - 1 &&
        message.totalFrames === state.frames.length;
    }
  };
  node.port.postMessage(
    Object.freeze({ generation, type: "seal", version: 1 }),
  );
  await waitFor(() => fallback.sealed, "quiet_fallback_seal_timeout");
  node.port.postMessage(
    Object.freeze({ generation, type: "take-fallback", version: 1 }),
  );
  await waitFor(() => fallback.terminal, "quiet_fallback_take_timeout");
  invariant(fallback.frames.length === 1, "quiet_fallback_chunk_count_invalid");
  invariant(
    fallback.frames.every(
      (frame) =>
        frame.valid &&
        frame.maximum > 500 &&
        frame.maximum <= 26_870 &&
        frame.spectralRatio > baseline * 1.1,
    ),
    "quiet_fallback_gain_bytes_invalid",
  );
  await context.resume();
  await rendering;
  stopSignal(signal, node);
  return true;
}

async function runWhisperBaselineScenario(pcmRingModule) {
  const { context } = await createOfflineHarness(pcmRingModule, 0.32);
  const generation = 605;
  const node = createNode(context, pcmRingModule, generation, 3, 10);
  const state = createCollector(node, generation);
  const signal = startWhisperSignal(context, node);
  const pause = context.suspend(signal.startedAt + 0.18);
  const rendering = context.startRendering();
  await pause;
  postConfirm(node, generation, 3, 3, 0, false);
  await waitFor(
    () => state.processorError || state.frames.length >= 3,
    "quiet_baseline_frames_timeout",
  );
  invariant(state.processorError === false, "quiet_baseline_processor_error");
  invariant(
    state.frames.every(
      (frame) => frame.validEnvelope && frame.spectralRatio > 0,
    ),
    "quiet_baseline_invalid",
  );
  const ratio = state.frames
    .map((frame) => frame.spectralRatio)
    .reduce((sum, value) => sum + value, 0) / state.frames.length;
  postStop(node, generation);
  await context.resume();
  await rendering;
  stopSignal(signal, node);
  return ratio;
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
  const clientResponse = await fetch("/wasm/kotae_client_bg.wasm", {
    cache: "no-store",
    credentials: "omit",
  });
  invariant(clientResponse.ok, "client_wasm_fetch_failed");
  invariant(
    clientResponse.headers.get("content-type") === "application/wasm",
    "client_wasm_mime_invalid",
  );
  const clientModule = await WebAssembly.compile(await clientResponse.arrayBuffer());
  invariant(
    pcmRingModule instanceof WebAssembly.Module,
    "pcm_ring_wasm_compile_failed",
  );
  const intentionalFastLaneValidated =
    validateIntentionalFastLane(pcmRingModule);
  const observationAddingValidated = validateObservationAdding(pcmRingModule);
  const rustGuestQuietOnsetValidated = validateGuestQuietOnset(clientModule);
  const quietSubbandEvidenceValidated = validateQuietSubbandEvidence(clientModule);
  const guestAFirstSprintSloValidated = validateGuestAFirstSprintSlo();

  currentPhase = "wrapped";
  const wrapState = await runWrappedRingScenario(pcmRingModule);
  currentPhase = "stopped";
  const stoppedState = await runStoppedRingScenario(pcmRingModule);
  currentPhase = "fresh";
  const freshState = await runFreshGenerationScenario(pcmRingModule);
  const quietGainValidated = await runQuietGainScenario(pcmRingModule);
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
    observationAddingValidated,
    guestAFirstSprintSloValidated,
    guestQuietOnsetValidated:
      rustGuestQuietOnsetValidated && quietGainValidated,
    quietSubbandEvidenceValidated,
    quietSpectralCompensationValidated: quietGainValidated,
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
