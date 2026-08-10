import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const client = await readFile(
  new URL("../src/main.rs", import.meta.url),
  "utf8",
);

test("terminal verified answerだけが本文なしの回答所有権レシートを表示する", () => {
  const predicateStart = client.indexOf(
    "const fn shows_answer_ownership_receipt(self) -> bool",
  );
  const predicateEnd = client.indexOf("\n    const fn status", predicateStart);
  assert.ok(predicateStart >= 0);
  assert.ok(predicateEnd > predicateStart);
  const predicate = client.slice(predicateStart, predicateEnd);

  assert.match(predicate, /self\.yielded_after_owned_answer\(\)/u);
  assert.doesNotMatch(predicate, /caption|transcript|score|latency/u);

  const receiptStart = client.indexOf(
    "if coach_snapshot.shows_answer_ownership_receipt()",
  );
  const receiptEnd = client.indexOf(
    "if turn_notice_snapshot.is_visible()",
    receiptStart,
  );
  assert.ok(receiptStart >= 0);
  assert.ok(receiptEnd > receiptStart);
  const receipt = client.slice(receiptStart, receiptEnd);

  for (const fixedLine of [
    "AI代理発話 0",
    "本人のA先頭 確認",
    "発話権 返却",
  ]) {
    assert.equal(receipt.match(new RegExp(fixedLine, "gu"))?.length, 1);
  }
  assert.doesNotMatch(
    receipt,
    /caption|transcript|score|能力|上達|read\(|clone\(|format!/u,
  );
});
