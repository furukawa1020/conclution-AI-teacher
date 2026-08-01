const BASE64URL_ALPHABET =
  "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
const BASE64URL_INDEX = new Map(
  [...BASE64URL_ALPHABET].map((character, index) => [character, index]),
);

export const PASSKEY_AUTH_METHOD = "passkey-v1";
export const PASSKEY_FRESHNESS_SECONDS = 5 * 60;

const REGISTRATION_FINISH_PENDING_KEY =
  "kotae.passkey-registration-finish-pending.v1";

function validPasskeyAccountId(value) {
  return typeof value === "string" && /^pk_[A-Za-z0-9_-]{32}$/u.test(value);
}

export function createPasskeyRegistrationRecoveryLatch(storageProvider) {
  if (typeof storageProvider !== "function") invalid();
  let pendingAccountIdInMemory = null;

  const storage = () => {
    const candidate = storageProvider();
    if (
      !candidate ||
      typeof candidate.getItem !== "function" ||
      typeof candidate.removeItem !== "function" ||
      typeof candidate.setItem !== "function"
    ) {
      invalid();
    }
    return candidate;
  };

  return Object.freeze({
    clear(accountId) {
      if (!validPasskeyAccountId(accountId)) return false;
      try {
        const target = storage();
        const pendingAccountId =
          pendingAccountIdInMemory ??
          target.getItem(REGISTRATION_FINISH_PENDING_KEY);
        if (pendingAccountId !== accountId) return false;
        target.removeItem(REGISTRATION_FINISH_PENDING_KEY);
        if (target.getItem(REGISTRATION_FINISH_PENDING_KEY) !== null) {
          return false;
        }
        pendingAccountIdInMemory = null;
        return true;
      } catch {
        return false;
      }
    },
    isPending() {
      if (pendingAccountIdInMemory !== null) return true;
      try {
        const stored = storage().getItem(REGISTRATION_FINISH_PENDING_KEY);
        if (stored === null) return false;
        pendingAccountIdInMemory = stored;
        return true;
      } catch {
        // Without readable per-tab state, another registration cannot be
        // proven distinct from an earlier finish whose response was lost.
        return true;
      }
    },
    matches(accountId) {
      if (!validPasskeyAccountId(accountId)) return false;
      try {
        const pendingAccountId =
          pendingAccountIdInMemory ??
          storage().getItem(REGISTRATION_FINISH_PENDING_KEY);
        return pendingAccountId === accountId;
      } catch {
        return false;
      }
    },
    mark(accountId) {
      if (!validPasskeyAccountId(accountId)) return false;
      if (pendingAccountIdInMemory !== null) {
        return pendingAccountIdInMemory === accountId;
      }
      try {
        const target = storage();
        if (target.getItem(REGISTRATION_FINISH_PENDING_KEY) !== null) return false;
        target.setItem(REGISTRATION_FINISH_PENDING_KEY, accountId);
        if (target.getItem(REGISTRATION_FINISH_PENDING_KEY) !== accountId) return false;
        pendingAccountIdInMemory = accountId;
        return true;
      } catch {
        return false;
      }
    },
  });
}

export function decidePasskeyRegistrationRecoveryAction({
  currentAccountMatches,
  interactive,
  pending,
}) {
  if (
    typeof currentAccountMatches !== "boolean" ||
    typeof interactive !== "boolean" ||
    typeof pending !== "boolean"
  ) {
    invalid();
  }
  if (!pending) return "normal";
  if (currentAccountMatches) return "verify-current";
  return interactive ? "authenticate" : "block";
}

const MAX_CHALLENGE_BYTES = 1024;
const MAX_CREDENTIAL_ID_BYTES = 1024;
const MAX_CREDENTIAL_RESPONSE_BYTES = 256 * 1024;
const MAX_EXTENSION_JSON_BYTES = 8 * 1024;
const ALLOWED_TRANSPORTS = new Set([
  "ble",
  "hybrid",
  "internal",
  "nfc",
  "smart-card",
  "usb",
]);
const ALLOWED_HINTS = new Set(["client-device", "hybrid", "security-key"]);

function invalid() {
  throw new TypeError("passkey_payload_invalid");
}

