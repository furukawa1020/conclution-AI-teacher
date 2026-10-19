import assert from "node:assert/strict";
import test from "node:test";
import { createGuestTrialController, validateGuestTrial } from "../web/guest-trial-controller.mjs";

const NOW = Date.parse("2026-10-19T00:00:00Z");
function trial(overrides = {}) {
  return { version: 1, id: "a".repeat(64), questionId: "rest_activity_v1",
    question: "少し休めるとしたら、何をしたいですか？", generation: 1, assisted: false,
    status: "pending", expiresAt: "2026-10-19T00:15:00Z", ...overrides };
}
function make(send, discardPrevious = async () => {}) {
  return createGuestTrialController({ send, discardPrevious, now: () => NOW, timeoutMs: 100 });
}

test("trial schema rejects altered questions, extra content, expiry and inconsistent generations", () => {
  assert.ok(Object.isFrozen(validateGuestTrial(trial(), NOW)));
  for (const bad of [trial({question: "別の質問"}), trial({generation: 2}), trial({caption: "私の答え"}),
    trial({expiresAt: "2026-10-19T00:00:00Z"}), trial({expiresAt: "2026-10-20T00:00:00Z"}),
    trial({id: "../other"}), trial({assisted: "false"}), trial({status: "improved"})]) {
    assert.throws(() => validateGuestTrial(bad, NOW), /guest_trial_invalid/u);
  }
});

test("previous audio disposal finishes before begin; help and stop retain provenance", async () => {
  const order = [];
  const c = make(async payload => {
    order.push(payload.action);
    if (payload.action === "begin") return trial();
    if (payload.action === "help") return trial({assisted: true, generation: 2});
    return trial({assisted: true, generation: 3, status: "stopped"});
  }, async () => { order.push("disposed"); });
  await c.begin(); await c.help(); await c.stop();
  assert.deepEqual(order, ["disposed", "begin", "help", "stop"]);
  assert.equal(c.state().phase, "ended");
  assert.equal(c.state().trial.assisted, true);
  await assert.rejects(c.help(), /reconcile/u);
});

test("lost help response requires reconciliation and cannot restore an unassisted state", async () => {
  const c = make(async ({action}) => {
    if (action === "begin") return trial();
    if (action === "help") throw new Error("network");
    return trial({assisted: true, generation: 2});
  });
  await c.begin(); await assert.rejects(c.help(), /network/u);
  assert.equal(c.state().phase, "uncertain");
  await assert.rejects(c.stop(), /reconcile/u);
  await c.read(); assert.equal(c.state().trial.assisted, true);
});

test("reset invalidates late responses without disturbing the replacement request", async () => {
  let late;
  let calls = 0;
  const c = make(() => ++calls === 1 ? new Promise(resolve => {late = resolve;}) : trial({id:"b".repeat(64)}));
  const first = c.begin();
  while (!late) await Promise.resolve();
  await assert.rejects(c.begin(), /busy/u);
  c.reset();
  const failure = assert.rejects(first, /cancelled/u);
  await c.begin(); late(trial()); await failure;
  assert.equal(c.state().trial.id, "b".repeat(64));
});

test("replacement begin waits for non-cooperative old cleanup", async () => {
  let release;
  let cleanupCalls = 0;
  let sent = 0;
  const c = make(() => { sent++; return trial(); }, () => ++cleanupCalls === 1 ? new Promise(resolve => {release = resolve;}) : undefined);
  const first = c.begin();
  while (!release) await Promise.resolve();
  c.reset();
  const rejected = assert.rejects(first, /cancelled/u);
  const second = c.begin();
  await Promise.resolve(); await Promise.resolve();
  assert.equal(sent, 0);
  release(); await rejected; await second;
  assert.equal(sent, 1);
});

test("failed disposal never sends begin; non-cooperative transport times out", async () => {
  let sent = 0;
  const c = make(() => { sent++; return trial(); }, async () => {throw new Error("cleanup");});
  await assert.rejects(c.begin(), /cleanup/u); assert.equal(sent, 0);
  const stuck = createGuestTrialController({send: () => new Promise(() => {}), discardPrevious: async () => {}, now: () => NOW, timeoutMs: 5});
  await assert.rejects(stuck.begin(), /cancelled/u);
  assert.equal(stuck.state().phase, "uncertain");
});

test("read cannot relabel the question or roll back assistance", async () => {
  let reply = trial();
  const c = make(async () => reply);
  await c.begin(); reply = trial({assisted:true,generation:2}); await c.help();
  reply = trial(); await assert.rejects(c.read(), /invalid/u);
  assert.equal(c.state().trial.assisted, true);
});
