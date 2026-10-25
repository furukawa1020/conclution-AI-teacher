import assert from "node:assert/strict";
import test from "node:test";
import {readFile} from "node:fs/promises";
import {runInNewContext} from "node:vm";

const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const root = bridge.slice(bridge.indexOf("let guestTrialRoot;"), bridge.indexOf("let guestVoiceComparisonRequested"));
function setup(capture) {
  const auth = {currentUser: {uid: "guest-a", isAnonymous: true}};
  let options, resets = 0, submissions = 0, cancellations = 0;
  const trial = {question: "質問", status: "pending", state: "private", assisted: false, id: "private-id"};
  const context = {guestModeActive: true, guestAuthInstance: auth, document: {hidden: false},
    AbortController, VOICE_ORIGIN: "https://example.test", secureCredentials: async () => ({}),
    guestVoiceComparison: {clear() {}}, stopGuestVoiceComparisonPlayback() {},
    fail: code => {throw new Error(code);}, nextPcmCaptureGeneration: () => 1,
    createGuestTrialTransport: o => { options = o; }, createGuestTrialVoiceTransport: () => {},
    createGuestTrialController: () => ({state: () => ({phase: "active", trial}),
      reset: () => resets++, begin: async () => {}, stop: async () => {},
      submit: async () => {submissions++;}}),
    openGuestTrialCapture: capture ?? (async () => ({cancel: () => cancellations++, finish: async () => new Uint8Array([1,2])})),
    loadPcmCaptureWorklet() {},
  };
  const api = runInNewContext(`${root}\nfunction stopSession(){invalidateGuestTrial();}\n({guestTrialAction,invalidateGuestTrial})`, context);
  return {...api, auth, options: () => options, counts: () => ({resets, submissions, cancellations})};
}

test("browser root binds transport to the guest and exposes no capability or trial ID", async () => {
  const env = setup(); const raw = await env.guestTrialAction("begin");
  assert.doesNotMatch(raw, /private/u);
  assert.equal(env.options().isCurrentGuest(), true);
  const epoch = env.options().getSessionEpoch();
  env.auth.currentUser = {uid: "guest-b", isAnonymous: true};
  assert.equal(env.options().isCurrentGuest(), false);
  env.invalidateGuestTrial(); assert.ok(env.options().getSessionEpoch() > epoch);
});

test("late microphone setup is cancelled after an identity boundary", async () => {
  let resolve, cancelled = 0;
  const env = setup(() => new Promise(r => {resolve = r;}));
  await env.guestTrialAction("begin"); const recording = env.guestTrialAction("record");
  env.invalidateGuestTrial(); resolve({cancel: () => cancelled++});
  await assert.rejects(recording, /cancelled/u); assert.equal(cancelled, 1);
  assert.equal(JSON.parse(await env.guestTrialAction("state")).phase, "idle");
});

test("late finished PCM is erased and never submitted after reset", async () => {
  let resolve;
  const env = setup(async () => ({cancel() {}, finish: () => new Promise(r => {resolve = r;})}));
  await env.guestTrialAction("begin"); await env.guestTrialAction("record");
  const sending = env.guestTrialAction("submit"); env.invalidateGuestTrial();
  const pcm = new Uint8Array([1,2]); resolve(pcm);
  await assert.rejects(sending, /cancelled/u);
  assert.ok(pcm.every(x => x === 0)); assert.equal(env.counts().submissions, 0);
});

test("trial participates in lifecycle and identity cancellation", () => {
  assert.match(bridge.slice(bridge.indexOf("function hasActiveVoiceSession()")), /guestTrialRoot \|\| guestTrialCaptureAbort/u);
  assert.match(bridge, /function stopSession\(reason = "request_cancelled"\) \{\s*invalidateGuestTrial\(\)/u);
});
