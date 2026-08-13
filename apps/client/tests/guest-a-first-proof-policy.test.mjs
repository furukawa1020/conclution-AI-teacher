import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const bridge = fs.readFileSync(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const server = fs.readFileSync(new URL("../../../internal/httpapi/httpapi.go", import.meta.url), "utf8");
const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");
const styles = fs.readFileSync(new URL("../assets/main.css", import.meta.url), "utf8");

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

test("答えの彫刻は有限proofだけを三つの誠実な表示へ写像する", () => {
  assert.match(client, /answer-sculpture--changed/u);
  assert.match(client, /answer-sculpture--stayed/u);
  assert.match(client, /answer-sculpture--unverified/u);
  assert.match(client, /あなたのAが説明の後ろから一言目へ移った/u);
  assert.match(styles, /@keyframes answer-move-forward/u);
  assert.match(styles, /@keyframes answer-leave-back/u);
  assert.match(styles, /answer-sculpture--changed \.answer-sculpture__a--back/u);
  assert.match(styles, /prefers-reduced-motion: reduce/u);
  assert.match(client, /role: "img",\s*aria_live: "polite"/u);
  assert.doesNotMatch(styles, /answer-sculpture--(?:stayed|unverified)[^{]*\{[^}]*(?:animation|transition):/u);
  assert.doesNotMatch(client, /answer-sculpture[\s\S]{0,180}(caption|transcript)/u);
});
