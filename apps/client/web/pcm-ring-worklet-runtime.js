import {
  initSync,
  PcmRing,
} from "/wasm/kotae_pcm_ring.js";

let initialized = false;
const MAXIMUM_CAPACITY = 200;

function hasRingContract(value) {
  return Boolean(
    value &&
      typeof value.capacity === "function" &&
      typeof value.clear === "function" &&
      typeof value.count === "function" &&
      typeof value.free === "function" &&
      typeof value.push === "function" &&
      typeof value.shiftInto === "function",
  );
}

export function createPcmRing(module, capacity, overwriteOldest) {
  if (!(module instanceof WebAssembly.Module)) {
    throw new Error("invalid_pcm_ring_module");
  }
  if (
    !Number.isSafeInteger(capacity) ||
    capacity <= 0 ||
    capacity > MAXIMUM_CAPACITY ||
    typeof overwriteOldest !== "boolean"
  ) {
    throw new Error("invalid_pcm_ring_contract");
  }
  if (!initialized) {
    initSync({ module });
    initialized = true;
  }
  const ring = new PcmRing(capacity, overwriteOldest);
  if (
    !hasRingContract(ring) ||
    ring.capacity() !== capacity ||
    ring.count() !== 0
  ) {
    try {
      ring?.free?.();
    } catch {
      // The constructor boundary is already fail-closed.
    }
    throw new Error("invalid_pcm_ring_contract");
  }
  return ring;
}