function isPlainRecord(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function plainRecord(value) {
  if (!isPlainRecord(value)) invalid();
  return value;
}

function exactKeys(value, allowed, required = []) {
  const record = plainRecord(value);
  const allowedSet = new Set(allowed);
  for (const key of Object.keys(record)) {
    if (!allowedSet.has(key)) invalid();
  }
  for (const key of required) {
    if (!Object.hasOwn(record, key)) invalid();
  }
  return record;
}

function boundedText(value, maximum, { allowEmpty = false } = {}) {
  if (
    typeof value !== "string" ||
    value.length > maximum ||
    (!allowEmpty && value.length === 0) ||
    /[\u0000-\u001f\u007f]/u.test(value)
  ) {
    invalid();
  }
  return value;
}

function closedValue(value, allowed) {
  if (!allowed.has(value)) invalid();
  return value;
}

function optionalTimeout(value) {
  if (value === undefined) return undefined;
  if (!Number.isInteger(value) || value <= 0 || value > 600_000) invalid();
  return value;
}

export function strictBase64urlDecode(
  value,
  { minBytes = 1, maxBytes = MAX_CREDENTIAL_RESPONSE_BYTES } = {},
) {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length % 4 === 1 ||
    !Number.isInteger(minBytes) ||
    !Number.isInteger(maxBytes) ||
    minBytes < 0 ||
    maxBytes < minBytes
  ) {
    invalid();
  }

  const outputLength = Math.floor((value.length * 6) / 8);
  if (outputLength < minBytes || outputLength > maxBytes) invalid();

  const output = new Uint8Array(outputLength);
  let accumulator = 0;
  let availableBits = 0;
  let outputOffset = 0;
  for (const character of value) {
    const sextet = BASE64URL_INDEX.get(character);
    if (sextet === undefined) invalid();
    accumulator = (accumulator << 6) | sextet;
    availableBits += 6;
    if (availableBits >= 8) {
      availableBits -= 8;
      output[outputOffset] = (accumulator >>> availableBits) & 0xff;
      outputOffset += 1;
      accumulator &= (1 << availableBits) - 1;
    }
  }
  if (outputOffset !== outputLength || accumulator !== 0) invalid();
  return output;
}

function bytesView(value, { minBytes = 1, maxBytes }) {
  let view;
  if (value instanceof ArrayBuffer) {
    view = new Uint8Array(value);
  } else if (ArrayBuffer.isView(value)) {
    view = new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  } else {
    invalid();
  }
  if (view.byteLength < minBytes || view.byteLength > maxBytes) invalid();
  return view;
}

export function base64urlEncode(
  value,
  { minBytes = 1, maxBytes = MAX_CREDENTIAL_RESPONSE_BYTES } = {},
) {
  const bytes = bytesView(value, { minBytes, maxBytes });
  let encoded = "";
  for (let offset = 0; offset < bytes.length; offset += 3) {
    const first = bytes[offset];
    const hasSecond = offset + 1 < bytes.length;
    const hasThird = offset + 2 < bytes.length;
    const second = hasSecond ? bytes[offset + 1] : 0;
    const third = hasThird ? bytes[offset + 2] : 0;
    encoded += BASE64URL_ALPHABET[first >>> 2];
    encoded += BASE64URL_ALPHABET[((first & 0x03) << 4) | (second >>> 4)];
    if (hasSecond) {
      encoded += BASE64URL_ALPHABET[((second & 0x0f) << 2) | (third >>> 6)];
    }
    if (hasThird) {
      encoded += BASE64URL_ALPHABET[third & 0x3f];
    }
  }
  return encoded;
}

function safeJSONValue(value, depth = 0) {
  if (depth > 5) invalid();
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "number") {
    if (!Number.isFinite(value)) invalid();
    return value;
  }
  if (typeof value === "string") return boundedText(value, 512, { allowEmpty: true });
  if (Array.isArray(value)) {
    if (value.length > 32) invalid();
    return value.map((entry) => safeJSONValue(entry, depth + 1));
  }
  const record = plainRecord(value);
  const keys = Object.keys(record);
  if (keys.length > 32) invalid();
  const clone = Object.create(null);
  for (const key of keys) {
    if (
      key === "__proto__" ||
      key === "constructor" ||
      key === "prototype" ||
      !/^[A-Za-z0-9_.-]{1,64}$/u.test(key)
    ) {
      invalid();
    }
    clone[key] = safeJSONValue(record[key], depth + 1);
  }
  return clone;
}

