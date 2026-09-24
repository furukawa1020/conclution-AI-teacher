package initiativescheduler

import (
	"errors"
	"sync"
)

const maximumPublicationLeaseMS int64 = 30_000

var errPublicationAuthority = errors.New("initiative publication authority rejected")

// PublicationAuthority owns a content-free, single-use right to make one
// prepared initiative audible. It deliberately stores no text or PCM. The
// caller must present the same user, goal, turn and finite action at commit.
// Abort is irreversible, including when it races with a late provider result.
type PublicationAuthority struct {
	mu         sync.Mutex
	capability [16]byte
	prepared   *Lease
	committed  bool
	aborted    bool
}

func NewPublicationAuthority(capability [16]byte) (*PublicationAuthority, error) {
	if capability == ([16]byte{}) {
		return nil, errPublicationAuthority
	}
	return &PublicationAuthority{capability: capability}, nil
}

func (authority *PublicationAuthority) Prepare(
	nowMS int64,
	lifetimeMS int64,
	userGeneration uint64,
	goalGeneration uint64,
	turnGeneration uint64,
	action Action,
) (Lease, error) {
	if authority == nil || nowMS < 0 || lifetimeMS <= 0 ||
		lifetimeMS > maximumPublicationLeaseMS || nowMS > int64(^uint64(0)>>1)-lifetimeMS ||
		userGeneration == 0 || goalGeneration == 0 || turnGeneration == 0 ||
		!validAction(action) {
		return Lease{}, errPublicationAuthority
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.aborted || authority.committed || authority.prepared != nil {
		return Lease{}, errPublicationAuthority
	}
	lease := Lease{
		SessionCapability: authority.capability,
		UserGeneration:    userGeneration,
		GoalGeneration:    goalGeneration,
		TurnGeneration:    turnGeneration,
		Action:            action,
		ExpiresMS:         nowMS + lifetimeMS,
	}
	authority.prepared = &lease
	return lease, nil
}

func (authority *PublicationAuthority) Commit(
	nowMS int64,
	lease Lease,
	userGeneration uint64,
	goalGeneration uint64,
	turnGeneration uint64,
	action Action,
) error {
	if authority == nil || nowMS < 0 || userGeneration == 0 ||
		goalGeneration == 0 || turnGeneration == 0 || !validAction(action) {
		return errPublicationAuthority
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.aborted || authority.committed || authority.prepared == nil ||
		lease != *authority.prepared || lease.SessionCapability != authority.capability ||
		lease.UserGeneration != userGeneration || lease.GoalGeneration != goalGeneration ||
		lease.TurnGeneration != turnGeneration || lease.Action != action ||
		lease.ExpiresMS < nowMS {
		return errPublicationAuthority
	}
	authority.committed = true
	authority.prepared = nil
	return nil
}

// Abort permanently revokes both a prepared lease and any future prepare.
// It is intentionally idempotent so every cancellation path can call it.
func (authority *PublicationAuthority) Abort() {
	if authority == nil {
		return
	}
	authority.mu.Lock()
	authority.aborted = true
	authority.prepared = nil
	authority.mu.Unlock()
}
