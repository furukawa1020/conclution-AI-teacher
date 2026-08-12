import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  base64urlEncode,
  createPasskeyRegistrationRecoveryLatch,
  decidePasskeyRegistrationRecoveryAction,
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

function memorySessionStorage(backing = new Map()) {
  return {
    getItem(key) {
      return backing.has(key) ? backing.get(key) : null;
    },
    removeItem(key) {
      backing.delete(key);
    },
    setItem(key, value) {
      backing.set(key, String(value));
    },
  };
}

test("registration recovery latch survives reload and clears only for its target account", () => {
  const backing = new Map();
  const storage = memorySessionStorage(backing);
  const targetAccount = `pk_${"A".repeat(32)}`;
  const otherAccount = `pk_${"B".repeat(32)}`;
  const firstPage = createPasskeyRegistrationRecoveryLatch(() => storage);

  assert.equal(firstPage.isPending(), false);
  assert.equal(firstPage.mark("invalid"), false);
  assert.equal(firstPage.mark(targetAccount), true);
  assert.equal(firstPage.isPending(), true);

  const reloadedPage = createPasskeyRegistrationRecoveryLatch(() => storage);
  assert.equal(reloadedPage.isPending(), true);
  assert.equal(reloadedPage.matches(otherAccount), false);
  assert.equal(reloadedPage.matches(targetAccount), true);
  assert.equal(reloadedPage.clear(otherAccount), false);
  assert.equal(reloadedPage.isPending(), true);
  assert.equal(reloadedPage.clear(targetAccount), true);
  assert.equal(reloadedPage.isPending(), false);
});

test("registration recovery latch fails closed when per-tab state is unavailable", () => {
  const unavailable = createPasskeyRegistrationRecoveryLatch(() => {
    throw new DOMException("blocked", "SecurityError");
  });
  const targetAccount = `pk_${"C".repeat(32)}`;

  assert.equal(unavailable.isPending(), true);
  assert.equal(unavailable.matches(targetAccount), false);
  assert.equal(unavailable.mark(targetAccount), false);
  assert.equal(unavailable.clear(targetAccount), false);
});

test("registration recovery forces the target credential without deadlocking on a wrong current user", () => {
  assert.equal(
    decidePasskeyRegistrationRecoveryAction({
      currentAccountMatches: false,
      interactive: false,
      pending: false,
    }),
    "normal",
  );
  assert.equal(
    decidePasskeyRegistrationRecoveryAction({
      currentAccountMatches: true,
      interactive: false,
      pending: true,
    }),
    "verify-current",
  );
  assert.equal(
    decidePasskeyRegistrationRecoveryAction({
      currentAccountMatches: false,
      interactive: false,
      pending: true,
    }),
    "block",
  );
  assert.equal(
    decidePasskeyRegistrationRecoveryAction({
      currentAccountMatches: false,
      interactive: true,
      pending: true,
    }),
    "authenticate",
  );
});

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

