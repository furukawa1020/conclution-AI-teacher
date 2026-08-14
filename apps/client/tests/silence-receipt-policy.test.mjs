import assert from "node:assert/strict";
import test from "node:test";

import {
  createSilenceReceiptGate,
  SILENCE_RECEIPT_OUTCOMES,
} from "../web/voice-stream-policy.mjs";

function terminal(overrides = {}) {
  return {
    answerProof: "question_bound_input_answer_first",
    coachAction: "complete",
    coachPhase: "complete",
    fallback: false,
    interrupted: false,
    ownerGeneration: 7,
    ...overrides,
  };
}

test("monotonic client sample clock and server answer boundary yield one content-free receipt", () => {
  const gate = createSilenceReceiptGate();
  assert.equal(gate.arm(7, true), true);
  assert.equal(gate.observe(7, 48_000), true);
  assert.equal(gate.observe(7, 48_960), true);
  assert.equal(gate.authorize(terminal()), SILENCE_RECEIPT_OUTCOMES.waited);
  assert.deepEqual(gate.snapshot(), { observedFrames: 0, state: "disabled" });
  assert.equal(gate.authorize(terminal()), SILENCE_RECEIPT_OUTCOMES.none);
});

test("output overlap, interruption, fallback, silence, and clock faults fail closed", () => {
  for (const mutate of [
    (gate) => gate.arm(7, false),
    (gate) => gate.arm(7, true),
    (gate) => { gate.arm(7, true); gate.observe(7, 10); gate.observe(7, 10); },
    (gate) => { gate.arm(7, true); gate.observe(7, 10); gate.observe(7, 9); },
  ]) {
    const gate = createSilenceReceiptGate();
    mutate(gate);
    assert.equal(gate.authorize(terminal()), SILENCE_RECEIPT_OUTCOMES.none);
  }
  for (const invalid of [
    { fallback: true },
    { interrupted: true },
    { ownerGeneration: 8 },
    { answerProof: "none" },
    { coachPhase: "expanding", coachAction: "expand" },
    { surplus: "content" },
  ]) {
    const gate = createSilenceReceiptGate();
    gate.arm(7, true);
    gate.observe(7, 10);
    assert.equal(gate.authorize(terminal(invalid)), SILENCE_RECEIPT_OUTCOMES.none);
  }

  const missingClock = createSilenceReceiptGate();
  missingClock.arm(7, true);
  missingClock.observe(7, 10);
  assert.equal(missingClock.observe(7, 10_000), false);
  assert.equal(missingClock.authorize(terminal()), SILENCE_RECEIPT_OUTCOMES.none);
});
