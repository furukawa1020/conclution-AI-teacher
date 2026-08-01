import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  base64urlEncode,
  decidePasskeyAction,
  decodeAuthenticationBegin,
  decodeRegistrationBegin,
  encodeAuthenticationCredential,
  encodeRegistrationCredential,
  isPasskeyCancellation,
  parsePasskeyFinish,
  PASSKEY_AUTH_METHOD,
  PASSKEY_FRESHNESS_SECONDS,
  strictBase64urlDecode,
} from "../web/passkey-policy.mjs";

function bytes(...values) {
  return Uint8Array.from(values);
}

function repeatedBytes(length, value) {
  return new Uint8Array(length).fill(value);
}

function registrationBegin() {
  return {
    ceremonyId: base64urlEncode(repeatedBytes(32, 7)),
    options: {
      publicKey: {
        attestation: "none",
        authenticatorSelection: {
          requireResidentKey: true,
          residentKey: "required",
          userVerification: "required",
        },
        challenge: base64urlEncode(repeatedBytes(32, 11)),
        excludeCredentials: [
          {
            id: base64urlEncode(bytes(1, 2, 3)),
            transports: ["internal", "hybrid"],
            type: "public-key",
          },
        ],
        pubKeyCredParams: [
          { alg: -7, type: "public-key" },
          { alg: -257, type: "public-key" },
        ],
        rp: { id: "example.com", name: "コタエーAI" },
        timeout: 300_000,
        user: {
          displayName: "コタエーAI利用者",
          id: base64urlEncode(repeatedBytes(64, 13)),
          name: "pk_account",
        },
      },
    },
  };
}

function authenticationBegin() {
  return {
    ceremonyId: base64urlEncode(repeatedBytes(32, 17)),
    options: {
      publicKey: {
        allowCredentials: [
          {
            id: base64urlEncode(bytes(8, 9, 10)),
            transports: ["usb"],
            type: "public-key",
          },
        ],
        challenge: base64urlEncode(repeatedBytes(32, 19)),
        rpId: "example.com",
        timeout: 120_000,
        userVerification: "required",
      },
    },
  };
}

test("base64url conversion is canonical, padding-free, and bounded", () => {
  for (const value of [bytes(0), bytes(0, 1), bytes(0, 1, 2), bytes(255, 254, 253, 252)]) {
    const encoded = base64urlEncode(value);
    assert.deepEqual(strictBase64urlDecode(encoded), value);
    assert.doesNotMatch(encoded, /[+/=]/u);
  }

  assert.throws(() => strictBase64urlDecode(""), /passkey_payload_invalid/u);
  assert.throws(() => strictBase64urlDecode("A"), /passkey_payload_invalid/u);
  assert.throws(() => strictBase64urlDecode("AA=="), /passkey_payload_invalid/u);
  assert.throws(() => strictBase64urlDecode("AB"), /passkey_payload_invalid/u);
  assert.throws(
    () => strictBase64urlDecode(base64urlEncode(bytes(1, 2)), { maxBytes: 1 }),
    /passkey_payload_invalid/u,
  );
});

test("registration begin decodes only reviewed binary fields without mutating JSON", () => {
  const source = registrationBegin();
  const original = structuredClone(source);
  const decoded = decodeRegistrationBegin(source);

  assert.equal(decoded.ceremonyId, source.ceremonyId);
  assert.ok(decoded.options.publicKey.challenge instanceof Uint8Array);
  assert.equal(decoded.options.publicKey.challenge.byteLength, 32);
  assert.ok(decoded.options.publicKey.user.id instanceof Uint8Array);
  assert.equal(decoded.options.publicKey.user.id.byteLength, 64);
  assert.ok(decoded.options.publicKey.excludeCredentials[0].id instanceof Uint8Array);
  assert.deepEqual(source, original);

  const unknown = registrationBegin();
  unknown.options.publicKey.transcript = "must never pass through";
  assert.throws(
    () => decodeRegistrationBegin(unknown),
    /passkey_payload_invalid/u,
  );

  const unsafeResidentKey = registrationBegin();
  unsafeResidentKey.options.publicKey.authenticatorSelection.userVerification =
    "preferred";
  assert.throws(
    () => decodeRegistrationBegin(unsafeResidentKey),
    /passkey_payload_invalid/u,
  );

  const missingSelection = registrationBegin();
  delete missingSelection.options.publicKey.authenticatorSelection;
  assert.throws(
    () => decodeRegistrationBegin(missingSelection),
    /passkey_payload_invalid/u,
  );

  const unknownExtension = registrationBegin();
  unknownExtension.options.publicKey.extensions = { largeBlob: { support: "required" } };
  assert.throws(
    () => decodeRegistrationBegin(unknownExtension),
    /passkey_payload_invalid/u,
  );
});