function safeExtensions(value) {
  if (value === undefined) return undefined;
  const clone = safeJSONValue(value);
  if (!isPlainRecord(clone)) invalid();
  const serialized = JSON.stringify(clone);
  if (serialized.length > MAX_EXTENSION_JSON_BYTES) invalid();
  return clone;
}

function credentialDescriptor(value) {
  const descriptor = exactKeys(value, ["id", "transports", "type"], [
    "id",
    "type",
  ]);
  if (descriptor.type !== "public-key") invalid();
  const normalized = {
    id: strictBase64urlDecode(descriptor.id, {
      minBytes: 1,
      maxBytes: MAX_CREDENTIAL_ID_BYTES,
    }),
    type: "public-key",
  };
  if (descriptor.transports !== undefined) {
    if (!Array.isArray(descriptor.transports) || descriptor.transports.length > 6) {
      invalid();
    }
    const transports = descriptor.transports.map((transport) =>
      closedValue(transport, ALLOWED_TRANSPORTS),
    );
    if (new Set(transports).size !== transports.length) invalid();
    normalized.transports = transports;
  }
  return normalized;
}

function credentialDescriptors(value) {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length > 16) invalid();
  const descriptors = value.map(credentialDescriptor);
  const identifiers = descriptors.map((entry) => base64urlEncode(entry.id));
  if (new Set(identifiers).size !== identifiers.length) invalid();
  return descriptors;
}

function relyingParty(value) {
  const entity = exactKeys(value, ["id", "name"], ["id", "name"]);
  const id = boundedText(entity.id, 253);
  if (!/^[A-Za-z0-9.-]+$/u.test(id)) invalid();
  return Object.freeze({ id, name: boundedText(entity.name, 128) });
}

function userEntity(value) {
  const entity = exactKeys(value, ["displayName", "id", "name"], [
    "displayName",
    "id",
    "name",
  ]);
  return Object.freeze({
    displayName: boundedText(entity.displayName, 128),
    id: strictBase64urlDecode(entity.id, { minBytes: 1, maxBytes: 64 }),
    name: boundedText(entity.name, 128),
  });
}

function credentialParameters(value) {
  if (!Array.isArray(value) || value.length === 0 || value.length > 16) invalid();
  return value.map((candidate) => {
    const parameter = exactKeys(candidate, ["alg", "type"], ["alg", "type"]);
    if (
      parameter.type !== "public-key" ||
      !Number.isInteger(parameter.alg) ||
      parameter.alg < -2_147_483_648 ||
      parameter.alg > 2_147_483_647
    ) {
      invalid();
    }
    return Object.freeze({ alg: parameter.alg, type: "public-key" });
  });
}

function authenticatorSelection(value) {
  if (value === undefined) return undefined;
  const selection = exactKeys(value, [
    "authenticatorAttachment",
    "requireResidentKey",
    "residentKey",
    "userVerification",
  ]);
  const normalized = Object.create(null);
  if (selection.authenticatorAttachment !== undefined) {
    normalized.authenticatorAttachment = closedValue(
      selection.authenticatorAttachment,
      new Set(["cross-platform", "platform"]),
    );
  }
  if (selection.requireResidentKey !== undefined) {
    if (selection.requireResidentKey !== true) invalid();
    normalized.requireResidentKey = true;
  }
  if (selection.residentKey !== undefined) {
    normalized.residentKey = closedValue(
      selection.residentKey,
      new Set(["required"]),
    );
  }
  if (selection.userVerification !== undefined) {
    normalized.userVerification = closedValue(
      selection.userVerification,
      new Set(["required"]),
    );
  }
  if (
    normalized.requireResidentKey !== true ||
    normalized.residentKey !== "required" ||
    normalized.userVerification !== "required"
  ) {
    invalid();
  }
  return normalized;
}

function optionalHints(value) {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length > 3) invalid();
  const hints = value.map((hint) => closedValue(hint, ALLOWED_HINTS));
  if (new Set(hints).size !== hints.length) invalid();
  return hints;
}

function optionalAttestationFormats(value) {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length > 8) invalid();
  const allowed = new Set([
    "android-key",
    "android-safetynet",
    "apple",
    "compound",
    "fido-u2f",
    "none",
    "packed",
    "tpm",
  ]);
  const formats = value.map((format) => closedValue(format, allowed));
  if (new Set(formats).size !== formats.length) invalid();
  return formats;
}