test("registration finish boundary preserves cancellation and recovery policy", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const registrationStart = bridge.indexOf("async function registerPasskey(");
  const registrationEnd = bridge.indexOf(
    "async function authenticatePasskey(",
    registrationStart,
  );
  assert.notEqual(registrationStart, -1);
  assert.notEqual(registrationEnd, -1);
  const registration = bridge.slice(registrationStart, registrationEnd);

  const stageDeclaration = registration.indexOf(
    "let registrationFinishStarted = false;",
  );
  const credentialCreate = registration.indexOf(
    "await navigator.credentials.create(",
  );
  const registrationCancellation = registration.indexOf(
    'fail("passkey_registration_cancelled")',
  );
  const credentialEncoding = registration.indexOf(
    "encodedCredential = encodeRegistrationCredential(credential);",
  );
  const stageTransition = registration.indexOf(
    "registrationFinishStarted = true;",
  );
  const finishRequest = registration.indexOf(
    "await passkeyJSON(",
    credentialEncoding,
  );
  const beforeRequest = registration.indexOf("beforeRequest: () =>", finishRequest);
  const recoveryMark = registration.indexOf(
    "passkeyRegistrationRecovery.mark(",
    beforeRequest,
  );
  const finishParsing = registration.indexOf(
    "const finish = parsePasskeyFinish(",
  );
  const customTokenExchange = registration.indexOf(
    "await signInWithCustomToken(",
  );
  const verification = registration.indexOf(
    "await verifyFreshCustomPasskeyUser(",
  );
  const recoveryCatch = registration.indexOf(
    "if (registrationFinishStarted)",
  );
  const preservedCatch = registration.indexOf(
    "if (preservePasskeyBoundaryError(error))",
  );

  assert.ok(stageDeclaration >= 0);
  assert.ok(credentialCreate > stageDeclaration);
  assert.ok(registrationCancellation > credentialCreate);
  assert.ok(credentialEncoding > registrationCancellation);
  assert.ok(finishRequest > credentialEncoding);
  assert.ok(beforeRequest > finishRequest);
  assert.ok(recoveryMark > beforeRequest);
  assert.ok(stageTransition > recoveryMark);
  assert.ok(finishParsing > credentialEncoding);
  assert.ok(customTokenExchange > finishRequest);
  assert.ok(verification > customTokenExchange);
  assert.ok(recoveryCatch > verification);
  assert.ok(preservedCatch > recoveryCatch);
  assert.match(
    registration,
    /failureCode: "passkey_registration_recovery_required"/u,
  );
  assert.match(
    registration,
    /await verifyFreshCustomPasskeyUser\([\s\S]*"passkey_registration_recovery_required"/u,
  );
  assert.match(
    registration,
    /if \(registrationFinishStarted\) \{\s*fail\("passkey_registration_recovery_required"\);/u,
  );
  assert.match(
    registration,
    /error\.message === "passkey_cancelled"[\s\S]*fail\("passkey_registration_cancelled"\)/u,
  );
  assert.match(
    registration,
    /passkeyRegistrationRecovery\.clear\(passkeyAccountUid\(verified\)\)[\s\S]*fail\("passkey_registration_recovery_required"\)/u,
  );

  const jsonStart = bridge.indexOf("async function passkeyJSON(");
  const jsonEnd = bridge.indexOf("async function runPasskeyOperation(", jsonStart);
  const passkeyJson = bridge.slice(jsonStart, jsonEnd);
  const serialization = passkeyJson.indexOf("serializedBody = JSON.stringify(body);");
  const beforeFetchBoundary = passkeyJson.indexOf("beforeRequest();");
  const fetchRequest = passkeyJson.indexOf("response = await fetch(endpoint");
  assert.ok(serialization >= 0);
  assert.ok(beforeFetchBoundary > serialization);
  assert.ok(fetchRequest > beforeFetchBoundary);

  const boundaryStart = bridge.indexOf(
    "function preservePasskeyBoundaryError(",
  );
  const boundaryEnd = bridge.indexOf(
    "async function verifyFreshCustomPasskeyUser(",
    boundaryStart,
  );
  const boundary = bridge.slice(boundaryStart, boundaryEnd);
  assert.match(boundary, /passkey_registration_cancelled/u);
  assert.match(boundary, /passkey_registration_recovery_required/u);

  const explicitStart = bridge.indexOf("async function registerPasskeyAccount(");
  const explicitEnd = bridge.indexOf(
    "async function secureCredentials(",
    explicitStart,
  );
  const explicitRegistration = bridge.slice(explicitStart, explicitEnd);
  assert.match(explicitRegistration, /preservePasskeyBoundaryError\(error\)/u);

  const secureStart = explicitEnd;
  const secureEnd = bridge.indexOf(
    "function primeVoiceTransportConnection(",
    secureStart,
  );
  const secureCredentials = bridge.slice(secureStart, secureEnd);
  assert.match(secureCredentials, /case "passkey_registration_cancelled":/u);
  assert.match(
    secureCredentials,
    /case "passkey_registration_recovery_required":/u,
  );
});

test("replacement passkey flows preserve the old Firebase session until proof succeeds", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const explicitStart = bridge.indexOf("async function registerPasskeyAccount(");
  const secureStart = bridge.indexOf(
    "async function secureCredentials(",
    explicitStart,
  );
  const statusStart = bridge.indexOf("async function getStatus(", secureStart);
  const explicitRegistration = bridge.slice(explicitStart, secureStart);
  const secureCredentials = bridge.slice(secureStart, statusStart);
  assert.doesNotMatch(explicitRegistration, /\bsignOut\b/u);
  assert.doesNotMatch(secureCredentials, /\bsignOut\b/u);

  const explicitCurrentUser = explicitRegistration.indexOf(
    "user = currentAccountUser(auth);",
  );
  const explicitTokenSnapshot = explicitRegistration.indexOf(
    "const tokenResult = await getIdTokenResult(user, false);",
  );
  const explicitDecision = explicitRegistration.indexOf(
    "const action = decidePasskeyAction(",
  );
  const explicitReject = explicitRegistration.indexOf(
    'if (action === "reject")',
  );
  const rejectedIdentity = explicitRegistration.indexOf(
    'fail("identity_verification_failed");',
    explicitReject,
  );
  const explicitIdentityFailure = explicitRegistration.indexOf(
    'error.message !== "identity_verification_failed"',
  );
  const rejectedUserCleared = explicitRegistration.indexOf("user = null;");
  const existingAccount = explicitRegistration.indexOf(
    'fail("passkey_account_exists");',
  );
  const explicitRegister = explicitRegistration.indexOf(
    "const registeredUser = await registerPasskey(auth, appCheckResult.token);",
  );
  assert.ok(explicitCurrentUser >= 0);
  assert.ok(explicitTokenSnapshot > explicitCurrentUser);
  assert.ok(explicitDecision > explicitTokenSnapshot);
  assert.ok(explicitReject > explicitDecision);
  assert.ok(rejectedIdentity > explicitReject);
  assert.ok(explicitIdentityFailure > rejectedIdentity);
  assert.ok(rejectedUserCleared > explicitIdentityFailure);
  assert.ok(existingAccount > rejectedUserCleared);
  assert.ok(explicitRegister > existingAccount);

  const interactiveGuard = secureCredentials.indexOf("!interactive");
  const returningIdentityFailure = secureCredentials.indexOf(
    'error.message !== "identity_verification_failed"',
    interactiveGuard,
  );
  const returningAuthentication = secureCredentials.indexOf(
    "await authenticatePasskey(auth, appCheckResult.token)",
  );
  assert.ok(interactiveGuard >= 0);
  assert.ok(returningIdentityFailure > interactiveGuard);
  assert.ok(returningAuthentication > returningIdentityFailure);
  const finalToken = secureCredentials.indexOf(
    "const idToken = await getIdToken(authorizedUser, false);",
  );
  const boundaryChanged = secureCredentials.indexOf(
    'globalThis.dispatchEvent(new Event("kotae:account-boundary-changed"));',
  );
  const accessConfirmed = secureCredentials.indexOf(
    'globalThis.dispatchEvent(new Event("kotae:account-access-confirmed"));',
  );
  const credentialReturn = secureCredentials.indexOf(
    "return Object.freeze({",
    finalToken,
  );
  assert.ok(finalToken > returningAuthentication);
  assert.ok(boundaryChanged > finalToken);
  assert.ok(accessConfirmed > boundaryChanged);
  assert.ok(credentialReturn > accessConfirmed);
  assert.doesNotMatch(
    secureCredentials.slice(accessConfirmed, credentialReturn),
    /CustomEvent|detail/u,
  );

});

