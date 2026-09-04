export const VOICE_LATENCY_TRACE_VERSION = "kotae.voice-latency-trace.v1";

export const VOICE_LATENCY_TRANSPORTS = Object.freeze({
  HTTP_BUFFERED: "http-buffered",
  HTTP_STREAM: "http-stream",
  NATIVE_LIVE: "native-live",
});

export const VOICE_LATENCY_DEVICE_CLASSES = Object.freeze({
  DESKTOP: "desktop",
  MOBILE: "mobile",
  UNKNOWN: "unknown",
});

export const VOICE_LATENCY_NETWORK_CLASSES = Object.freeze({
  CONSTRAINED: "constrained",
  FAST: "fast",
  TYPICAL: "typical",
  UNKNOWN: "unknown",
});

const MAX_DURATION_MS = 120_000;
const MAX_MANIFEST_BYTES = 64 * 1024;
const REVISION_PATTERN = /^[0-9a-f]{40}$/u;
const ROUTES = new Set([
  "continuing-coach",
  "http-fallback",
  "initial-answer-support",
  "native-conversation",
  "strict-local",
]);
const TRANSPORTS = new Set(Object.values(VOICE_LATENCY_TRANSPORTS));

function duration(from, to) {
  const value = Math.round(to - from);
  if (!Number.isSafeInteger(value) || value < 0 || value > MAX_DURATION_MS) {
    throw new TypeError("voice_latency_trace_invalid");
  }
  return value;
}

function nullableDuration(value) {
  if (value === null || value === undefined) return null;
  if (!Number.isSafeInteger(value) || value < 0 || value > MAX_DURATION_MS) {
    throw new TypeError("voice_latency_trace_invalid");
  }
  return value;
}

export function classifyVoiceLatencyDevice({ coarsePointer, touchPoints } = {}) {
  if (typeof coarsePointer !== "boolean" || !Number.isSafeInteger(touchPoints) || touchPoints < 0) {
    return VOICE_LATENCY_DEVICE_CLASSES.UNKNOWN;
  }
  return coarsePointer && touchPoints > 0
    ? VOICE_LATENCY_DEVICE_CLASSES.MOBILE
    : VOICE_LATENCY_DEVICE_CLASSES.DESKTOP;
}

export function classifyVoiceLatencyNetwork(effectiveType) {
  if (effectiveType === "4g") return VOICE_LATENCY_NETWORK_CLASSES.FAST;
  if (effectiveType === "3g") return VOICE_LATENCY_NETWORK_CLASSES.TYPICAL;
  if (effectiveType === "2g" || effectiveType === "slow-2g") {
    return VOICE_LATENCY_NETWORK_CLASSES.CONSTRAINED;
  }
  return VOICE_LATENCY_NETWORK_CLASSES.UNKNOWN;
}

export async function loadVoiceLatencyRevision(
  fetcher,
  url = "/.kotae-release-manifest.json",
) {
  if (typeof fetcher !== "function" || typeof url !== "string" || !url.startsWith("/")) {
    throw new TypeError("voice_latency_revision_invalid");
  }
  const response = await fetcher(url, {
    cache: "no-store",
    credentials: "omit",
    redirect: "error",
    referrerPolicy: "no-referrer",
  });
  if (!response?.ok || typeof response.text !== "function") {
    throw new TypeError("voice_latency_revision_invalid");
  }
  const text = await response.text();
  if (typeof text !== "string" || new TextEncoder().encode(text).byteLength > MAX_MANIFEST_BYTES) {
    throw new TypeError("voice_latency_revision_invalid");
  }
  let manifest;
  try {
    manifest = JSON.parse(text);
  } catch {
    throw new TypeError("voice_latency_revision_invalid");
  }
  if (
    !manifest ||
    typeof manifest !== "object" ||
    Array.isArray(manifest) ||
    manifest.schemaVersion !== 2 ||
    !REVISION_PATTERN.test(manifest.sourceCommit)
  ) {
    throw new TypeError("voice_latency_revision_invalid");
  }
  return manifest.sourceCommit;
}

export function buildVoiceLatencyTrace({
  commitAcknowledgedAt,
  commitSentAt,
  deviceClass,
  firstBinaryAt,
  gestureToListeningMs,
  networkClass,
  revision,
  route,
  serverStages = {},
  speakerWriteAt,
  speechEndedAt,
  transport,
}) {
  if (
    !REVISION_PATTERN.test(revision) ||
    !TRANSPORTS.has(transport) ||
    !ROUTES.has(route) ||
    !Object.values(VOICE_LATENCY_DEVICE_CLASSES).includes(deviceClass) ||
    !Object.values(VOICE_LATENCY_NETWORK_CLASSES).includes(networkClass) ||
    !Number.isSafeInteger(gestureToListeningMs) ||
    gestureToListeningMs < 0 ||
    gestureToListeningMs > MAX_DURATION_MS
  ) {
    throw new TypeError("voice_latency_trace_invalid");
  }

  const speechEndToCommitSendMs = duration(speechEndedAt, commitSentAt);
  const commitSendToAckMs = duration(commitSentAt, commitAcknowledgedAt);
  const commitAckToFirstBinaryMs = duration(commitAcknowledgedAt, firstBinaryAt);
  const firstBinaryToSpeakerWriteMs = duration(firstBinaryAt, speakerWriteAt);
  const speechEndToSpeakerWriteMs =
    speechEndToCommitSendMs +
    commitSendToAckMs +
    commitAckToFirstBinaryMs +
    firstBinaryToSpeakerWriteMs;

  return Object.freeze({
    schemaVersion: VOICE_LATENCY_TRACE_VERSION,
    revision,
    transport,
    route,
    deviceClass,
    networkClass,
    gestureToListeningMs,
    speechEndToSpeakerWriteMs,
    stages: Object.freeze({
      speechEndToCommitSendMs,
      commitSendToAckMs,
      commitAckToFirstBinaryMs,
      firstBinaryToSpeakerWriteMs,
      serverCommitToDrainMs: nullableDuration(serverStages.serverCommitToDrainMs),
      serverDrainToActivityEndMs: nullableDuration(serverStages.serverDrainToActivityEndMs),
      activityEndToFinalMs: nullableDuration(serverStages.activityEndToFinalMs),
      finalToControlCommitMs: nullableDuration(serverStages.finalToControlCommitMs),
      controlCommitToFirstPcmMs: nullableDuration(serverStages.controlCommitToFirstPcmMs),
    }),
  });
}
