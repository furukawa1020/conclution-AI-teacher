import {
  initSync,
  PcmRing,
  intentionalFastLaneSelfTest,
  temporalVadClockSelfTest,
} from "/wasm/kotae_pcm_ring.js";

let initialized = false;
const MAXIMUM_CAPACITY = 10_500;
const PCM_RING_FRAME_BYTES = 640;
const RING_PUSH_INVALID = 0;
const RING_SHIFT_INVALID = -2;
const RING_QUIET_COMPENSATION_INVALID = 0;

function hasRingContract(value) {
  return Boolean(
    value &&
      typeof value.capacity === "function" &&
      typeof value.clear === "function" &&
      typeof value.compensateQuietFrame === "function" &&
      typeof value.count === "function" &&
      typeof value.free === "function" &&
      typeof value.generation === "function" &&
      typeof value.push === "function" &&
      typeof value.shiftInto === "function",
  );
}

function verifyGenerationIsolation(ring, generation) {
  const staleGeneration =
    generation === Number.MAX_SAFE_INTEGER
      ? generation - 1
      : generation + 1;
  const probe = new Uint8Array(PCM_RING_FRAME_BYTES);
  const destination = new Uint8Array(PCM_RING_FRAME_BYTES);
  probe.fill(0xa5);
  destination.fill(0x3c);
  try {
    const stalePush = ring.push(staleGeneration, 0, probe);
    const staleCompensation = ring.compensateQuietFrame(
      staleGeneration,
      probe,
    );
    const staleCount = ring.count(staleGeneration);
    const staleShift = ring.shiftInto(staleGeneration, destination);
    const staleClear = ring.clear(staleGeneration);
    if (
      stalePush !== RING_PUSH_INVALID ||
      staleCompensation !== RING_QUIET_COMPENSATION_INVALID ||
      staleCount !== -1 ||
      staleShift !== RING_SHIFT_INVALID ||
      staleClear !== false ||
      destination.some((byte) => byte !== 0x3c) ||
      probe.some((byte) => byte !== 0xa5) ||
      ring.count(generation) !== 0
    ) {
      try {
        ring.clear(generation);
      } catch {
        // The caller frees this invalid ring below.
      }
      throw new Error("pcm_ring_generation_boundary_invalid");
    }
  } finally {
    probe.fill(0);
    destination.fill(0);
  }
}

export function createPcmRing(
  module,
  generation,
  capacity,
  overwriteOldest,
) {
  if (!(module instanceof WebAssembly.Module)) {
    throw new Error("invalid_pcm_ring_module");
  }
  if (
    !Number.isSafeInteger(generation) ||
    generation <= 0 ||
    !Number.isSafeInteger(capacity) ||
    capacity <= 0 ||
    capacity > MAXIMUM_CAPACITY ||
    typeof overwriteOldest !== "boolean"
  ) {
    throw new Error("invalid_pcm_ring_contract");
  }
  if (!initialized) {
    initSync({ module });
    if (temporalVadClockSelfTest() !== true) {
      throw new Error("temporal_vad_clock_self_test_failed");
    }
    if (intentionalFastLaneSelfTest() !== true) {
      throw new Error("intentional_fast_lane_self_test_failed");
    }
    initialized = true;
  }
  const ring = new PcmRing(generation, capacity, overwriteOldest);
  if (
    !hasRingContract(ring) ||
    ring.generation() !== generation ||
    ring.capacity() !== capacity ||
    ring.count(generation) !== 0
  ) {
    try {
      ring?.free?.();
    } catch {
      // The constructor boundary is already fail-closed.
    }
    throw new Error("invalid_pcm_ring_contract");
  }
  try {
    verifyGenerationIsolation(ring, generation);
  } catch (error) {
    try {
      ring.clear(generation);
      ring.free();
    } catch {
      // The generation boundary already failed closed.
    }
    throw error;
  }
  return ring;
}
