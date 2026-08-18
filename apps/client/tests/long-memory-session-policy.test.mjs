import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  createLongMemorySessionController,
  LONG_MEMORY_SESSION_STATES,
} from "../web/long-memory-session-policy.mjs";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, reject, resolve };
}

function jsonResponse(value, ok = true) {
  return Object.freeze({
    headers: Object.freeze({ get: () => "application/json; charset=utf-8" }),
    ok,
    text: async () => JSON.stringify(value),
  });
}

const credentials = Object.freeze({
  appCheckToken: "app-check-token",
  beginEndpoint: "https://api.example/context:begin",
  consumeEndpoint: "https://api.example/context:consume",
  guest: false,
  idToken: "firebase-id-token",
  voiceGeneration: 7,
});

test("beginとconsumeが遅くてもstartは待たず、同一世代は一度だけ準備する", async () => {
  const begin = deferred();
  const consume = deferred();
  const requests = [];
  const controller = createLongMemorySessionController({
    request: (url, options) => {
      requests.push({ options, url });
      return requests.length === 1 ? begin.promise : consume.promise;
    },
    setTimer: () => 1,
  });

  assert.deepEqual(controller.start(credentials), {
    generation: 7,
    state: LONG_MEMORY_SESSION_STATES.PREPARING,
  });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].url, credentials.beginEndpoint);
  assert.equal(requests[0].options.body, undefined);

  begin.resolve(jsonResponse({ available: true, capability: "kmc1.opaque" }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(requests.length, 2);
  assert.deepEqual(JSON.parse(requests[1].options.body), {
    capability: "kmc1.opaque",
  });
  consume.resolve(
    jsonResponse({ expiresInSeconds: 900, sessionContext: "kms1.opaque" }),
  );
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(controller.snapshot(), {
    generation: 7,
    state: LONG_MEMORY_SESSION_STATES.READY,
  });
  assert.doesNotMatch(JSON.stringify(controller.snapshot()), /km[cs]1\./u);
  controller.start(credentials);
  assert.equal(requests.length, 2);
});

test("available=falseならconsumeせず有限状態で終了する", async () => {
  let calls = 0;
  const controller = createLongMemorySessionController({
    request: async () => {
      calls += 1;
      return jsonResponse({ available: false });
    },
  });
  controller.start(credentials);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls, 1);
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.UNAVAILABLE);
});

test("ゲストは長期メモリAPIを一度も呼ばない", async () => {
  let calls = 0;
  const controller = createLongMemorySessionController({
    request: async () => {
      calls += 1;
      throw new Error("must not run");
    },
  });
  const state = controller.start({
    ...credentials,
    appCheckToken: undefined,
    guest: true,
    idToken: undefined,
  });
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls, 0);
  assert.equal(state.state, LONG_MEMORY_SESSION_STATES.UNAVAILABLE);
});

test("世代更新とclearは遅れて完了した秘密を復活させない", async () => {
  const stale = deferred();
  const signals = [];
  let calls = 0;
  const controller = createLongMemorySessionController({
    request: (_url, options) => {
      calls += 1;
      signals.push(options.signal);
      if (calls === 1) return stale.promise;
      return Promise.resolve(jsonResponse({ available: false }));
    },
  });
  controller.start(credentials);
  controller.start({ ...credentials, voiceGeneration: 8 });
  assert.equal(signals[0].aborted, true);
  stale.resolve(jsonResponse({ available: true, capability: "kmc1.stale" }));
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(controller.snapshot(), {
    generation: 8,
    state: LONG_MEMORY_SESSION_STATES.UNAVAILABLE,
  });

  controller.clear();
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.CANCELLED);
  assert.doesNotMatch(JSON.stringify(controller), /km[cs]1\./u);
});

test("不正応答と通信失敗は音声へthrowせずfailedへ閉じる", async () => {
  for (const request of [
    async () => jsonResponse({ available: true, capability: "broken" }),
    async () => {
      throw new Error("secret provider detail");
    },
  ]) {
    const controller = createLongMemorySessionController({ request });
    assert.doesNotThrow(() => controller.start(credentials));
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.FAILED);
  }
});

test("900秒でsession context参照を期限切れにする", async () => {
  let now = 10_000;
  let expiry;
  const responses = [
    jsonResponse({ available: true, capability: "kmc1.valid" }),
    jsonResponse({ expiresInSeconds: 900, sessionContext: "kms1.valid" }),
  ];
  const controller = createLongMemorySessionController({
    now: () => now,
    request: async () => responses.shift(),
    setTimer: (callback) => {
      expiry = callback;
      return 1;
    },
  });
  controller.start(credentials);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.READY);
  now += 899_000;
  expiry();
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.READY);
  now += 1_000;
  expiry();
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.EXPIRED);
});

test("準備済みcontextは同じ音声世代だけへ束縛し期限後は渡さない", async () => {
  let now = 10_000;
  const responses = [
    jsonResponse({ available: true, capability: "kmc1.valid" }),
    jsonResponse({ expiresInSeconds: 900, sessionContext: "kms1.valid" }),
  ];
  const controller = createLongMemorySessionController({
    now: () => now,
    request: async () => responses.shift(),
    setTimer: () => 1,
  });
  controller.start({ ...credentials, voiceGeneration: 7 });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(controller.voiceBinding(7), {
    sessionContext: "kms1.valid",
  });
  assert.equal(controller.voiceBinding(6), undefined);
  now += 900_000;
  assert.equal(controller.voiceBinding(7), undefined);
  assert.equal(controller.snapshot().state, LONG_MEMORY_SESSION_STATES.EXPIRED);
});

test("bridgeは認証後に非awaitで開始し、停止とpagehideで破棄する", async () => {
  const [bridge, browserGate] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(
      new URL("../../../scripts/test-browser-audio.mjs", import.meta.url),
      "utf8",
    ),
  ]);
  assert.match(bridge, /from "\.\/long-memory-session-policy\.mjs";/u);
  assert.match(
    bridge,
    /const credentials = await secureCredentials\(true\);[\s\S]*?longMemorySession\.start\([\s\S]*?const stream = await ensureMediaStream/u,
  );
  assert.doesNotMatch(bridge, /await\s+longMemorySession\.start/u);
  assert.match(bridge, /guest: guestModeActive/u);
  assert.match(bridge, /function stopSession[\s\S]*?longMemorySession\.clear\(\);/u);
  assert.match(bridge, /addEventListener\("pagehide"[\s\S]*?longMemorySession\.clear\(\);/u);
  assert.match(bridge, /longMemorySession\.voiceBinding\(expectedEpoch\)/u);
  assert.match(bridge, /recording\.sessionContext = undefined/u);
  assert.doesNotMatch(
    bridge,
    /(?:console\.|dispatchEvent|CustomEvent|localStorage|sessionStorage)[^\n]*sessionContext/iu,
  );
  const policy = await readFile(
    new URL("../web/long-memory-session-policy.mjs", import.meta.url),
    "utf8",
  );
  assert.doesNotMatch(
    policy,
    /(?:localStorage|sessionStorage|dispatchEvent|CustomEvent|console\.)/u,
  );
  assert.match(browserGate, /"long-memory-session-policy\.mjs"/u);
});