function beginEnvelope(value) {
  const begin = exactKeys(value, ["ceremonyId", "options"], [
    "ceremonyId",
    "options",
  ]);
  strictBase64urlDecode(begin.ceremonyId, { minBytes: 16, maxBytes: 96 });
  const options = exactKeys(begin.options, ["mediation", "publicKey"], [
    "publicKey",
  ]);
  let mediation;
  if (options.mediation !== undefined) {
    mediation = closedValue(
      options.mediation,
      new Set(["conditional", "optional", "required", "silent"]),
    );
  }
  return { begin, mediation, publicKey: plainRecord(options.publicKey) };
}

export function decodeRegistrationBegin(value) {
  const { begin, mediation, publicKey } = beginEnvelope(value);
  exactKeys(
    publicKey,
    [
      "attestation",
      "attestationFormats",
      "authenticatorSelection",
      "challenge",
      "excludeCredentials",
      "extensions",
      "hints",
      "pubKeyCredParams",
      "rp",
      "timeout",
      "user",
    ],
    [
      "attestation",
      "authenticatorSelection",
      "challenge",
      "pubKeyCredParams",
      "rp",
      "user",
    ],
  );
  if (publicKey.extensions !== undefined) invalid();
  const normalized = {
    attestation: closedValue(publicKey.attestation, new Set(["none"])),
    authenticatorSelection: authenticatorSelection(
      publicKey.authenticatorSelection,
    ),
    challenge: strictBase64urlDecode(publicKey.challenge, {
      minBytes: 16,
      maxBytes: MAX_CHALLENGE_BYTES,
    }),
    pubKeyCredParams: credentialParameters(publicKey.pubKeyCredParams),
    rp: relyingParty(publicKey.rp),
    user: userEntity(publicKey.user),
  };
  const timeout = optionalTimeout(publicKey.timeout);
  const excludeCredentials = credentialDescriptors(publicKey.excludeCredentials);
  const hints = optionalHints(publicKey.hints);
  const attestationFormats = optionalAttestationFormats(
    publicKey.attestationFormats,
  );
  if (timeout !== undefined) normalized.timeout = timeout;
  if (excludeCredentials !== undefined) {
    normalized.excludeCredentials = excludeCredentials;
  }
  if (hints !== undefined) normalized.hints = hints;
  if (attestationFormats !== undefined) {
    normalized.attestationFormats = attestationFormats;
  }
  const options = { publicKey: normalized };
  if (mediation !== undefined) options.mediation = mediation;
  return Object.freeze({ ceremonyId: begin.ceremonyId, options });
}

export function decodeAuthenticationBegin(value) {
  const { begin, mediation, publicKey } = beginEnvelope(value);
  exactKeys(
    publicKey,
    [
      "allowCredentials",
      "challenge",
      "extensions",
      "hints",
      "rpId",
      "timeout",
      "userVerification",
    ],
    ["challenge", "userVerification"],
  );
  if (publicKey.extensions !== undefined) invalid();
  if (publicKey.userVerification !== "required") invalid();
  const normalized = {
    challenge: strictBase64urlDecode(publicKey.challenge, {
      minBytes: 16,
      maxBytes: MAX_CHALLENGE_BYTES,
    }),
    userVerification: "required",
  };
  if (publicKey.rpId !== undefined) {
    const rpId = boundedText(publicKey.rpId, 253);
    if (!/^[A-Za-z0-9.-]+$/u.test(rpId)) invalid();
    normalized.rpId = rpId;
  }
  const timeout = optionalTimeout(publicKey.timeout);
  const allowCredentials = credentialDescriptors(publicKey.allowCredentials);
  const hints = optionalHints(publicKey.hints);
  if (timeout !== undefined) normalized.timeout = timeout;
  if (allowCredentials !== undefined) {
    normalized.allowCredentials = allowCredentials;
  }
  if (hints !== undefined) normalized.hints = hints;
  const options = { publicKey: normalized };
  if (mediation !== undefined) options.mediation = mediation;
  return Object.freeze({ ceremonyId: begin.ceremonyId, options });
}

function extensionResults(credential) {
  if (typeof credential.getClientExtensionResults !== "function") invalid();
  const results =
    safeExtensions(credential.getClientExtensionResults()) ?? Object.create(null);
  if (Object.keys(results).length !== 0) invalid();
  return results;
}

