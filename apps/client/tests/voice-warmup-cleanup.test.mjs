import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";

test("successful warmup cancels the unused body and clears its deadline", async () => {
  const bridge = readFileSync(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const start = bridge.indexOf("function warmVoiceService()");
  const end = bridge.indexOf("// Establish DNS/TLS", start);
  assert.ok(start >= 0 && end > start);
  let cancelled = 0;
  const cleared = [];
  const warm = runInNewContext(`let voiceServiceWarmupStarted=false;
    ${bridge.slice(start, end)}; warmVoiceService`, {
    AbortController,
    primeVoiceTransportConnection() {},
    VOICE_ORIGIN: "https://voice.example",
    setTimeout: () => 7,
    clearTimeout: id => cleared.push(id),
    fetch: async () => ({ body: { cancel: async () => { cancelled++; } } }),
  });
  warm();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(cancelled, 1);
  assert.deepEqual(cleared, [7]);
  warm();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(cancelled, 1);
});