test("authentication begin strictly decodes challenge and allowed credential IDs", () => {
  const decoded = decodeAuthenticationBegin(authenticationBegin());

  assert.equal(decoded.options.publicKey.userVerification, "required");
  assert.equal(decoded.options.publicKey.challenge.byteLength, 32);
  assert.equal(decoded.options.publicKey.allowCredentials[0].id.byteLength, 3);

  const nonCanonical = authenticationBegin();
  nonCanonical.options.publicKey.challenge = "AB";
  assert.throws(
    () => decodeAuthenticationBegin(nonCanonical),
    /passkey_payload_invalid/u,
  );

  const downgrade = authenticationBegin();
  downgrade.options.publicKey.userVerification = "discouraged";
  assert.throws(
    () => decodeAuthenticationBegin(downgrade),
    /passkey_payload_invalid/u,
  );

  const unknownExtension = authenticationBegin();
  unknownExtension.options.publicKey.extensions = { prf: {} };
  assert.throws(
    () => decodeAuthenticationBegin(unknownExtension),
    /passkey_payload_invalid/u,
  );
});

test("registration credential is encoded as go-webauthn JSON", () => {
  const rawId = bytes(1, 2, 3, 4);
  const credential = {
    authenticatorAttachment: "platform",
    getClientExtensionResults: () => ({}),
    id: base64urlEncode(rawId),
    rawId: rawId.buffer,
    response: {
      attestationObject: bytes(5, 6, 7).buffer,
      clientDataJSON: bytes(8, 9).buffer,
      getTransports: () => ["internal", "hybrid"],
    },
    type: "public-key",
  };

  assert.deepEqual(JSON.parse(JSON.stringify(encodeRegistrationCredential(credential))), {
    authenticatorAttachment: "platform",
    clientExtensionResults: {},
    id: "AQIDBA",
    rawId: "AQIDBA",
    response: {
      attestationObject: "BQYH",
      clientDataJSON: "CAk",
      transports: ["internal", "hybrid"],
    },
    type: "public-key",
  });

  credential.id = "different";
  assert.throws(
    () => encodeRegistrationCredential(credential),
    /passkey_payload_invalid/u,
  );
});

test("authentication credential includes nullable userHandle and no free-form fields", () => {
  const rawId = bytes(10, 11, 12);
  const credential = {
    authenticatorAttachment: "cross-platform",
    getClientExtensionResults: () => ({}),
    id: base64urlEncode(rawId),
    rawId,
    response: {
      authenticatorData: bytes(1, 1, 1),
      clientDataJSON: bytes(2, 2),
      signature: bytes(3, 3, 3, 3),
      userHandle: null,
    },
    type: "public-key",
  };

  assert.deepEqual(JSON.parse(JSON.stringify(encodeAuthenticationCredential(credential))), {
    authenticatorAttachment: "cross-platform",
    clientExtensionResults: {},
    id: "CgsM",
    rawId: "CgsM",
    response: {
      authenticatorData: "AQEB",
      clientDataJSON: "AgI",
      signature: "AwMDAw",
      userHandle: null,
    },
    type: "public-key",
  });
});

test("passkey action never downgrades a stale or invalid custom session", () => {
  const now = 10_000;
  assert.equal(
    decidePasskeyAction(
      {
        accountVerified: true,
        authMethod: undefined,
        passkeyAtSeconds: now,
        provider: "google.com",
      },
      now,
    ),
    "reject",
  );
  assert.equal(
    decidePasskeyAction(
      {
        accountVerified: true,
        authMethod: PASSKEY_AUTH_METHOD,
        passkeyAtSeconds: now - PASSKEY_FRESHNESS_SECONDS,
        provider: "custom",
      },
      now,
    ),
    "reuse",
  );
  assert.equal(
    decidePasskeyAction(
      {
        accountVerified: true,
        authMethod: PASSKEY_AUTH_METHOD,
        // A delayed custom-token exchange may make auth_time look fresh. The
        // immutable WebAuthn completion time must remain authoritative.
        authTimeSeconds: now,
        passkeyAtSeconds: now - PASSKEY_FRESHNESS_SECONDS - 1,
        provider: "custom",
      },
      now,
    ),
    "authentication",
  );
  assert.equal(
    decidePasskeyAction(
      {
        accountVerified: false,
        authMethod: undefined,
        passkeyAtSeconds: undefined,
        provider: "password",
      },
      now,
    ),
    "reject",
  );

  for (const invalidPasskeyAt of [undefined, "10000", 10_000.5, NaN, -1]) {
    assert.equal(
      decidePasskeyAction(
        {
          accountVerified: true,
          authMethod: PASSKEY_AUTH_METHOD,
          passkeyAtSeconds: invalidPasskeyAt,
          provider: "custom",
        },
        now,
      ),
      "authentication",
    );
  }
});

