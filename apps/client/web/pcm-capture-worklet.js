import { createPcmRing } from "./pcm-ring-worklet-runtime.js";

const OUTPUT_SAMPLE_RATE_HZ = 16_000;
const FRAME_SAMPLES = 320;
const FRAME_BYTES = FRAME_SAMPLES * 2;
// 125 x 20 ms = a finite 2.5 s total: the 2.4 s candidate window plus
// 100 ms pre-roll. This is a hard 80 KB PCM16 ceiling, not an open-ended recorder.
const MAXIMUM_PRE_CONFIRM_FRAMES = 125;
const MAXIMUM_QUEUED_FRAMES = 200;
// 10,500 x 20 ms = the existing three-minute-thirty-second turn ceiling.
// This ring exists only for a Rust-confirmed quiet turn.
const MAXIMUM_HTTP_FALLBACK_FRAMES = 10_500;
const HTTP_FALLBACK_CHUNK_FRAMES = 50;
const CONTROL_VERSION = 1;
const RING_PUSH_INSERTED = 1;
const RING_PUSH_INSERTED_AFTER_EVICTION = 2;
const RING_PUSH_FULL = 3;
const QUIET_COMPENSATION_INVALID = 0;

function isPositiveSafeInteger(value, maximum) {
  return (
    Number.isSafeInteger(value) &&
    value > 0 &&
    value <= maximum
  );
}

function hasExactKeys(value, expected) {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value)
  ) {
    return false;
  }
  const actual = Reflect.ownKeys(value);
  if (
    actual.length !== expected.length ||
    actual.some((key) => typeof key !== "string")
  ) {
    return false;
  }
  const allowed = new Set(expected);
  return actual.every((key) => allowed.has(key));
}

function zeroizeBuffer(value) {
  if (value instanceof ArrayBuffer && value.byteLength > 0) {
    new Uint8Array(value).fill(0);
  }
}

function zeroizeFloats(value) {
  if (value instanceof Float32Array && value.length > 0) {
    value.fill(0);
  }
}

class KotaePcmCaptureProcessor extends AudioWorkletProcessor {
  constructor(options) {
    super();
    const processorOptions = options?.processorOptions;
    if (
      !hasExactKeys(processorOptions, [
        "generation",
        "maximumPreConfirmFrames",
        "maximumQueuedFrames",
        "pcmRingModule",
      ]) ||
      !isPositiveSafeInteger(
        processorOptions.generation,
        Number.MAX_SAFE_INTEGER,
      ) ||
      !isPositiveSafeInteger(
        processorOptions.maximumPreConfirmFrames,
        MAXIMUM_PRE_CONFIRM_FRAMES,
      ) ||
      !isPositiveSafeInteger(
        processorOptions.maximumQueuedFrames,
        MAXIMUM_QUEUED_FRAMES,
      )
    ) {
      throw new Error("invalid_processor_options");
    }
    if (
      !Number.isSafeInteger(sampleRate) ||
      sampleRate < OUTPUT_SAMPLE_RATE_HZ ||
      sampleRate > 192_000 ||
      !Number.isSafeInteger(sampleRate / 50)
    ) {
      throw new Error("unsupported_input_sample_rate");
    }

    this.generation = processorOptions.generation;
    this.maximumPreConfirmFrames =
      processorOptions.maximumPreConfirmFrames;
    this.maximumQueuedFrames =
      processorOptions.maximumQueuedFrames;
    this.pcmRingModule = processorOptions.pcmRingModule;
    this.contextFramesPerOutputFrame = sampleRate / 50;

    this.ratio = sampleRate / OUTPUT_SAMPLE_RATE_HZ;
    this.positionNumerator = 0;
    this.carry = new Float32Array(0);
    this.carryContextFrame = undefined;

    this.frame = new ArrayBuffer(FRAME_BYTES);
    this.frameView = new DataView(this.frame);
    this.frameOffset = 0;
    this.frameContextFrame = undefined;

    if (typeof createPcmRing !== "function") {
      throw new Error("pcm_ring_runtime_unavailable");
    }
    try {
      this.preConfirmRing = createPcmRing(
        processorOptions.pcmRingModule,
        this.generation,
        this.maximumPreConfirmFrames,
        true,
      );
      this.confirmedQueue = createPcmRing(
        processorOptions.pcmRingModule,
        this.generation,
        this.maximumQueuedFrames,
        false,
      );
      this.ringsReleased = false;
      this.fallbackRing = undefined;
      this.fallbackRingReleased = false;
    } catch (error) {
      try {
        this.preConfirmRing?.clear?.(this.generation);
        this.preConfirmRing?.free?.();
      } catch {
        // Construction is already fail-closed.
      }
      throw error;
    }
    this.credit = 0;
    this.sequence = 0;
    this.confirmedCutoffContextFrame = undefined;
    this.quietGainCandidateContextFrame = undefined;
    this.quietGainEnabled = false;

    this.state = "preconfirm";
    this.errorPosted = false;
    this.sealedPosted = false;
    this.port.onmessage = (event) => {
      try {
        this.handleControl(event?.data);
      } catch {
        this.failClosed();
      }
    };
  }

