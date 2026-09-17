import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("guest status authenticates the named guest app before primary passkey Auth", async () => {
  const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const status = bridge.slice(
    bridge.indexOf("async function getStatus()"),
    bridge.indexOf("function classifyMicrophoneError("),
  );
  const guest = status.indexOf("if (guestModeActive)");
  const primary = status.indexOf("const { auth } = await firebaseAuth()");
  assert.ok(guest >= 0 && primary >= 0 && guest < primary);
  assert.match(status.slice(guest, primary), /await secureCredentials\(\)/u);
  assert.match(status.slice(guest, primary), /state: `guest-ready`/u);
});
