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
  assert.match(bridge, /guestModeActive = false;[\s\S]*signOut\(authInstance\)/u);
  assert.match(bridge, /guest_session_expired/u);
  assert.match(ui, /パスキーなしで、今すぐ試す/u);
  assert.match(ui, /保存しません/u);
  assert.match(ui, /あなたのひとこと/u);
  assert.match(agent, /guest_word_mining/u);
  assert.match(agent, /もしかして、○○？/u);
  assert.match(agent, /いまの言葉はあなたが言った/u);
});
