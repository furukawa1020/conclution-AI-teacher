await import("/firebase-bridge.js");

const {
  default: init,
  advanceIntentionalInterrupt,
  advanceTemporalVadClock,
  classifyInterruptFrame,
} = await import("/wasm/kotae_client.js");
const { installTemporalVadClockAdvancer } = await import(
  "/temporal-vad-clock.mjs"
);
const {
  installIntentionalInterruptAdvancer,
  installInterruptFrameClassifier,
} = await import(
  "/voice-stream-policy.mjs"
);
installInterruptFrameClassifier(classifyInterruptFrame);
installIntentionalInterruptAdvancer(advanceIntentionalInterrupt);
installTemporalVadClockAdvancer(advanceTemporalVadClock);
await init();
