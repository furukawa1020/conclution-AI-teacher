import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../../", import.meta.url);

test("passkey management is principal-bound, minimal, and confirmation-gated", async () => {
  const [bridge, ui, api] = await Promise.all([
    readFile(new URL("apps/client/web/firebase-bridge.js", root), "utf8"),
    readFile(new URL("apps/client/src/main.rs", root), "utf8"),
    readFile(new URL("internal/httpapi/passkeys.go", root), "utf8"),
  ]);
  assert.match(bridge, /async function listPasskeyCredentials\(\)/u);
  assert.match(bridge, /async function addPasskeyCredential\(\)/u);
  assert.match(bridge, /async function revokePasskeyCredential\(reference\)/u);
  assert.match(bridge, /PASSKEY_CREDENTIAL_REGISTRATION_BEGIN_ENDPOINT/u);
  assert.match(bridge, /PASSKEY_CREDENTIAL_REGISTRATION_FINISH_ENDPOINT/u);
  assert.match(bridge, /const credentials = await secureCredentials\(true\)/u);
  assert.match(bridge, /finishResponse\.status !== 204/u);
  const addCredentialFunction = bridge.slice(
    bridge.indexOf("async function addPasskeyCredential"),
    bridge.indexOf("async function revokePasskeyCredential"),
  );
  assert.match(addCredentialFunction, /Authorization: `Bearer \$\{credentials\.idToken\}`/u);
  assert.match(addCredentialFunction, /"X-Firebase-AppCheck": credentials\.appCheckToken/u);
  assert.doesNotMatch(addCredentialFunction, /\buid\b|userHandle|localStorage|sessionStorage|console\./u);
  assert.match(bridge, /Reflect\.ownKeys\(summary\)\.length !== 3/u);
  assert.match(ui, /同じ仮名アカウントだけへ登録します/u);
  assert.match(ui, /本人確認して新しいパスキーを追加する/u);
  assert.match(ui, /このパスキーでは以後戻れません。本当に失効しますか？/u);
  assert.match(ui, /最後の1件はアカウントへ戻れなくなるため失効できません/u);
  assert.match(api, /principal\.UID/u);
  assert.doesNotMatch(api, /json:"uid"/u);
  assert.match(api, /passkey_credential_not_found/u);
  assert.match(api, /passkey_last_credential/u);
});
