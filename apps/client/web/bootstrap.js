const { installQuietEvidenceTrackerFactory } = await import("/firebase-bridge.js");

const {
  default: init,
  advanceIntentionalInterrupt,
  advanceTemporalVadClock,
  classifyInterruptFrame,
  classifyOnsetFrame,
  createQuietEvidenceTracker,
} = await import("/wasm/kotae_client.js");
const { installTemporalVadClockAdvancer } = await import(
  "/temporal-vad-clock.mjs"
);
const { installOnsetFrameClassifier } = await import(
  "/voice-session-policy.mjs"
);
const {
  installIntentionalInterruptAdvancer,
  installInterruptFrameClassifier,
} = await import(
  "/voice-stream-policy.mjs"
);
installInterruptFrameClassifier(classifyInterruptFrame);
installOnsetFrameClassifier(classifyOnsetFrame);
installQuietEvidenceTrackerFactory(createQuietEvidenceTracker);
installIntentionalInterruptAdvancer(advanceIntentionalInterrupt);
installTemporalVadClockAdvancer(advanceTemporalVadClock);
await init();
