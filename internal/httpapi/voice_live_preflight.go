package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

const (
	voiceLivePreflightVersion = 1
	voiceLivePreflightTTL     = 15 * time.Second
	voiceLiveMaxGeneration    = int64(1<<53 - 1)
	voiceLiveLeaseIDPrefix    = "knl1_"
	voiceLiveLeaseIDBytes     = 32
)

// voiceLivePreflightFrame is deliberately unable to represent voice or
// conversation content. Authentication material is accepted once, in the
// first WebSocket message, and is cleared immediately after verification.
type voiceLivePreflightFrame struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	IDToken       string `json:"idToken"`
	AppCheckToken string `json:"appCheckToken"`
	Generation    int64  `json:"generation"`
}

type voiceLivePreflightReadyFrame struct {
	Type        string `json:"type"`
	Version     int    `json:"version"`
	LeaseID     string `json:"leaseId"`
	Generation  int64  `json:"generation"`
	ExpiresInMS int64  `json:"expiresInMs"`
}

// voiceLiveActivateFrame carries the existing bounded turn only after the
// caller has proved possession of the short-lived preflight capability. It
// has no credential fields, so tokens cannot be replayed during activation.
type voiceLiveActivateFrame struct {
	Type                    string        `json:"type"`
	Version                 int           `json:"version"`
	LeaseID                 string        `json:"leaseId"`
	Generation              int64         `json:"generation"`
	NativeCoachControl      bool          `json:"nativeCoachControl"`
	SessionState            string        `json:"sessionState"`
	SessionContext          string        `json:"sessionContext,omitempty"`
	TurnMode                VoiceTurnMode `json:"turnMode"`
	SampleRateHz            int           `json:"sampleRateHz"`
	StrictCloudMinimization bool          `json:"strictCloudMinimization"`
	NativeAudio             bool          `json:"nativeAudio"`
	LatencyProofVersion     *int          `json:"latencyProofVersion,omitempty"`
}

func validVoiceLivePreflight(frame voiceLivePreflightFrame) bool {
	return frame.Type == "preflight" &&
		frame.Version == voiceLivePreflightVersion &&
		validVoiceLiveJWT(frame.IDToken) &&
		validVoiceLiveJWT(frame.AppCheckToken) &&
		validVoiceLiveGeneration(frame.Generation)
}

func validVoiceLivePreflightReady(frame voiceLivePreflightReadyFrame) bool {
	return frame.Type == "preflight-ready" &&
		frame.Version == voiceLivePreflightVersion &&
		validVoiceLiveLeaseID(frame.LeaseID) &&
		validVoiceLiveGeneration(frame.Generation) &&
		frame.ExpiresInMS > 0 &&
		frame.ExpiresInMS <= voiceLivePreflightTTL.Milliseconds()
}

func validVoiceLiveActivate(
	frame voiceLiveActivateFrame,
	expectedLeaseID string,
	expectedGeneration int64,
) bool {
	if frame.Type != "activate" ||
		frame.Version != voiceLivePreflightVersion ||
		!validVoiceLiveLeaseID(frame.LeaseID) ||
		frame.LeaseID != expectedLeaseID ||
		!validVoiceLiveGeneration(frame.Generation) ||
		frame.Generation != expectedGeneration ||
		frame.SampleRateHz != voiceLiveSampleRateHz ||
		len(frame.SessionState) > maxStateBytes ||
		len(frame.SessionContext) > maxSessionContextBytes ||
		!utf8.ValidString(frame.SessionState) ||
		strings.TrimSpace(frame.SessionState) != frame.SessionState ||
		(frame.StrictCloudMinimization && frame.SessionState != "") ||
		(frame.StrictCloudMinimization && frame.NativeAudio) ||
		(frame.NativeCoachControl != frame.NativeAudio) ||
		(frame.LatencyProofVersion != nil &&
			*frame.LatencyProofVersion != voiceLiveLatencyProofVersion) {
		return false
	}
	switch frame.TurnMode {
	case VoiceTurnIntentional, VoiceTurnForeground, VoiceTurnAmbient:
		return true
	default:
		return false
	}
}

func validVoiceLiveGeneration(value int64) bool {
	return value > 0 && value <= voiceLiveMaxGeneration
}

func validVoiceLiveLeaseID(value string) bool {
	if !strings.HasPrefix(value, voiceLiveLeaseIDPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, voiceLiveLeaseIDPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != voiceLiveLeaseIDBytes {
		clear(decoded)
		return false
	}
	canonical := base64.RawURLEncoding.EncodeToString(decoded)
	clear(decoded)
	return encoded == canonical
}

func newVoiceLivePreflightLeaseID() (string, error) {
	return newVoiceLivePreflightLeaseIDFrom(rand.Reader)
}

func newVoiceLivePreflightLeaseIDFrom(source io.Reader) (string, error) {
	if source == nil {
		return "", errors.New("voice live preflight entropy is required")
	}
	random := make([]byte, voiceLiveLeaseIDBytes)
	if _, err := io.ReadFull(source, random); err != nil {
		clear(random)
		return "", errors.New("create voice live preflight lease")
	}
	encoded := base64.RawURLEncoding.EncodeToString(random)
	clear(random)
	leaseID := voiceLiveLeaseIDPrefix + encoded
	if !validVoiceLiveLeaseID(leaseID) {
		return "", errors.New("create voice live preflight lease")
	}
	return leaseID, nil
}

func writeVoiceLivePreflightReadyJSON(
	ctx context.Context,
	conn *websocket.Conn,
	frame voiceLivePreflightReadyFrame,
) error {
	if !validVoiceLivePreflightReady(frame) {
		return errors.New("invalid voice live preflight ready frame")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, payload)
}