test("finish and cancellation handling use closed values", () => {
  assert.deepEqual(parsePasskeyFinish({
    authMethod: "passkey-v1",
    customToken: "header.payload.signature",
  }), {
    authMethod: "passkey-v1",
    customToken: "header.payload.signature",
  });
  assert.throws(
    () => parsePasskeyFinish({
      authMethod: "password",
      customToken: "header.payload.signature",
    }),
    /passkey_payload_invalid/u,
  );
  assert.equal(isPasskeyCancellation({ name: "NotAllowedError" }), true);
  assert.equal(isPasskeyCancellation({ name: "AbortError" }), true);
  assert.equal(isPasskeyCancellation({ name: "SecurityError" }), false);
});

test("voice startup awaits fresh passkey credentials before microphone or AudioContext", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const beginStart = bridge.indexOf("async function beginTurn(");
  const beginEnd = bridge.indexOf("async function waitForTurnEnd(", beginStart);
  assert.notEqual(beginStart, -1);
  assert.notEqual(beginEnd, -1);
  const beginTurn = bridge.slice(beginStart, beginEnd);
  const credentials = beginTurn.indexOf("await secureCredentials(true)");
  const microphone = beginTurn.indexOf("await ensureMediaStream(");
  const audioGraph = beginTurn.indexOf("await ensureAudioGraph(");
  assert.ok(credentials >= 0);
  assert.ok(microphone > credentials);
  assert.ok(audioGraph > microphone);
  assert.match(bridge, /passkey_required/u);
  assert.match(bridge, /signInWithCustomToken/u);
  assert.doesNotMatch(bridge, /(?:GoogleAuthProvider|signInWithPopup)/u);
  assert.doesNotMatch(bridge, /(?:localStorage|sessionStorage).*credential/iu);

  const registrationStart = bridge.indexOf("async function registerPasskey(");
  const registrationEnd = bridge.indexOf(
    "async function authenticatePasskey(",
    registrationStart,
  );
  assert.notEqual(registrationStart, -1);
  assert.notEqual(registrationEnd, -1);
  const registration = bridge.slice(registrationStart, registrationEnd);
  assert.match(registration, /X-Firebase-AppCheck|appCheckToken/u);
  assert.doesNotMatch(registration, /(?:Authorization|sourceIDToken|idToken)/u);
  assert.ok(
    registration.indexOf('requirePasskeySupport("registration")') <
      registration.indexOf("await passkeyJSON("),
  );
  assert.doesNotMatch(
    registration,
    /(?:console\.|localStorage|sessionStorage)/u,
  );

  const authenticationStart = bridge.indexOf(
    "async function authenticatePasskey(",
  );
  const authenticationEnd = bridge.indexOf(
    "async function freshPasskeyUser(",
    authenticationStart,
  );
  const authentication = bridge.slice(authenticationStart, authenticationEnd);
  assert.ok(
    authentication.indexOf('requirePasskeySupport("authentication")') <
      authentication.indexOf("await passkeyJSON("),
  );
  assert.doesNotMatch(
    authentication,
    /(?:Authorization|console\.|idToken|localStorage|sessionStorage)/u,
  );

  const explicitStart = bridge.indexOf("async function registerPasskeyAccount(");
  const explicitEnd = bridge.indexOf("async function secureCredentials(", explicitStart);
  assert.notEqual(explicitStart, -1);
  assert.notEqual(explicitEnd, -1);
  const explicitRegistration = bridge.slice(explicitStart, explicitEnd);
  assert.match(explicitRegistration, /await registerPasskey\(auth, appCheckResult\.token\)/u);
  assert.doesNotMatch(
    explicitRegistration,
    /ensureMediaStream|ensureAudioGraph|beginTurn\(/u,
  );
  assert.match(bridge.slice(bridge.indexOf("const publicBridge")), /registerPasskeyAccount/u);

  const main = await readFile(new URL("../src/main.rs", import.meta.url), "utf8");
  assert.match(main, /パスキーでアカウント操作を確認/u);
  assert.match(main, /登録済みの方　同じパスキーで戻る/u);
  assert.match(main, /初めての方　新しい仮名アカウントを作る/u);
  assert.match(main, /既存の仮名アカウントとは別/u);
  assert.match(main, /声の本人確認ではない/u);
  assert.match(main, /長期効果は未実証/u);

  const build = await readFile(
    new URL("../../../scripts/build-web.ps1", import.meta.url),
    "utf8",
  );
  assert.match(build, /"passkey-policy\.mjs"/u);
});
