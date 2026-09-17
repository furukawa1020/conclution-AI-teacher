import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";
import test from "node:test";

import { authIdentityChanged } from "../web/voice-session-policy.mjs";

test("same UID token refresh cannot invalidate a speaking guest", () => {
  assert.equal(authIdentityChanged("guest-a", "guest-a"), false);
  assert.equal(authIdentityChanged(null, null), false);
  assert.equal(authIdentityChanged(null, "guest-a"), true);
  assert.equal(authIdentityChanged("guest-a", "guest-b"), true);
  assert.equal(authIdentityChanged("guest-a", null), true);
  assert.throws(() => authIdentityChanged(undefined, "guest-a"), /auth_identity_invalid/u);
});

test("browser Auth observer compares UID instead of every ID token notification", async () => {
  const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const observer = bridge.slice(
    bridge.indexOf("function observeAuthInvalidation(auth)"),
    bridge.indexOf("async function initializeFirebaseAuth()"),
  );
  assert.match(observer, /onIdTokenChanged\(auth, \(user\) => \{/u);
  assert.match(observer, /authIdentityChanged\(previousUid, nextUid\)/u);
  assert.match(observer, /if \(!changed\) return;[\s\S]*stopSession\("identity_changed"\)/u);
});

test("guest start refuses an App Check dummy token before anonymous sign-up", async () => {
  const bridge = await readFile(new URL("../web/firebase-bridge.js", import.meta.url), "utf8");
  const verifier = bridge.slice(
    bridge.indexOf("function verifiedGuestAppCheckToken(result)"),
    bridge.indexOf("let guestModeActive = false;"),
  );
  const start = bridge.slice(
    bridge.indexOf("async function startGuestMode()"),
    bridge.indexOf("async function listPasskeyCredentials()"),
  );
  assert.match(verifier, /result\?\.error/u);
  assert.match(verifier, /result\.token\.split\(`\.`\)\.length !== 3/u);
  assert.match(verifier, /guest_attestation_failed/u);
  assert.ok(start.indexOf("await getAppCheckToken(appCheck, false)") < start.indexOf("verifiedGuestAppCheckToken(attestation)"));
  assert.ok(start.indexOf("verifiedGuestAppCheckToken(attestation)") < start.indexOf("signInAnonymously(auth)"));

  const verify = runInNewContext(`${verifier}\nverifiedGuestAppCheckToken`, {
    fail: (reason) => { throw new Error(reason); },
  });
  assert.throws(() => verify({ token: "dummy", error: new Error("network") }), /guest_attestation_failed/u);
  assert.throws(() => verify({ token: "dummy" }), /guest_attestation_failed/u);
  assert.equal(verify({ token: "header.payload.signature" }), "header.payload.signature");
});
