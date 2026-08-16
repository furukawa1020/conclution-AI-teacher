# ASR finite-sample risk boundary

Issue #121 introduces a content-free boundary between speech recognition and
semantic inference. The boundary receives only whether provider confidence was
observed, its finite value, or a server-authored Native final-caption commit.
It must not receive audio, transcripts, prompts, user identifiers, device
identifiers, or inferred personal attributes.

## Decisions

- `accept`: the observation may cross into semantic inference.
- `reobserve`: coverage is missing or the measured nonconformity is outside the
  calibrated boundary. The transcript must not cross into the model.
- `reject`: the evidence is contradictory, non-finite, or outside its type.

The production bootstrap policy preserves the pre-existing measured confidence
boundary of 0.65. This is an operational compatibility policy, **not** a
statistical coverage claim. Provider confidence encoded as zero remains usable
only in bootstrap mode and is marked `coverage-unavailable`. Once a validated
calibration artifact is active, missing confidence is always `reobserve`.

Native Audio captions do not expose the same confidence value. They can be
accepted only through the separate `NativeFinalCommitted` capability created by
the server after final caption and deterministic route commit. No client JSON or
WebSocket field can grant this capability, and mixing it with confidence
evidence is rejected.

## Calibration artifact

An artifact fixes all of the following before a controller is constructed:

- schema and policy version;
- exact evidence bucket;
- alpha in integer parts-per-million;
- sample count and sorted integer nonconformity scores;
- canonical SHA-256 digest over every field and score.

The split-conformal threshold uses the one-indexed ceiling rank
`ceil((n + 1) * (1 - alpha))`. If that rank does not exist in the finite sample,
the artifact is invalid; the implementation never rounds down to manufacture a
coverage claim. Unknown buckets, count mismatch, unsorted or out-of-range
scores, and digest mismatch also fail closed.

This tranche does not claim whispered-Japanese hallucination coverage. That
claim remains blocked on the licensed, digest-fixed corpus and unseen
device/room/speaker evaluation in Issues #97 and #117. A synthetic test fixture
tests the mathematics and integrity contract only; it is never packaged as a
production calibration population.
