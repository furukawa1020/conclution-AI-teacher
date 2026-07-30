const OUTPUT_SAMPLE_RATE_HZ = 16_000;
const FRAME_SAMPLES = 320;
const FRAME_BYTES = FRAME_SAMPLES * 2;

class KotaePcmCaptureProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    if (
      !Number.isFinite(sampleRate) ||
      sampleRate < OUTPUT_SAMPLE_RATE_HZ ||
      sampleRate > 192_000
    ) {
      throw new Error("unsupported_input_sample_rate");
    }
    this.ratio = sampleRate / OUTPUT_SAMPLE_RATE_HZ;
    this.position = 0;
    this.carry = new Float32Array(0);
    this.frame = new ArrayBuffer(FRAME_BYTES);
    this.frameView = new DataView(this.frame);
    this.frameOffset = 0;
    this.stopped = false;
    this.port.onmessage = ({ data }) => {
      if (
        data?.type === "stop" &&
        data.version === 1 &&
        Object.keys(data).length === 2
      ) {
        this.stopped = true;
      }
    };
  }

  appendSample(value) {
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

    const completed = this.frame;
    this.frame = new ArrayBuffer(FRAME_BYTES);
    this.frameView = new DataView(this.frame);
    this.frameOffset = 0;
    this.port.postMessage(
      Object.freeze({
        pcm: completed,
        sampleRateHz: OUTPUT_SAMPLE_RATE_HZ,
        type: "frame",
        version: 1,
      }),
      [completed],
    );
  }

  process(inputs) {
    if (this.stopped) return false;
    const input = inputs[0]?.[0];
    if (!(input instanceof Float32Array) || input.length === 0) {
      return true;
    }

    const combined = new Float32Array(this.carry.length + input.length);
    combined.set(this.carry);
    combined.set(input, this.carry.length);
    let position = this.position;
    while (position + this.ratio <= combined.length) {
      const end = position + this.ratio;
      let cursor = position;
      let weighted = 0;
      while (cursor < end) {
        const index = Math.floor(cursor);
        const boundary = Math.min(end, index + 1);
        const weight = boundary - cursor;
        weighted += combined[index] * weight;
        cursor = boundary;
      }
      // A box low-pass precedes every downsampled output. Browsers that
      // honor the requested 16 kHz AudioContext take the ratio=1 fast path;
      // 44.1/48 kHz devices still avoid linear-resampler aliasing.
      this.appendSample(weighted / this.ratio);
      position = end;
    }

    const consumed = Math.min(
      Math.floor(position),
      combined.length,
    );
    this.carry = combined.slice(consumed);
    this.position = position - consumed;
    return true;
  }
}

registerProcessor("kotae-pcm-capture", KotaePcmCaptureProcessor);