test("ambiguous registration stays target-bound and blocks account-crossing credentials", async () => {
  const bridge = await readFile(
    new URL("../web/firebase-bridge.js", import.meta.url),
    "utf8",
  );
  const registrationStart = bridge.indexOf("async function registerPasskeyAccount(");
  const secureStart = bridge.indexOf("async function secureCredentials(");
  const statusStart = bridge.indexOf("async function getStatus(");
  const registration = bridge.slice(registrationStart, secureStart);
  const secure = bridge.slice(secureStart, statusStart);
  const status = bridge.slice(
    statusStart,
    bridge.indexOf("function classifyMicrophoneError(", statusStart),
  );

  assert.ok(
    registration.indexOf("passkeyRegistrationRecovery.isPending()") <
      registration.indexOf("hasActiveVoiceSession()"),
  );
  assert.match(
    secure,
    /decidePasskeyRegistrationRecoveryAction\([\s\S]*registrationRecoveryAction === "authenticate"[\s\S]*authenticatePasskey/u,
  );
  assert.match(
    secure,
    /registrationRecoveryPending[\s\S]*passkeyRegistrationRecovery\.clear\(accountUid\)/u,
  );
  const boundaryEvent = secure.indexOf('new Event("kotae:account-boundary-changed")');
  const boundaryFailure = secure.indexOf('fail("account_boundary_changed")');
  const recoveryFailure = secure.indexOf(
    'fail("passkey_registration_recovery_required")',
    boundaryFailure,
  );
  const accessRefresh = secure.indexOf(
    'new Event("kotae:account-access-confirmed")',
  );
  const credentialReturn = secure.indexOf("return Object.freeze({", accessRefresh);
  assert.ok(boundaryEvent >= 0);
  assert.ok(boundaryFailure > boundaryEvent);
  assert.ok(recoveryFailure > boundaryFailure);
  assert.ok(accessRefresh > recoveryFailure);
  assert.ok(credentialReturn > accessRefresh);
  assert.match(
    status,
    /passkeyRegistrationRecovery\.isPending\(\)[\s\S]*state: "passkey-registration-recovery-required"/u,
  );
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
  assert.match(
    main,
    /Some\("passkey_authentication_failed"\) => PASSKEY_AUTHENTICATION_FAILED_COPY/u,
  );
  assert.match(
    main,
    /Some\("passkey_registration_failed"\) => PASSKEY_REGISTRATION_FAILED_COPY/u,
  );
  assert.match(
    main,
    /Some\("passkey_registration_cancelled"\) => PASSKEY_REGISTRATION_CANCELLED_COPY/u,
  );
  assert.match(
    main,
    /Some\("passkey_registration_recovery_required"\)[\s\S]*PASSKEY_REGISTRATION_RECOVERY_REQUIRED_COPY/u,
  );
  assert.doesNotMatch(
    main,
    /Some\("passkey_registration_failed"\) \| Some\("passkey_authentication_failed"\)/u,
  );
  assert.match(main, /声の本人確認ではない/u);
  assert.match(main, /長期効果は未実証/u);
  const accessListenerStart = main.indexOf(
    "pub fn install_account_access_refresh_listener(",
  );
  const accessListenerEnd = main.indexOf(
    "pub fn install_account_boundary_changed_listener(",
    accessListenerStart,
  );
  const accessListener = main.slice(accessListenerStart, accessListenerEnd);
  assert.ok(accessListenerStart >= 0);
  assert.ok(accessListenerEnd > accessListenerStart);
  assert.match(accessListener, /"kotae:account-access-confirmed"/u);
  assert.match(accessListener, /cloud_status_refresh\.set\(next\)/u);
  assert.doesNotMatch(accessListener, /detail|Reflect|get/u);
  assert.doesNotMatch(main, /cloud_state_after_confirmed_access/u);

  const boundaryListenerStart = accessListenerEnd;
  const boundaryListenerEnd = main.indexOf(
    "pub fn focus_element(",
    boundaryListenerStart,
  );
  const boundaryListener = main.slice(boundaryListenerStart, boundaryListenerEnd);
  assert.match(boundaryListener, /"kotae:account-boundary-changed"/u);
  for (const reset of [
    /generation\.set\(next\)/u,
    /stop_session_js\(\)/u,
    /session_state\.set\(String::new\(\)\)/u,
    /detected_domain\.set\(String::new\(\)\)/u,
    /route\.set\(String::new\(\)\)/u,
    /coach_state\.set\(CoachState::NONE\)/u,
    /research_records\.set\(Vec::new\(\)\)/u,
    /document_info\.set\(None\)/u,
    /caption\.set\(None\)/u,
    /voice_state\.set\(VoiceState::Error\(ACCOUNT_BOUNDARY_CHANGED_COPY\)\)/u,
  ]) {
    assert.match(boundaryListener, reset);
  }
  const generationAt = boundaryListener.indexOf("generation.set(next)");
  const stopAt = boundaryListener.indexOf("stop_session_js()");
  const sessionClearAt = boundaryListener.indexOf(
    "session_state.set(String::new())",
  );
  const boundaryErrorAt = boundaryListener.indexOf(
    "voice_state.set(VoiceState::Error(ACCOUNT_BOUNDARY_CHANGED_COPY))",
  );
  assert.ok(generationAt >= 0);
  assert.ok(stopAt > generationAt);
  assert.ok(sessionClearAt > stopAt);
  assert.ok(boundaryErrorAt > sessionClearAt);

  const build = await readFile(
    new URL("../../../scripts/build-web.ps1", import.meta.url),
    "utf8",
  );
  assert.match(build, /"passkey-policy\.mjs"/u);
});