  resetResampler(contextFrame) {
    zeroizeFloats(this.carry);
    this.carry = new Float32Array(0);
    this.carryContextFrame = contextFrame;
    this.positionNumerator = 0;
    this.zeroizePartialFrame(true);
  }

  zeroizePartialFrame(replace) {
    zeroizeBuffer(this.frame);
    this.frameOffset = 0;
    this.frameContextFrame = undefined;
    if (replace) {
      this.frame = new ArrayBuffer(FRAME_BYTES);
      this.frameView = new DataView(this.frame);
    } else {
      this.frame = null;
      this.frameView = null;
    }
  }

  clearPreConfirmRing() {
    if (this.ringsReleased) return;
    try {
      this.preConfirmRing.clear(this.generation);
    } catch {
      // The processor remains fail-closed even if the Wasm instance faulted.
    }
  }

  clearConfirmedQueue() {
    if (this.ringsReleased) return;
    try {
      this.confirmedQueue.clear(this.generation);
    } catch {
      // The processor remains fail-closed even if the Wasm instance faulted.
    }
  }

  clearPrivateAudio() {
    this.clearPreConfirmRing();
    this.clearConfirmedQueue();
    this.clearFallbackRing();
    zeroizeFloats(this.carry);
    this.carry = new Float32Array(0);
    this.carryContextFrame = undefined;
    this.positionNumerator = 0;
    this.zeroizePartialFrame(false);
    this.credit = 0;
    this.quietGainCandidateContextFrame = undefined;
    this.quietGainEnabled = false;
  }

  clearFallbackRing() {
    if (this.fallbackRingReleased || !this.fallbackRing) return;
    try {
      this.fallbackRing.clear(this.generation);
    } catch {
      // The processor remains fail-closed even if the Wasm instance faulted.
    }
  }

  releaseTransportRings() {
    if (this.ringsReleased) return;
    this.clearPreConfirmRing();
    this.clearConfirmedQueue();
    this.ringsReleased = true;
    for (const ring of [this.preConfirmRing, this.confirmedQueue]) {
      try {
        ring.free();
      } catch {
        // clear already wiped all retained frames before ownership release.
      }
    }
  }

  releaseRings() {
    this.releaseTransportRings();
    if (this.fallbackRingReleased) return;
    this.clearFallbackRing();
    this.fallbackRingReleased = true;
    try {
      this.fallbackRing?.free?.();
    } catch {
      // clear already wiped all retained fallback frames.
    }
    this.fallbackRing = undefined;
  }

  countRing(ring, maximum) {
    let count;
    try {
      count = ring.count(this.generation);
    } catch {
      this.failClosed();
      return undefined;
    }
    if (
      !Number.isSafeInteger(count) ||
      count < 0 ||
      count > maximum
    ) {
      this.failClosed();
      return undefined;
    }
    return count;
  }

  failClosed() {
    if (this.state === "stopped") return;
    this.clearPrivateAudio();
    this.releaseRings();
    this.state = "stopped";
    this.postError("capture_invalid");
  }

