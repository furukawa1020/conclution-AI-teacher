import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const client = fs.readFileSync(new URL("../src/main.rs", import.meta.url), "utf8");
const agent = fs.readFileSync(new URL("../../../internal/conversation/agent.go", import.meta.url), "utf8");

test("ゲストは一操作で30秒A-first専用体験へ入りAIは答えを代作しない", () => {
  assert.match(client, /30秒で違いを試す　パスキー不要/u);
  assert.match(client, /今、いちばん減らしたい負担は？/u);
  const instruction = client.match(/const GUEST_SPRINT_INSTRUCTION: &str =\s*"([^"]+)"/u)?.[1];
  assert.ok(instruction);
  assert.match(instruction, /思いつくままに話してください/u);
  assert.doesNotMatch(instruction, /答えだけ|先に|理由はあと/u);
  assert.match(client, /KOTAEはあなたより先にAを言いません/u);
  assert.match(client, /route\.set\("guest-word-mining"\.to_string\(\)\)/u);
  assert.match(agent, /AI自身の答え・候補・助言を作らない/u);
  assert.match(agent, /同じ一言を、今度は最初に/u);
  assert.match(agent, /能力・上達の主張をしない/u);
  assert.match(agent, /成功の講評・復唱・褒め言葉を追加せず発話権を返す/u);
  assert.doesNotMatch(agent, /いま、答えが先に届いた|今の一言はあなたが先に言った/u);
});
