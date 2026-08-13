# Passkey credential lifecycle boundary

This document describes the internal storage primitive introduced as the first
part of the Passkey credential lifecycle. It is deliberately not a public
credential-management feature yet.

## What exists

The Passkey store can list and revoke credentials for one exact pseudonymous
UID. A list result contains only:

- a canonical `CredentialReference`;
- the credential creation time;
- the last successful-use time.

`CredentialReference` is the unpadded base64url encoding of
`SHA-256(raw credential ID)`. Parsing is strict: padding, whitespace, standard
base64 characters, non-canonical trailing bits, and values that are not exactly
a 32-byte digest are rejected. Firestore uses this value directly as the
existing `passkey_credentials_v1` document ID; it is never hashed a second
time. The stable digest can be correlated, is not a bearer capability, and is
not an authorization boundary. It may be returned only after an exact-account
management request has been independently authorized.

The list result never returns the raw credential ID, public key, user handle,
AAGUID, transports, signature counter, or stored credential JSON.

## Atomic revoke invariant

Before changing state, both stores validate the account's complete credential
array, including every raw-ID-derived reference and uniqueness constraint, and
validate every referenced credential index record. Missing, duplicate,
malformed, cross-account, or inconsistent state fails closed without mutation.
Listing performs the same complete validation before returning any result.

The in-memory store performs validation, user-list replacement, and credential
deletion under one mutex. The Firestore store reads the user document and the
complete referenced credential-document set before writing, then replaces the
user credential array and deletes the target credential document in one
transaction. Because every revoke transaction reads and writes the same user
document, concurrent attempts from a two-credential account can produce only
one successful revoke; the retry observes one remaining credential and returns
`ErrLastCredential`.

The final credential cannot be revoked through this primitive. Account deletion
is a separate operation authorized from the verified principal only. It deletes
the complete credential relation, handle reservation, and user record in one
transaction, writes a digest-keyed 24-hour retry marker, then deletes the
Firebase Auth user. A transient Auth failure can therefore be retried without
recreating or trusting deleted credential state.

## What does not exist yet

List, existing-account credential addition, revoke, and account deletion have
HTTP and client boundaries. Every management route requires a passkey-derived
principal verified within five minutes; guest identity and request-supplied UID
are rejected. Account deletion additionally requires an exact second-step
confirmation phrase and returns no account data.

Account recovery and management audit events remain unimplemented. Recovery
must not be inferred from an ordinary Firebase token because that would weaken
the passkey ownership boundary.

This internal primitive therefore does not close the full Passkey credential
lifecycle issue by itself.
