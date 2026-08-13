await import("/firebase-bridge.js");

const {
  default: init,
  advanceIntentionalInterrupt,
  advanceTemporalVadClock,
  classifyInterruptFrame,
  classifyOnsetFrame,
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
installIntentionalInterruptAdvancer(advanceIntentionalInterrupt);
installTemporalVadClockAdvancer(advanceTemporalVadClock);
await init();
