import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import test from "node:test";

const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
const statusSource = bridge.slice(
  bridge.indexOf("async function getStatus()"),
  bridge.indexOf("function classifyMicrophoneError("),
);

function statusHarness(overrides = {}) {
  const scope = {
    siteKeyConfigured: () => true,
    guestModeActive: false,
    firebaseAuth: async () => ({ auth: { currentUser: null } }),
    secureCredentials: async () => ({}),
    passkeyRegistrationRecovery: { isPending: () => false },
    ...overrides,
  };
  return {
    getStatus: runInNewContext(`${statusSource}\ngetStatus`, scope),
    activateGuest: () => { scope.guestModeActive = true; },
  };
}

function statusWith(overrides = {}) {
  return statusHarness(overrides).getStatus;
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

for (const scenario of [
  { name: "signed-out primary", primaryUser: null, expired: false, expected: "guest-ready" },
  { name: "signed-in primary", primaryUser: {}, expired: false, expected: "guest-ready" },
  { name: "expired guest", primaryUser: null, expired: true, expected: "unavailable" },
]) {
  test(`guest activation during primary Auth await uses guest credentials: ${scenario.name}`, async () => {
    let resolvePrimary;
    const primary = new Promise((resolve) => { resolvePrimary = resolve; });
    let primaryCalls = 0;
    let credentialCalls = 0;
    let accountRequests = 0;
    const { getStatus, activateGuest } = statusHarness({
      firebaseAuth: () => {
        primaryCalls += 1;
        return primary;
      },
      secureCredentials: async () => {
        credentialCalls += 1;
        if (scenario.expired) throw new Error("guest_session_expired");
        return {};
      },
      fetch: async () => {
        accountRequests += 1;
        return { ok: true };
      },
    });

    const pendingStatus = getStatus();
    assert.equal(primaryCalls, 1);
    assert.equal(credentialCalls, 0);
    activateGuest();
    resolvePrimary({ auth: { currentUser: scenario.primaryUser } });

    assert.equal((await pendingStatus).state, scenario.expected);
    assert.equal(credentialCalls, 1);
    assert.equal(accountRequests, 0);
  });
}
