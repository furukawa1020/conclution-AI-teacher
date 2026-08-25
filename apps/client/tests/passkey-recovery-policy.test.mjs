import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("single-use recovery is transient, passkey-bound, and explicit about its boundary", async () => {
  const [bridge, ui, service, memory, firestore] = await Promise.all([
    readFile(new URL("apps/client/web/firebase-bridge.js", root), "utf8"),
    readFile(new URL("apps/client/src/main.rs", root), "utf8"),
    readFile(new URL("internal/passkey/passkey.go", root), "utf8"),
    readFile(new URL("internal/passkey/memory_store.go", root), "utf8"),
    readFile(new URL("internal/passkey/firestore_store.go", root), "utf8"),
  ]);

  assert.match(bridge, /async function recoverPasskeyAccount\(code\)/u);
  assert.match(bridge, /PASSKEY_RECOVERY_REGISTRATION_BEGIN_ENDPOINT/u);
  assert.match(bridge, /PASSKEY_REGISTRATION_FINISH_ENDPOINT/u);
  assert.match(bridge, /recoveryCode = undefined/gmu);
  assert.match(bridge, /async function issuePasskeyRecoveryCode\(\)/u);
  assert.match(bridge, /Reflect\.ownKeys\(value\)\.length !== 2/u);
  assert.match(bridge, /\^krc1_\[A-Za-z0-9_-\]\{43\}\$/u);

  const recoveryFunction = bridge.slice(
    bridge.indexOf("async function recoverPasskeyAccount"),
    bridge.indexOf("async function secureCredentials"),
  );
  assert.doesNotMatch(recoveryFunction, /console\.|localStorage|sessionStorage/u);

  assert.match(ui, /回復コードを持っていることだけを確認します/u);
  assert.match(ui, /自然人としての本人性を証明するものではありません/u);
  assert.match(ui, /コードと全パスキーを失うと、この仮名アカウントは回復できません/u);
  assert.match(ui, /今だけ表示しています/u);

  assert.match(service, /CreateRecoveryCredential/u);
  assert.match(memory, /delete\(s\.recoveryByCode, codeKey\)/u);
  assert.match(firestore, /tx\.Delete\(recoveryCodeRef\)/u);
  assert.match(firestore, /tx\.Delete\(recoveryRef\)/u);
});
