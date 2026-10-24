package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"github.com/furukawa1020/conclution-ai-teacher/internal/guesttrial"
	"github.com/furukawa1020/conclution-ai-teacher/internal/httpapi"
	"github.com/furukawa1020/conclution-ai-teacher/internal/speechio"
	"github.com/furukawa1020/conclution-ai-teacher/internal/voiceflow"
)

type guestTrialRuntime struct {
	control guesttrial.Control
	voice   httpapi.GuestTrialVoiceService
	state   httpapi.GuestTrialStateIssuer
}

// All three capabilities are installed together or none are. Production calls
// this with a shared Firestore store, never an instance-local ledger. TTL/rules
// must be verified before enabling the explicit deployment switch.
func connectGuestTrial(enabled bool, rootKey []byte, store guesttrial.Store, speech speechio.ExplicitPCM16Transcriber, agent any) (guestTrialRuntime, error) {
	if !enabled {
		return guestTrialRuntime{}, nil
	}
	verifier, verifierOK := agent.(voiceflow.GuestTrialVoiceVerifier)
	issuer, issuerOK := agent.(httpapi.GuestTrialStateIssuer)
	if len(rootKey) != sha256.Size || store == nil || speech == nil || !verifierOK || !issuerOK {
		return guestTrialRuntime{}, errors.New("guest trial composition is incomplete")
	}
	mac := hmac.New(sha256.New, rootKey)
	_, _ = mac.Write([]byte("kotae-guest-trial-ledger-key-v1"))
	key := mac.Sum(nil)
	defer clear(key)
	service, err := guesttrial.NewService(store, key)
	if err != nil {
		return guestTrialRuntime{}, err
	}
	pipeline, err := voiceflow.NewGuestTrialPipeline(speech, verifier, service)
	if err != nil {
		return guestTrialRuntime{}, err
	}
	return guestTrialRuntime{control: service, voice: pipeline, state: issuer}, nil
}
