// Content-free, fixed-vocabulary timings only. Never accept descriptions or IDs.
export function parseGuestTrialServerTiming(header) {
  if (typeof header !== "string" || header.length === 0 || header.length > 128) return null;
  const values = {};
  for (const part of header.split(",")) {
    const match = /^(asr|verify|pipeline);dur=(0|[1-9][0-9]{0,4})$/u.exec(part.trim());
    if (!match || Object.hasOwn(values, match[1]) || Number(match[2]) > 60000) return null;
    values[match[1]] = Number(match[2]);
  }
  return Object.freeze(values);
}
