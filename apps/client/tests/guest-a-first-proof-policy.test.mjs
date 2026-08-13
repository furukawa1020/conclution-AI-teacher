import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const bridge = fs.readFileSync(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const server = fs.readFileSync(new URL("../../../internal/httpapi/httpapi.go", import.meta.url), "utf8");
const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");

test("ゲストA-first表示は本文なしの有限proofだけから導出される", () => {
  for (const value of ["no_verified_change", "changed_to_answer_first", "stayed_answer_first"]) {
    assert.match(bridge, new RegExp(value, "u"));
    assert.match(server, new RegExp(value, "u"));
  }
  assert.match(bridge, /guestModeActive && guestAFirstOutcome !== expectedGuestAFirstOutcome/u);
  assert.match(server, /guest A-first outcome does not match its proofs/u);
  assert.match(client, /AIは答えを足していません/u);
  assert.match(client, /上達や能力の判定ではありません/u);
  assert.doesNotMatch(bridge, /guestAFirstOutcome.*caption/u);
});
