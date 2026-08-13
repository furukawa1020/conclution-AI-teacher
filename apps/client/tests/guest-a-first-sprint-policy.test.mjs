import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");
const agent = fs.readFileSync(new URL("../../../internal/conversation/agent.go", import.meta.url), "utf8");

test("ゲストは一操作で30秒A-first専用体験へ入りAIは答えを代作しない", () => {
  assert.match(client, /30秒で違いを試す　パスキー不要/u);
  assert.match(client, /今、いちばん減らしたい負担は？/u);
  assert.match(client, /答えだけを先に、小声で一言/u);
  assert.match(client, /KOTAEはあなたより先にAを言いません/u);
  assert.match(client, /route\.set\("guest-word-mining"\.to_string\(\)\)/u);
  assert.match(agent, /AI自身の答え・候補・助言を作らない/u);
  assert.match(agent, /同じ一言を、今度は最初に/u);
  assert.match(agent, /能力・上達の主張をしない/u);
});
