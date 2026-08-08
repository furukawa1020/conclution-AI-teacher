const OUTPUT_SAMPLE_RATE_HZ = 16_000;
const FRAME_SAMPLES = 320;
const FRAME_BYTES = FRAME_SAMPLES * 2;
// 125 x 20 ms = a finite 2.5 s total: the 2.4 s candidate window plus
// 100 ms pre-roll. This is a hard 80 KB PCM16 ceiling, not an open-ended recorder.
const MAXIMUM_PRE_CONFIRM_FRAMES = 125;
const MAXIMUM_QUEUED_FRAMES = 200;
const CONTROL_VERSION = 1;

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
    this.contextFramesPerOutputFrame = sampleRate / 50;

    this.ratio = sampleRate / OUTPUT_SAMPLE_RATE_HZ;
    this.positionNumerator = 0;
    this.carry = new Float32Array(0);
    this.carryContextFrame = undefined;

    this.frame = new ArrayBuffer(FRAME_BYTES);
    this.frameView = new DataView(this.frame);
    this.frameOffset = 0;
    this.frameContextFrame = undefined;

    this.preConfirmRing = new Array(this.maximumPreConfirmFrames).fill(null);
    this.preConfirmHead = 0;
    this.preConfirmCount = 0;

    this.confirmedQueue = new Array(this.maximumQueuedFrames).fill(null);
    this.confirmedHead = 0;
    this.confirmedCount = 0;
    this.credit = 0;
    this.sequence = 0;
    this.confirmedCutoffContextFrame = undefined;

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
    for (let index = 0; index < this.preConfirmRing.length; index += 1) {
      const entry = this.preConfirmRing[index];
      if (entry !== null) {
        zeroizeBuffer(entry.pcm);
        this.preConfirmRing[index] = null;
      }
    }
    this.preConfirmHead = 0;
    this.preConfirmCount = 0;
  }

  clearConfirmedQueue() {
    for (let index = 0; index < this.confirmedQueue.length; index += 1) {
      const entry = this.confirmedQueue[index];
      if (entry !== null) {
        zeroizeBuffer(entry.pcm);
        this.confirmedQueue[index] = null;
      }
    }
    this.confirmedHead = 0;
    this.confirmedCount = 0;
  }

  clearPrivateAudio() {
    this.clearPreConfirmRing();
    this.clearConfirmedQueue();
    zeroizeFloats(this.carry);
    this.carry = new Float32Array(0);
    this.carryContextFrame = undefined;
    this.positionNumerator = 0;
    this.zeroizePartialFrame(false);
    this.credit = 0;
  }

  failClosed() {
    if (this.state === "stopped") return;
    this.clearPrivateAudio();
    this.state = "stopped";
  }

  failOverflow(extraEntry) {
    if (this.state === "stopped") {
      if (extraEntry !== undefined) zeroizeBuffer(extraEntry.pcm);
      return;
    }
    if (extraEntry !== undefined) zeroizeBuffer(extraEntry.pcm);
    this.clearPrivateAudio();
    this.state = "stopped";
    if (this.errorPosted) return;
    this.errorPosted = true;
    try {
      this.port.postMessage(Object.freeze({
        type: "error",
        version: CONTROL_VERSION,
        generation: this.generation,
        code: "capture_overflow",
      }));
    } catch {
      // The processor is already fail-closed and contains no retained audio.
    }
  }

  pushPreConfirm(entry) {
    if (this.preConfirmCount === this.maximumPreConfirmFrames) {
      const evicted = this.preConfirmRing[this.preConfirmHead];
      if (evicted !== null) zeroizeBuffer(evicted.pcm);
      this.preConfirmRing[this.preConfirmHead] = entry;
      this.preConfirmHead =
        (this.preConfirmHead + 1) % this.maximumPreConfirmFrames;
      return;
    }
    const index =
      (this.preConfirmHead + this.preConfirmCount) %
      this.maximumPreConfirmFrames;
    this.preConfirmRing[index] = entry;
    this.preConfirmCount += 1;
  }

  shiftPreConfirm() {
    if (this.preConfirmCount === 0) return undefined;
    const entry = this.preConfirmRing[this.preConfirmHead];
    this.preConfirmRing[this.preConfirmHead] = null;
    this.preConfirmHead =
      (this.preConfirmHead + 1) % this.maximumPreConfirmFrames;
    this.preConfirmCount -= 1;
    if (this.preConfirmCount === 0) this.preConfirmHead = 0;
    return entry ?? undefined;
  }

  enqueueConfirmed(entry) {
    if (
      this.state !== "confirmed" &&
      this.state !== "sealing"
    ) {
      zeroizeBuffer(entry.pcm);
      return;
    }
    if (this.confirmedCount === this.maximumQueuedFrames) {
      this.failOverflow(entry);
      return;
    }
    const index =
      (this.confirmedHead + this.confirmedCount) %
      this.maximumQueuedFrames;
    this.confirmedQueue[index] = entry;
    this.confirmedCount += 1;
    this.flushConfirmed();
  }

  shiftConfirmed() {
    if (this.confirmedCount === 0) return undefined;
    const entry = this.confirmedQueue[this.confirmedHead];
    this.confirmedQueue[this.confirmedHead] = null;
    this.confirmedHead =
      (this.confirmedHead + 1) % this.maximumQueuedFrames;
    this.confirmedCount -= 1;
    if (this.confirmedCount === 0) this.confirmedHead = 0;
    return entry ?? undefined;
  }

  flushConfirmed() {
    while (
      this.credit > 0 &&
      this.confirmedCount > 0 &&
      this.state !== "stopped"
    ) {
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
    if (
      this.state !== "sealing" ||
      this.confirmedCount !== 0 ||
      this.sealedPosted
    ) {
      return;
    }
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
    this.state = "stopped";
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
      control.initialCredit > this.maximumQueuedFrames
    ) {
      this.failClosed();
      return;
    }

    this.confirmedCutoffContextFrame =
      control.candidateContextFrame -
      control.leadInFrames * this.contextFramesPerOutputFrame;
    this.credit = control.initialCredit;
    this.state = "confirmed";

    while (this.preConfirmCount > 0 && this.state === "confirmed") {
      const entry = this.shiftPreConfirm();
      if (entry === undefined) {
        this.failClosed();
        return;
      }
      if (entry.contextFrame < this.confirmedCutoffContextFrame) {
        zeroizeBuffer(entry.pcm);
      } else {
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
