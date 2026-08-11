await import("/firebase-bridge.js");

const {
  default: init,
  advanceTemporalVadClock,
  classifyInterruptFrame,
} = await import("/wasm/kotae_client.js");
const { installTemporalVadClockAdvancer } = await import(
  "/temporal-vad-clock.mjs"
);
const { installInterruptFrameClassifier } = await import(
  "/voice-stream-policy.mjs"
);
installInterruptFrameClassifier(classifyInterruptFrame);
installTemporalVadClockAdvancer(advanceTemporalVadClock);
await init();
