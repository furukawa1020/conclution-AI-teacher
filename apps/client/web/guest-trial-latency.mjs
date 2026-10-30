const STAGES = ["roundtrip", "asr", "verify", "pipeline"];
const valid = value => Number.isSafeInteger(value) && value >= 0 && value <= 60000;

// Most recent 100 successful responses only, memory-only, no request identifiers.
export function createGuestTrialLatency() {
  const samples = [];
  return Object.freeze({
    clear() { samples.length = 0; },
    observe(value) {
      if (!value || Object.keys(value).length !== 2 || !Object.hasOwn(value, "server") ||
          !valid(value.roundtripMs)) return false;
      const server = value.server;
      if (server !== null && (typeof server !== "object" || Array.isArray(server) ||
          Object.keys(server).some(key => !STAGES.slice(1).includes(key) || !valid(server[key])))) return false;
      samples.push(Object.freeze({roundtrip:value.roundtripMs, ...(server ?? {})}));
      if (samples.length > 100) samples.shift();
      return true;
    },
    snapshot() {
      const stages = {};
      for (const stage of STAGES) {
        const values = samples.filter(sample => Object.hasOwn(sample, stage)).map(sample => sample[stage]).sort((a,b) => a-b);
        stages[stage] = Object.freeze({count:values.length,
          p50Ms:values.length ? values[Math.ceil(values.length * .5) - 1] : null,
          p95Ms:values.length ? values[Math.ceil(values.length * .95) - 1] : null});
      }
      return Object.freeze({scope:"successful_responses_only", retained:samples.length,
        capacity:100, stages:Object.freeze(stages)});
    },
  });
}
