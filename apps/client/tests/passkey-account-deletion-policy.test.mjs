import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("仮名アカウント削除は再認証境界と二段確認に固定される", async () => {
  const [bridge, ui, api] = await Promise.all([
    readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8"),
    readFile(new URL("../src/main.rs", import.meta.url), "utf8"),
    readFile(new URL("../../../internal/httpapi/httpapi.go", import.meta.url), "utf8"),
  ]);
  assert.match(bridge, /PASSKEY_ACCOUNT_DELETE_CONFIRMATION/u);
  assert.match(bridge, /await secureCredentials\(true\)/u);
  assert.match(bridge, /await signOut\(authInstance\)/u);
  assert.match(ui, /仮名アカウントの完全削除を確認する/u);
  assert.match(ui, /この仮名アカウントを完全に削除する/u);
  assert.match(api, /requirePasskeyManagementIdentity\(http\.HandlerFunc\(server\.deletePasskeyAccount\)\)/u);
});