  postError(code) {
    if (this.errorPosted) return;
    this.errorPosted = true;
    try {
      this.port.postMessage(Object.freeze({
        type: "error",
        version: CONTROL_VERSION,
        generation: this.generation,
        code,
      }));
    } catch {
      // The processor is already fail-closed and contains no retained audio.
    }
  }

  failOverflow(extraEntry) {
    if (this.state === "stopped") {
      if (extraEntry !== undefined) zeroizeBuffer(extraEntry.pcm);
      return;
    }
    if (extraEntry !== undefined) zeroizeBuffer(extraEntry.pcm);
    this.clearPrivateAudio();
    this.releaseRings();
    this.state = "stopped";
    this.postError("capture_overflow");
  }

  pushPreConfirm(entry) {
    let result = 0;
    try {
      result = this.preConfirmRing.push(
        this.generation,
        entry.contextFrame,
        new Uint8Array(entry.pcm),
      );
    } catch {
      result = 0;
    } finally {
      zeroizeBuffer(entry.pcm);
    }
    if (
      result !== RING_PUSH_INSERTED &&
      result !== RING_PUSH_INSERTED_AFTER_EVICTION
    ) {
      this.failClosed();
    }
  }

  shiftPreConfirm() {
    return this.shiftRing(this.preConfirmRing);
  }

  enqueueConfirmed(entry) {
    if (
      this.state !== "confirmed" &&
      this.state !== "sealing"
    ) {
      zeroizeBuffer(entry.pcm);
      return;
    }
    let result = 0;
    let fallbackResult = RING_PUSH_INSERTED;
    try {
      if (this.fallbackRing) {
        fallbackResult = this.fallbackRing.push(
          this.generation,
          entry.contextFrame,
          new Uint8Array(entry.pcm),
        );
      }
      result = this.confirmedQueue.push(
        this.generation,
        entry.contextFrame,
        new Uint8Array(entry.pcm),
      );
    } catch {
      result = 0;
    } finally {
      zeroizeBuffer(entry.pcm);
    }
    if (result === RING_PUSH_FULL || fallbackResult === RING_PUSH_FULL) {
      this.failOverflow();
      return;
    }
    if (
      result !== RING_PUSH_INSERTED ||
      fallbackResult !== RING_PUSH_INSERTED
    ) {
      this.failClosed();
      return;
    }
    this.flushConfirmed();
  }

  shiftConfirmed() {
    return this.shiftRing(this.confirmedQueue);
  }

  normalizeConfirmedQuietSpeech(entry) {
    if (
      !this.quietGainEnabled ||
      entry.contextFrame < this.quietGainCandidateContextFrame
    ) {
      return true;
    }
    if (!(entry.pcm instanceof ArrayBuffer) || entry.pcm.byteLength !== FRAME_BYTES) {
      return false;
    }
    try {
      return this.confirmedQueue.compensateQuietFrame(
        this.generation,
        new Uint8Array(entry.pcm),
      ) !== QUIET_COMPENSATION_INVALID;
    } catch {
      return false;
    }
  }

  shiftRing(ring) {
    const pcm = new ArrayBuffer(FRAME_BYTES);
    let contextFrame;
    try {
      contextFrame = ring.shiftInto(
        this.generation,
        new Uint8Array(pcm),
      );
    } catch {
      zeroizeBuffer(pcm);
      this.failClosed();
      return undefined;
    }
    if (!Number.isSafeInteger(contextFrame) || contextFrame < 0) {
      zeroizeBuffer(pcm);
      this.failClosed();
      return undefined;
    }
    return { contextFrame, pcm };
  }