function normalizedCredentialBase(credential) {
  if (credential === null || typeof credential !== "object") invalid();
  if (credential.type !== "public-key") invalid();
  const rawId = base64urlEncode(credential.rawId, {
    minBytes: 1,
    maxBytes: MAX_CREDENTIAL_ID_BYTES,
  });
  if (credential.id !== rawId) invalid();
  let authenticatorAttachment;
  if (credential.authenticatorAttachment !== null && credential.authenticatorAttachment !== undefined) {
    authenticatorAttachment = closedValue(
      credential.authenticatorAttachment,
      new Set(["cross-platform", "platform"]),
    );
  }
  const normalized = {
    clientExtensionResults: extensionResults(credential),
    id: rawId,
    rawId,
    type: "public-key",
  };
  if (authenticatorAttachment !== undefined) {
    normalized.authenticatorAttachment = authenticatorAttachment;
  }
  return normalized;
}

export function encodeRegistrationCredential(credential) {
  const normalized = normalizedCredentialBase(credential);
  const response = credential.response;
  if (response === null || typeof response !== "object") invalid();
  const transports =
    typeof response.getTransports === "function" ? response.getTransports() : [];
  if (!Array.isArray(transports) || transports.length > 6) invalid();
  const normalizedTransports = transports.map((transport) =>
    closedValue(transport, ALLOWED_TRANSPORTS),
  );
  if (new Set(normalizedTransports).size !== normalizedTransports.length) invalid();
  return Object.freeze({
    ...normalized,
    response: Object.freeze({
      attestationObject: base64urlEncode(response.attestationObject, {
        minBytes: 1,
        maxBytes: MAX_CREDENTIAL_RESPONSE_BYTES,
      }),
      clientDataJSON: base64urlEncode(response.clientDataJSON, {
        minBytes: 1,
        maxBytes: 16 * 1024,
      }),
      transports: normalizedTransports,
    }),
  });
}

export function encodeAuthenticationCredential(credential) {
  const normalized = normalizedCredentialBase(credential);
  const response = credential.response;
  if (response === null || typeof response !== "object") invalid();
  let userHandle = null;
  if (response.userHandle !== null && response.userHandle !== undefined) {
    userHandle = base64urlEncode(response.userHandle, {
      minBytes: 1,
      maxBytes: 64,
    });
  }
  return Object.freeze({
    ...normalized,
    response: Object.freeze({
      authenticatorData: base64urlEncode(response.authenticatorData, {
        minBytes: 1,
        maxBytes: 4096,
      }),
      clientDataJSON: base64urlEncode(response.clientDataJSON, {
        minBytes: 1,
        maxBytes: 16 * 1024,
      }),
      signature: base64urlEncode(response.signature, {
        minBytes: 1,
        maxBytes: 8192,
      }),
      userHandle,
    }),
  });
}

export function parsePasskeyFinish(value) {
  const finish = exactKeys(value, ["authMethod", "customToken"], [
    "authMethod",
    "customToken",
  ]);
  if (finish.authMethod !== PASSKEY_AUTH_METHOD) invalid();
  const customToken = boundedText(finish.customToken, 16 * 1024);
  if (/\s/u.test(customToken)) invalid();
  return Object.freeze({
    authMethod: PASSKEY_AUTH_METHOD,
    customToken,
  });
}

export function decidePasskeyAction(
  { accountVerified, authMethod, passkeyAtSeconds, provider },
  nowSeconds,
) {
  if (!Number.isSafeInteger(nowSeconds) || nowSeconds < 0) invalid();
  if (
    provider !== "custom" ||
    accountVerified !== true ||
    authMethod !== PASSKEY_AUTH_METHOD
  ) {
    return "reject";
  }
  if (!Number.isSafeInteger(passkeyAtSeconds) || passkeyAtSeconds < 0) {
    return "authentication";
  }
  const age = nowSeconds - passkeyAtSeconds;
  if (age >= -30 && age <= PASSKEY_FRESHNESS_SECONDS) {
    return "reuse";
  }
  return "authentication";
}

export function isPasskeyCancellation(error) {
  return Boolean(
    error &&
      typeof error === "object" &&
      (error.name === "AbortError" || error.name === "NotAllowedError"),
  );
}
