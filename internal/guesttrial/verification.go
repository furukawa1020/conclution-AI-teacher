package guesttrial

import "context"

// Verdict is an internal verifier result, not a request field. Verification
// failures must return an error, not Unverified: a provider outage says nothing
// about the person's answer.
type Verdict uint8

const (
	VerdictAnswerFirst Verdict = iota + 1
	VerdictUnverified
)

type Verifier func(context.Context, Snapshot) (Verdict, error)

// Evaluate is deliberately absent from Control, the HTTP control capability.
// Its caller must supply an independent verifier of finalized voice for the
// exact snapshot it receives. This method neither transcribes nor judges speech.
//
// Expensive verification is outside the store transaction, so Firestore retries
// cannot repeat a model call. Only a CAS against the same pending generation
// can publish the result. A racing help/stop/completion invalidates the work.
func (s *Service) Evaluate(ctx context.Context, owner Owner, id string, generation int64, verify Verifier) (Snapshot, error) {
	if verify == nil || generation < 1 || generation > 2 {
		return Snapshot{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	key, err := s.documentKey(owner, id)
	if err != nil {
		return Snapshot{}, err
	}
	record, err := s.store.Read(ctx, key, s.now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	if record.Status != Pending || record.Generation != generation {
		return Snapshot{}, ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	verdict, err := verify(ctx, snapshot(id, record))
	if err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var event Event
	switch verdict {
	case VerdictAnswerFirst:
		event = CompleteVerified
	case VerdictUnverified:
		event = CompleteUnverified
	default:
		return Snapshot{}, ErrInvalid
	}
	// Re-evaluate time at commit; verification does not extend the trial TTL.
	committed, err := s.store.Apply(ctx, key, generation, event, s.now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(id, committed), nil
}
