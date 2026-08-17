import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("guest mode is one-tap, ephemeral, and App Check-bound", async () => {
  const [bridge, ui, agent] = await Promise.all([
    readFile(new URL("apps/client/web/firebase-bridge.js", root), "utf8"),
    readFile(new URL("apps/client/src/main.rs", root), "utf8"),
    readFile(new URL("internal/conversation/agent.go", root), "utf8"),
  ]);
  assert.match(bridge, /signInAnonymously/u);
  assert.match(bridge, /setPersistence\(auth, inMemoryPersistence\)/u);
  assert.match(bridge, /getAppCheckToken\(appCheck, false\)/u);
  assert.match(
    bridge,
    /if \(!auth\.currentUser\) \{\s*return Object\.freeze\(\{ state: "identity-required" \}\)/u,
  );
  assert.doesNotMatch(
    bridge,
    /async function initializeFirebaseAuth\(\)[\s\S]*?getAppCheckToken\(appCheck, false\)[\s\S]*?const firebaseAuth/u,
  );
  assert.match(
    bridge,
    /async function initializeFirebaseAuth\(\) \{\s*const \{ app \} = await firebaseApp\(\)/u,
  );
  assert.doesNotMatch(
    bridge,
    /async function initializeFirebaseAuth\(\)[\s\S]*?appServices\(\)[\s\S]*?const firebaseAuth/u,
  );
  assert.match(bridge, /GUEST_START_DEADLINE_MS = 12_000/u);
  assert.match(bridge, /guestStartGate\.isCurrent\(generation\)/u);
  assert.match(ui, /安全なゲスト接続を準備中…/u);
  assert.match(bridge, /guestModeActive = false;[\s\S]*signOut\(authInstance\)/u);
  assert.match(bridge, /guest_session_expired/u);
  assert.match(ui, /30秒で違いを試す　パスキー不要/u);
  assert.match(ui, /保存しません/u);
  assert.match(ui, /あなたの最初の一言/u);
  assert.match(agent, /guest_word_mining/u);
  assert.match(agent, /AI自身の答え・候補・助言を作らない/u);
  assert.match(agent, /今の一言はあなたが先に言った/u);
});
