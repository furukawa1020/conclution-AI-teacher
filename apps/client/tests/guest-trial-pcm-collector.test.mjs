import assert from "node:assert/strict";
import test from "node:test";
import { createGuestTrialPcmCollector } from "../web/guest-trial-controller.mjs";

function setup(options = {}) {
  const commands = [];
  const port = {postMessage: value => commands.push(value)};
  const collector = createGuestTrialPcmCollector({port, generation: 1, sampleRate: 48000, ...options});
  const emit = message => port.onmessage({data: {version: 1, generation: 1, ...message}});
  const frame = (sequence, value = 1) => {
    const pcm = new Uint8Array(640).fill(value);
    emit({type: "frame", sequence, contextFrame: sequence * 960, pcm: pcm.buffer});
    return pcm;
  };
  return {collector, commands, emit, frame};
}

test("explicit recording preserves even tiny samples and sends only after seal", async () => {
  const {collector, commands, emit, frame} = setup();
  const first = frame(0), second = frame(1, 0);
  assert.equal(commands[0].type, "confirm");
  assert.equal(commands[0].quietConfirmed, false);
  const finished = collector.finish();
  emit({type: "sealed", lastSequence: 1});
  const audio = await finished;
  assert.equal(audio.length, 1280);
  assert.equal(audio[0], 1); assert.equal(audio[640], 0);
  assert.ok(first.every(v => v === 0)); assert.ok(second.every(v => v === 0));
  const late = frame(2, 7); assert.ok(late.every(v => v === 0));
  audio.fill(0);
});

test("cancellation rejects pending seal and erases all frames", async () => {
  const {collector, frame} = setup();
  const pcm = frame(0, 7);
  const finished = collector.finish(); collector.cancel();
  await assert.rejects(finished, /capture_unavailable/u);
  assert.ok(pcm.every(v => v === 0));
  assert.ok(frame(1, 7).every(v => v === 0));
});

test("missing seal has a finite deadline", async () => {
  const {collector, frame} = setup({sealTimeoutMs: 5});
  const pcm = frame(0, 4);
  await assert.rejects(collector.finish(), /capture_unavailable/u);
  assert.ok(pcm.every(v => v === 0));
});

test("gaps, duplicates and foreign generations discard the entire capture", async () => {
  for (const invalid of [
    {type: "frame", sequence: 0, contextFrame: 960},
    {type: "frame", sequence: 2, contextFrame: 960},
    {type: "frame", sequence: 1, contextFrame: 1920},
    {type: "frame", sequence: 1, contextFrame: 960, generation: 2},
  ]) {
    const {collector, emit, frame} = setup(); const first = frame(0, 8);
    const bad = new Uint8Array(640).fill(9); emit({...invalid, pcm: bad.buffer});
    assert.ok(first.every(v => v === 0)); assert.ok(bad.every(v => v === 0));
    await assert.rejects(collector.finish(), /capture_unavailable/u);
  }
});

test("30 second byte ceiling cannot grow with delayed main-thread delivery", async () => {
  const {collector, frame} = setup();
  const first = frame(0, 3);
  for (let i = 1; i <= 1500; i++) frame(i);
  assert.equal(collector.state(), "closed"); assert.ok(first.every(v => v === 0));
  await assert.rejects(collector.finish(), /capture_unavailable/u);
});

test("cancellation notifies the owner once after erasure even with reentrant disposal", () => {
  let notifications = 0, pcm, collector;
  const env = setup({onCancel: () => {
    notifications++;
    assert.ok(pcm.every(v => v === 0));
    collector.cancel();
  }});
  collector = env.collector; pcm = env.frame(0, 3);
  collector.cancel(); collector.cancel();
  assert.equal(notifications, 1);
});

test("successful seal does not notify cancellation", async () => {
  let notifications = 0;
  const env = setup({onCancel: () => notifications++});
  env.frame(0); const pending = env.collector.finish();
  env.emit({type: "sealed", lastSequence: 0});
  (await pending).fill(0); env.collector.cancel();
  assert.equal(notifications, 0);
});
