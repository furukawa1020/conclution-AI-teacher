import assert from "node:assert/strict";
import test from "node:test";
import { createGuestTrialTransport } from "../web/guest-trial-controller.mjs";

const origin = "https://voice.example";
const endpoint = `${origin}/api/v1/guest-trials/control`;
const now = Date.parse("2026-10-20T00:00:00Z");
const snapshot = { version: 1, id: "a".repeat(64), questionId: "rest_activity_v1",
  question: "少し休めるとしたら、何をしたいですか？", generation: 1, assisted: false,
  status: "pending", expiresAt: "2026-10-20T00:15:00Z" };
const credentials = async () => ({ idToken: "id.secret", appCheckToken: "app.secret" });
function response(body = JSON.stringify(snapshot), status = 200) {
  const result = new Response(body, { status, headers: {"Content-Type": "application/json"} });
  Object.defineProperty(result, "url", { value: endpoint });
  return result;
}
function transport(overrides = {}) {
  return createGuestTrialTransport({ origin, credentials, isCurrentGuest: () => true,
    now: () => now, request: async () => response(), ...overrides });
}
function options() { return {signal: new AbortController().signal}; }

test("trial transport sends one bounded authenticated POST without redirects or cookies", async () => {
  let calls = 0;
  const send = transport({ request: async (url, init) => {
    calls++;
    assert.equal(url, endpoint);
    assert.equal(init.headers.Authorization, "Bearer id.secret");
    assert.equal(init.headers["X-Firebase-AppCheck"], "app.secret");
    assert.equal(init.credentials, "omit");
    assert.equal(init.redirect, "error");
    assert.equal(init.cache, "no-store");
    assert.equal(init.mode, "cors");
    assert.equal(init.referrerPolicy, "no-referrer");
    assert.equal(init.body, '{"action":"begin"}');
    return response();
  }});
  assert.deepEqual(await send({action:"begin"}, options()), snapshot);
  assert.equal(calls, 1);
});

test("expired authorization and control conflicts do not retry or disclose response contents", async () => {
  for (const status of [401, 403, 409, 410, 429, 503]) {
    let calls = 0;
    const send = transport({request: async () => { calls++; return response("private detail", status); }});
    await assert.rejects(send({action:"begin"}, options()), error =>
      error.message.startsWith("guest_trial_") && !error.message.includes("private"));
    assert.equal(calls, 1);
  }
});

test("wrong destinations, completion claims and invalid credentials fail before network", async () => {
  for (const destination of ["http://voice.example", `${origin}/`, `${origin}/path`, `${origin}?x`, "https://user:secret@voice.example"]) {
    assert.throws(() => transport({origin:destination}), /invalid/u);
  }
  let calls = 0;
  const request = async () => {calls++; return response();};
  for (const payload of [{action:"complete"}, {action:"begin", id:"a".repeat(64)}, {action:"help", id:"a".repeat(64), generation:3}]) {
    await assert.rejects(transport({request})(payload, options()), /invalid/u);
  }
  for (const token of ["", "secret\n", "é", "x".repeat(16385)]) {
    await assert.rejects(transport({request, credentials:async () => ({idToken:token, appCheckToken:"app"})})({action:"begin"}, options()), /authentication_failed/u);
  }
  assert.equal(calls, 0);
});

test("cancellation or account change while acquiring credentials cannot send a late mutation", async () => {
  for (const mode of ["cancel", "account"]) {
    let release;
    let current = true;
    let calls = 0;
    const controller = new AbortController();
    const send = transport({
      credentials: () => new Promise(resolve => {release = resolve;}),
      isCurrentGuest: () => current,
      request:async () => {calls++; return response();},
    });
    const task = send({action:"begin"}, {signal:controller.signal});
    const rejected = assert.rejects(task, /cancelled|session_changed/u);
    if (mode === "cancel") controller.abort(); else current = false;
    release(await credentials());
    await rejected;
    await Promise.resolve();
    assert.equal(calls, 0);
  }
});

test("response byte limit cancels streamed bodies without buffering arbitrary content", async () => {
  let cancelled = false;
  const body = new ReadableStream({
    start(c) { c.enqueue(new Uint8Array(2049)); },
    cancel() { cancelled = true; },
  });
  await assert.rejects(transport({request:async () => response(body)})({action:"begin"}, options()), /invalid/u);
  assert.equal(cancelled, true);
});

test("late fetch response is disposed after cancellation instead of restoring the trial", async () => {
  let deliver;
  let cancelled = false;
  const controller = new AbortController();
  const send = transport({ request: () => new Promise(resolve => {deliver = resolve;}) });
  const pending = send({action:"begin"}, {signal:controller.signal});
  const rejected = assert.rejects(pending, /cancelled/u);
  while (!deliver) await Promise.resolve();
  controller.abort();
  await rejected;
  deliver(response(new ReadableStream({cancel() {cancelled = true;}})));
  await Promise.resolve(); await Promise.resolve();
  assert.equal(cancelled, true);
});

test("stalled credentials and stalled response body time out; no mutation retry", async () => {
  await assert.rejects(transport({timeoutMs:5, credentials:() => new Promise(() => {})})({action:"begin"}, options()), /cancelled/u);
  let cancelled = false;
  const body = new ReadableStream({cancel() {cancelled = true;}});
  await assert.rejects(transport({timeoutMs:5, request:async () => response(body)})({action:"begin"}, options()), /cancelled/u);
  assert.equal(cancelled, true);
});

test("malformed bodies and fetch exceptions become content-free errors", async () => {
  for (const body of ["private body", JSON.stringify({...snapshot, caption:"private"}), new Uint8Array([0xff])]) {
    await assert.rejects(transport({request:async () => response(body)})({action:"begin"}, options()), /^Error: guest_trial_(invalid|unavailable)$/u);
  }
  await assert.rejects(transport({request:async () => {throw new Error("private URL?token=secret");}})({action:"begin"}, options()), /^Error: guest_trial_unavailable$/u);
});