  flushConfirmed() {
    while (this.credit > 0 && this.state !== "stopped") {
      const confirmedCount = this.countRing(
        this.confirmedQueue,
        this.maximumQueuedFrames,
      );
      if (confirmedCount === undefined || confirmedCount === 0) break;
      const entry = this.shiftConfirmed();
      if (entry === undefined) {
        this.failClosed();
        return;
      }
      const completed = entry.pcm;
      const message = Object.freeze({
        type: "frame",
        version: CONTROL_VERSION,
        generation: this.generation,
        sequence: this.sequence,
        contextFrame: entry.contextFrame,
        pcm: completed,
      });
      try {
        this.port.postMessage(message, [completed]);
        if (completed.byteLength !== 0) {
          zeroizeBuffer(completed);
          this.failClosed();
          return;
        }
      } catch {
        zeroizeBuffer(completed);
        this.failClosed();
        return;
      }
      this.sequence += 1;
      this.credit -= 1;
    }
    this.maybePostSealed();
  }

  maybePostSealed() {
    if (this.state !== "sealing" || this.sealedPosted) return;
    const confirmedCount = this.countRing(
      this.confirmedQueue,
      this.maximumQueuedFrames,
    );
    if (confirmedCount === undefined) return;
    if (confirmedCount !== 0) return;
    this.sealedPosted = true;
    try {
      this.port.postMessage(Object.freeze({
        type: "sealed",
        version: CONTROL_VERSION,
        generation: this.generation,
        lastSequence: this.sequence - 1,
      }));
    } catch {
      // No audio remains queued; terminal failure is already fail-closed.
    }
    this.credit = 0;
    this.releaseTransportRings();
    if (this.fallbackRing) {
      this.state = "sealed";
    } else {
      this.releaseRings();
      this.state = "stopped";
    }
  }

  completeFrame(entry) {
    if (this.state === "preconfirm") {
      this.pushPreConfirm(entry);
      return;
    }
    if (this.state === "confirmed") {
      if (entry.contextFrame < this.confirmedCutoffContextFrame) {
        zeroizeBuffer(entry.pcm);
        return;
      }
      if (!this.normalizeConfirmedQuietSpeech(entry)) {
        zeroizeBuffer(entry.pcm);
        this.failClosed();
        return;
      }
      this.enqueueConfirmed(entry);
      return;
    }
    zeroizeBuffer(entry.pcm);
  }

  appendSample(value, contextPosition) {
    if (this.frameView === null) return;
    if (this.frameOffset === 0) {
      const boundary = Math.round(contextPosition);
      if (!Number.isSafeInteger(boundary) || boundary < 0) {
        this.failClosed();
        return;
      }
      this.frameContextFrame = boundary;
    }
    const sample = Number.isFinite(value)
      ? Math.max(-1, Math.min(1, value))
      : 0;
    const pcm =
      sample < 0
        ? Math.round(sample * 32_768)
        : Math.round(sample * 32_767);
    this.frameView.setInt16(this.frameOffset, pcm, true);
    this.frameOffset += 2;
    if (this.frameOffset !== FRAME_BYTES) return;

    const completed = {
      contextFrame: this.frameContextFrame,
      pcm: this.frame,
    };
    this.frame = new ArrayBuffer(FRAME_BYTES);
    this.frameView = new DataView(this.frame);
    this.frameOffset = 0;
    this.frameContextFrame = undefined;
    this.completeFrame(completed);
  }

