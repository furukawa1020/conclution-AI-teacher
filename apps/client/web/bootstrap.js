await import("/firebase-bridge.js");

const { default: init, classifyInterruptFrame } = await import("/wasm/kotae_client.js");
const { installInterruptFrameClassifier } = await import(
  "/voice-stream-policy.mjs"
);
installInterruptFrameClassifier(classifyInterruptFrame);
await init();
