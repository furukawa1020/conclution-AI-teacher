await import("/firebase-bridge.js");

const { default: init } = await import("/wasm/kotae_client.js");
await init();