  confirm(control) {
    if (
      this.state !== "preconfirm" ||
      !hasExactKeys(control, [
        "type",
        "version",
        "generation",
        "candidateContextFrame",
        "leadInFrames",
        "initialCredit",
        "quietConfirmed",
      ]) ||
      control.type !== "confirm" ||
      control.version !== CONTROL_VERSION ||
      !Number.isSafeInteger(control.candidateContextFrame) ||
      control.candidateContextFrame < 0 ||
      !Number.isSafeInteger(control.leadInFrames) ||
      control.leadInFrames < 0 ||
      control.leadInFrames > this.maximumPreConfirmFrames ||
      !Number.isSafeInteger(control.initialCredit) ||
      control.initialCredit < 0 ||
      control.initialCredit > this.maximumQueuedFrames ||
      typeof control.quietConfirmed !== "boolean"
    ) {
      this.failClosed();
      return;
    }

    this.confirmedCutoffContextFrame =
      control.candidateContextFrame -
      control.leadInFrames * this.contextFramesPerOutputFrame;
    this.credit = control.initialCredit;
    this.quietGainCandidateContextFrame = control.candidateContextFrame;
    this.quietGainEnabled = control.quietConfirmed;
    if (control.quietConfirmed) {
      try {
        this.fallbackRing = createPcmRing(
          this.pcmRingModule,
          this.generation,
          MAXIMUM_HTTP_FALLBACK_FRAMES,
          false,
        );
      } catch {
        this.failClosed();
        return;
      }
    }
    this.state = "confirmed";

    while (this.state === "confirmed") {
      const preConfirmCount = this.countRing(
        this.preConfirmRing,
        this.maximumPreConfirmFrames,
      );
      if (preConfirmCount === undefined || preConfirmCount === 0) break;
      const entry = this.shiftPreConfirm();
      if (entry === undefined) {
        this.failClosed();
        return;
      }
      if (entry.contextFrame < this.confirmedCutoffContextFrame) {
        zeroizeBuffer(entry.pcm);
      } else {
        if (!this.normalizeConfirmedQuietSpeech(entry)) {
          zeroizeBuffer(entry.pcm);
          this.failClosed();
          return;
        }
        this.enqueueConfirmed(entry);
      }
    }
  }

  addCredit(control) {
    if (
      (this.state !== "confirmed" && this.state !== "sealing") ||
      !hasExactKeys(control, [
        "type",
        "version",
        "generation",
        "frames",
      ]) ||
      control.type !== "credit" ||
      control.version !== CONTROL_VERSION ||
      !Number.isSafeInteger(control.frames) ||
      control.frames <= 0 ||
      control.frames > this.maximumQueuedFrames ||
      this.credit + control.frames > this.maximumQueuedFrames
    ) {
      this.failClosed();
      return;
    }
    this.credit += control.frames;
    this.flushConfirmed();
  }

  seal(control) {
    if (
      this.state !== "confirmed" ||
      !hasExactKeys(control, [
        "type",
        "version",
        "generation",
      ]) ||
      control.type !== "seal" ||
      control.version !== CONTROL_VERSION
    ) {
      this.failClosed();
      return;
    }
    zeroizeFloats(this.carry);
    this.carry = new Float32Array(0);
    this.carryContextFrame = undefined;
    this.positionNumerator = 0;
    this.zeroizePartialFrame(false);
    this.state = "sealing";
    this.flushConfirmed();
  }

  takeFallback(control) {
    if (
      this.state !== "sealed" ||
      !this.fallbackRing ||
      !hasExactKeys(control, ["type", "version", "generation"]) ||
      control.type !== "take-fallback" ||
      control.version !== CONTROL_VERSION
    ) {
      this.failClosed();
      return;
    }
    let remaining = this.countRing(
      this.fallbackRing,
      MAXIMUM_HTTP_FALLBACK_FRAMES,
    );
    if (remaining === undefined || remaining <= 0) {
      this.failClosed();
      return;
    }
    const totalFrames = remaining;
    let sequence = 0;
    let lastContextFrame = -1;
    while (remaining > 0) {
      const frameCount = Math.min(
        remaining,
        HTTP_FALLBACK_CHUNK_FRAMES,
      );
      const pcm = new ArrayBuffer(frameCount * FRAME_BYTES);
      let valid = true;
      try {
        for (let index = 0; index < frameCount; index += 1) {
          const destination = new Uint8Array(
            pcm,
            index * FRAME_BYTES,
            FRAME_BYTES,
          );
          const contextFrame = this.fallbackRing.shiftInto(
            this.generation,
            destination,
          );
          if (
            !Number.isSafeInteger(contextFrame) ||
            contextFrame <= lastContextFrame
          ) {
            valid = false;
            break;
          }
          lastContextFrame = contextFrame;
        }
        if (!valid) throw new Error("fallback_shift_invalid");
        this.port.postMessage(
          Object.freeze({
            type: "fallback-frame",
            version: CONTROL_VERSION,
            generation: this.generation,
            sequence,
            frameCount,
            pcm,
          }),
          [pcm],
        );
        if (pcm.byteLength !== 0) {
          throw new Error("fallback_transfer_failed");
        }
      } catch {
        zeroizeBuffer(pcm);
        this.failClosed();
        return;
      }
      remaining -= frameCount;
      sequence += 1;
    }
    try {
      this.port.postMessage(Object.freeze({
        type: "fallback-sealed",
        version: CONTROL_VERSION,
        generation: this.generation,
        lastSequence: sequence - 1,
        totalFrames,
      }));
    } catch {
      this.failClosed();
      return;
    }
    this.releaseRings();
    this.state = "stopped";
  }

