import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import test from "node:test";

const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const statusSource = bridge.slice(
  bridge.indexOf("async function getStatus()"),
  bridge.indexOf("function classifyMicrophoneError("),
);

function statusWith(overrides = {}) {
  const scope = {
    siteKeyConfigured: () => true,
    guestModeActive: false,
    firebaseAuth: async () => ({ auth: { currentUser: null } }),
    secureCredentials: async () => ({}),
    passkeyRegistrationRecovery: { isPending: () => false },
    ...overrides,
  };
  return runInNewContext(`${statusSource}\ngetStatus`, scope);
}

test("a guest remains ready even when primary passkey Auth is signed out", async () => {
  let primaryCalls = 0;
  const getStatus = statusWith({
    guestModeActive: true,
    firebaseAuth: async () => {
      primaryCalls += 1;
      return { auth: { currentUser: null } };
    },
  });
  assert.equal((await getStatus()).state, "guest-ready");
  assert.equal(primaryCalls, 0);
});

test("expired guest credentials are unavailable, not mistaken for a passkey login", async () => {
  const getStatus = statusWith({
    guestModeActive: true,
    secureCredentials: async () => {
      throw new Error("guest_session_expired");
    },
  });
  assert.equal((await getStatus()).state, "unavailable");
});

test("a signed-out non-guest still requires identity", async () => {
  assert.equal((await statusWith()()).state, "identity-required");
});
