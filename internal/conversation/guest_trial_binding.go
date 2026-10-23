package conversation

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"unicode/utf8"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
)

// Optional only for the earlier read-first schema, which has never been issued
// by a production route. Dedicated trial consumption always requires Binding.
type guestTrialBinding struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	AppTag     string `json:"app_tag"`
	ExpiresAt  int64  `json:"expires_at_us"`
}

// ValidateGuestTrialState is for the dedicated voice path, before recognition.
// Ordinary/native token validators intentionally continue rejecting trials.
func (agent *vertexAgent) ValidateGuestTrialState(owner guesttrial.Owner, snapshot guesttrial.Snapshot, token string) error {
	_, err := agent.openGuestTrialState(owner, token, snapshot)
	return err
}

func validGuestTrialBinding(b guestTrialBinding, assisted bool, outcome string) bool {
	raw, err := hex.DecodeString(b.ID)
	expected := int64(1)
	if assisted {
		expected++
	}
	if outcome != guestTrialPending {
		expected++
	}
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == b.ID &&
		b.Generation == expected && validCoachControlTag(b.AppTag) && b.ExpiresAt > 0
}

func (agent *vertexAgent) guestTrialAppTag(owner guesttrial.Owner) string {
	if agent == nil || len(agent.continuityKey) != sha256.Size || !validUID(owner.UID) ||
		strings.TrimSpace(owner.UID) != owner.UID || strings.ContainsRune(owner.UID, 0) ||
		len(owner.AppID) == 0 || len(owner.AppID) > 256 || !utf8.ValidString(owner.AppID) ||
		strings.TrimSpace(owner.AppID) != owner.AppID || strings.ContainsRune(owner.AppID, 0) {
		return ""
	}
	mac := hmac.New(sha256.New, agent.continuityKey)
	_, _ = mac.Write([]byte("guest-trial-owner-app-v1\x00" + owner.UID + "\x00" + owner.AppID))
	full := mac.Sum(nil)
	defer wipe(full)
	return base64.RawURLEncoding.EncodeToString(full[:coachContinuityTagBytes])
}

// A snapshot must come from the owner-bound shared service, never the browser.
// Its expiry remains authoritative even when sealing refreshes the outer token.
func (agent *vertexAgent) guestTrialBindingFor(owner guesttrial.Owner, snapshot guesttrial.Snapshot) (guestTrialBinding, error) {
	if agent == nil || agent.codec == nil || snapshot.Version != 1 ||
		snapshot.QuestionID != guesttrial.QuestionID || snapshot.Question != guesttrial.QuestionText ||
		snapshot.Status != guesttrial.Pending {
		return guestTrialBinding{}, ErrInvalidStateToken
	}
	now := agent.codec.now()
	if !snapshot.ExpiresAt.After(now) || snapshot.ExpiresAt.After(now.Add(guesttrial.TTL)) {
		return guestTrialBinding{}, ErrInvalidStateToken
	}
	binding := guestTrialBinding{
		ID: snapshot.ID, Generation: snapshot.Generation,
		AppTag: agent.guestTrialAppTag(owner), ExpiresAt: snapshot.ExpiresAt.UnixMicro(),
	}
	if !validGuestTrialBinding(binding, snapshot.Assisted, string(snapshot.Status)) {
		return guestTrialBinding{}, ErrInvalidStateToken
	}
	return binding, nil
}

// Not connected to public issuance until voice, control and UI are integrated.
// There is intentionally no previous state argument: a new question cannot
// inherit the preceding answer, memory, prompt, proof or adaptation history.
func (agent *vertexAgent) issueGuestTrialState(owner guesttrial.Owner, snapshot guesttrial.Snapshot) (string, error) {
	binding, err := agent.guestTrialBindingFor(owner, snapshot)
	if err != nil {
		return "", err
	}
	state, err := agent.newGuestTransferState()
	if err != nil {
		return "", err
	}
	state.GuestTrial.Binding = &binding
	state.GuestTrial.Assisted = snapshot.Assisted
	return agent.codec.seal(owner.UID, state)
}

// The caller must fetch snapshot from the shared ledger for this owner before
// opening. A valid encrypted token alone cannot prove a current generation.
func (agent *vertexAgent) openGuestTrialState(owner guesttrial.Owner, token string, snapshot guesttrial.Snapshot) (conversationState, error) {
	binding, err := agent.guestTrialBindingFor(owner, snapshot)
	if err != nil {
		return conversationState{}, err
	}
	state, err := agent.codec.open(owner.UID, token)
	if err != nil {
		return conversationState{}, err
	}
	trial := state.GuestTrial
	if trial == nil || trial.Binding == nil || trial.Outcome != guestTrialPending ||
		trial.Assisted != snapshot.Assisted || trial.Binding.ID != binding.ID ||
		trial.Binding.Generation != binding.Generation || trial.Binding.ExpiresAt != binding.ExpiresAt ||
		subtle.ConstantTimeCompare([]byte(trial.Binding.AppTag), []byte(binding.AppTag)) != 1 ||
		trial.Scope != agent.coachQuestionInstanceTag(state.SessionID, guestTransferQuestion) {
		return conversationState{}, ErrInvalidStateToken
	}
	return state, nil
}