  stop(control) {
    if (
      !hasExactKeys(control, [
        "type",
        "version",
        "generation",
      ]) ||
      control.type !== "stop" ||
      control.version !== CONTROL_VERSION
    ) {
      this.failClosed();
      return;
    }
    this.clearPrivateAudio();
    this.releaseRings();
    this.state = "stopped";
  }

  handleControl(control) {
    if (this.state === "stopped") return;
    if (
      control === null ||
      typeof control !== "object" ||
      control.generation !== this.generation
    ) {
      return;
    }
    switch (control.type) {
      case "confirm":
        this.confirm(control);
        break;
      case "credit":
        this.addCredit(control);
        break;
      case "seal":
        this.seal(control);
        break;
      case "take-fallback":
        this.takeFallback(control);
        break;
      case "stop":
        this.stop(control);
        break;
      default:
        this.failClosed();
    }
  }

  process(inputs) {
    if (this.state === "stopped") return false;
    if (this.state === "sealing") {
      return true;
    }
    if (
      !Number.isSafeInteger(currentFrame) ||
      currentFrame < 0
    ) {
      this.failClosed();
      return false;
    }
    const input = inputs[0]?.[0];
    if (!(input instanceof Float32Array) || input.length === 0) {
      return true;
    }

    if (this.carryContextFrame === undefined) {
      this.carryContextFrame = currentFrame;
    } else {
      const expectedInputContextFrame =
        this.carryContextFrame + this.carry.length;
      if (
        !Number.isSafeInteger(expectedInputContextFrame) ||
        currentFrame < expectedInputContextFrame
      ) {
        this.failClosed();
        return false;
      }
      if (currentFrame > expectedInputContextFrame) {
        this.resetResampler(currentFrame);
      }
    }

    const combinedStartContextFrame = this.carryContextFrame;
    const combined = new Float32Array(this.carry.length + input.length);
    combined.set(this.carry);
    combined.set(input, this.carry.length);
    zeroizeFloats(this.carry);

    let positionNumerator = this.positionNumerator;
    while (
      this.state !== "stopped" &&
      positionNumerator + sampleRate <=
        combined.length * OUTPUT_SAMPLE_RATE_HZ
    ) {
      const position =
        positionNumerator / OUTPUT_SAMPLE_RATE_HZ;
      const end =
        (positionNumerator + sampleRate) /
        OUTPUT_SAMPLE_RATE_HZ;
      let cursor = position;
      let weighted = 0;
      while (cursor < end) {
        const index = Math.floor(cursor);
        const boundary = Math.min(end, index + 1);
        const weight = boundary - cursor;
        weighted += combined[index] * weight;
        cursor = boundary;
      }
      this.appendSample(
        weighted / this.ratio,
        combinedStartContextFrame + position,
      );
      positionNumerator += sampleRate;
    }

    if (this.state === "stopped") {
      zeroizeFloats(combined);
      return false;
    }
    const consumed = Math.min(
      Math.floor(positionNumerator / OUTPUT_SAMPLE_RATE_HZ),
      combined.length,
    );
    const nextCarry = combined.slice(consumed);
    zeroizeFloats(combined);
    this.carry = nextCarry;
    this.carryContextFrame =
      combinedStartContextFrame + consumed;
    this.positionNumerator =
      positionNumerator - consumed * OUTPUT_SAMPLE_RATE_HZ;
    return true;
  }
}

registerProcessor("kotae-pcm-capture", KotaePcmCaptureProcessor);
