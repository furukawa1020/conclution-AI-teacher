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

The final credential cannot be revoked through this primitive. Deleting the
last credential is account deletion and requires a separate, explicit flow that
also handles the user record, handle reservation, Firebase account, recovery
policy, and audit boundary.

## What does not exist yet

There is no HTTP route or client UI for list or revoke. Existing-account
credential addition, recovery, account deletion, a non-bypassable recent-user-
verification boundary for management operations, and management audit events
also remain unimplemented. Until those boundaries are complete, callers must
not expose this store API directly.

This internal primitive therefore does not close the full Passkey credential
lifecycle issue by itself.
