import assert from "node:assert/strict";
import test from "node:test";

import {
  createGuestAFirstSprintSlo,
  createGuestStartGate,
  validateGuestAFirstSloBatch,
} from "../web/guest-a-first-slo-policy.mjs";

function proof(overrides = {}) {
  return {
    aiOutputBeforeAnswer: false,
    answerProof: "question_bound_input_answer_first",
    coachAction: "complete",
    coachPhase: "complete",
    guestAFirstOutcome: "changed_to_answer_first",
    transitionProof: "question_bound_input_clause_later_to_first",
    ...overrides,
  };
}

function run({ listen = 1_000, question = 10_000, response = 11_000, finish = 30_000, evidence = proof() } = {}) {
  const slo = createGuestAFirstSprintSlo();
  assert.equal(slo.begin(0), true);
  assert.equal(slo.markListening(listen), true);
  assert.equal(slo.markQuestionEnded(question), true);
  assert.equal(slo.markResponseStarted(response), true);
  return slo.finish(finish, evidence);
}

test("controlled clock keeps exact one-second and thirty-second boundaries", () => {
  assert.deepEqual(run(), {
    aiSubstitution: "zero",
    completion: "within_target",
    counterexample: "verified_run",
    listeningStart: "on_target",
    responseStart: "on_target",
    version: 1,
  });
  assert.equal(run({ listen: 1_001 }).listeningStart, "missed");
  assert.equal(run({ response: 11_001 }).responseStart, "missed");
  assert.equal(run({ finish: 30_001 }).completion, "over_target");
});

test("AI substitution, no-A, quote, different question, and malformed proof never count", () => {
  for (const evidence of [
    proof({ aiOutputBeforeAnswer: true }),
    proof({ answerProof: "none", guestAFirstOutcome: "no_verified_change", transitionProof: "none" }),
    proof({ guestAFirstOutcome: "no_verified_change" }),
    proof({ transitionProof: "none" }),
    proof({ quote: "A" }),
  ]) {
    const event = run({ evidence });
    assert.equal(event.completion, "not_verified");
    assert.equal(event.counterexample, "rejected");
  }
});

test("one hundred content-free observations enforce p95 and zero substitution", () => {
  const events = Array.from({ length: 100 }, (_, index) =>
    run({ listen: index < 95 ? 1_000 : 1_001, response: index < 95 ? 11_000 : 11_001 }));
  assert.equal(validateGuestAFirstSloBatch(events), true);
  events[0] = run({ evidence: proof({ aiOutputBeforeAnswer: true }) });
  assert.equal(validateGuestAFirstSloBatch(events), false);
});

test("guest start generation rejects overlap and every stale completion", () => {
  const gate = createGuestStartGate();
  const first = gate.begin();
  assert.equal(Number.isSafeInteger(first), true);
  assert.equal(gate.begin(), null);
  assert.equal(gate.isCurrent(first), true);
  gate.clear();
  assert.equal(gate.isCurrent(first), false);
  assert.equal(gate.finish(first), false);
  const second = gate.begin();
  assert.notEqual(second, first);
  assert.equal(gate.finish(second), true);
  assert.equal(gate.isCurrent(second), false);
});
