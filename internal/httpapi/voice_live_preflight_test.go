package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validPreflightFixture() voiceLivePreflightFrame {
	return voiceLivePreflightFrame{
		Type:          "preflight",
		Version:       voiceLivePreflightVersion,
		IDToken:       "header.payload.signature",
		AppCheckToken: "header.payload.signature",
		Generation:    7,
	}
}

func validActivateFixture(leaseID string) voiceLiveActivateFrame {
	proofVersion := voiceLiveLatencyProofVersion
	return voiceLiveActivateFrame{
		Type:                "activate",
		Version:             voiceLivePreflightVersion,
		LeaseID:             leaseID,
		Generation:          7,
		NativeCoachControl:  true,
		SessionState:        "",
		TurnMode:            VoiceTurnIntentional,
		SampleRateHz:        voiceLiveSampleRateHz,
		NativeAudio:         true,
		LatencyProofVersion: &proofVersion,
	}
}

func TestVoiceLivePreflightFrameCannotContainConversationContent(t *testing.T) {
	payloads := []string{
		`{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"audio":"AA"}`,
		`{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"caption":"秘密"}`,
		`{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"question":"答えは？"}`,
		`{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"sessionState":"state"}`,
		`{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"turnMode":"intentional"}`,
	}
	for _, payload := range payloads {
		var frame voiceLivePreflightFrame
		if err := decodeStrictVoiceLiveJSON([]byte(payload), &frame); err == nil {
			t.Fatalf("content-bearing preflight decoded: %s", payload)
		}
	}
}

func TestVoiceLivePreflightFrameIsStrictAndFinite(t *testing.T) {
	frame := validPreflightFixture()
	if !validVoiceLivePreflight(frame) {
		t.Fatal("valid preflight rejected")
	}
	for _, mutate := range []func(*voiceLivePreflightFrame){
		func(value *voiceLivePreflightFrame) { value.Type = "start" },
		func(value *voiceLivePreflightFrame) { value.Version = 2 },
		func(value *voiceLivePreflightFrame) { value.IDToken = "secret" },
		func(value *voiceLivePreflightFrame) { value.AppCheckToken = "header..signature" },
		func(value *voiceLivePreflightFrame) { value.Generation = 0 },
		func(value *voiceLivePreflightFrame) { value.Generation = voiceLiveMaxGeneration + 1 },
	} {
		candidate := frame
		mutate(&candidate)
		if validVoiceLivePreflight(candidate) {
			t.Fatalf("invalid preflight accepted: %+v", candidate)
		}
	}

	duplicate := `{"type":"preflight","version":1,"idToken":"header.payload.signature","appCheckToken":"header.payload.signature","generation":7,"generation":8}`
	var decoded voiceLivePreflightFrame
	if err := decodeStrictVoiceLiveJSON([]byte(duplicate), &decoded); err == nil {
		t.Fatal("duplicate preflight key accepted")
	}
}

func TestVoiceLivePreflightLeaseIDIsOpaqueCanonicalAndUnbiased(t *testing.T) {
	entropy := bytes.Repeat([]byte{0xab}, voiceLiveLeaseIDBytes)
	leaseID, err := newVoiceLivePreflightLeaseIDFrom(bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("new lease ID: %v", err)
	}
	if !validVoiceLiveLeaseID(leaseID) ||
		!strings.HasPrefix(leaseID, voiceLiveLeaseIDPrefix) ||
		len(leaseID) != len(voiceLiveLeaseIDPrefix)+43 {
		t.Fatalf("lease ID = %q", leaseID)
	}
	if strings.Contains(leaseID, "=") {
		t.Fatal("lease ID contains base64 padding")
	}
	if _, err := newVoiceLivePreflightLeaseIDFrom(
		bytes.NewReader(entropy[:voiceLiveLeaseIDBytes-1]),
	); err == nil {
		t.Fatal("short entropy accepted")
	}
	if _, err := newVoiceLivePreflightLeaseIDFrom(nil); err == nil {
		t.Fatal("nil entropy source accepted")
	}
}

func TestVoiceLivePreflightReadyHasExactContentFreeShape(t *testing.T) {
	leaseID, err := newVoiceLivePreflightLeaseIDFrom(
		bytes.NewReader(bytes.Repeat([]byte{1}, voiceLiveLeaseIDBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	frame := voiceLivePreflightReadyFrame{
		Type:        "preflight-ready",
		Version:     1,
		LeaseID:     leaseID,
		Generation:  7,
		ExpiresInMS: voiceLivePreflightTTL.Milliseconds(),
	}
	if !validVoiceLivePreflightReady(frame) {
		t.Fatal("valid preflight ready rejected")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(payload, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 5 {
		t.Fatalf("preflight ready keys = %v", keys)
	}
	for _, forbidden := range []string{
		"audio", "caption", "question", "sessionState", "uid", "idToken",
	} {
		if _, exists := keys[forbidden]; exists {
			t.Fatalf("preflight ready exposed %q", forbidden)
		}
	}
	frame.ExpiresInMS++
	if validVoiceLivePreflightReady(frame) {
		t.Fatal("over-TTL ready accepted")
	}
}

func TestVoiceLiveActivateConsumesOnlyMatchingLeaseAndGeneration(t *testing.T) {
	leaseID, err := newVoiceLivePreflightLeaseIDFrom(
		bytes.NewReader(bytes.Repeat([]byte{2}, voiceLiveLeaseIDBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	frame := validActivateFixture(leaseID)
	if !validVoiceLiveActivate(frame, leaseID, 7) {
		t.Fatal("valid activation rejected")
	}
	if validVoiceLiveActivate(frame, leaseID, 8) {
		t.Fatal("cross-generation activation accepted")
	}
	otherLease, _ := newVoiceLivePreflightLeaseIDFrom(
		bytes.NewReader(bytes.Repeat([]byte{3}, voiceLiveLeaseIDBytes)),
	)
	if validVoiceLiveActivate(frame, otherLease, 7) {
		t.Fatal("cross-lease activation accepted")
	}
	frame.NativeCoachControl = false
	if validVoiceLiveActivate(frame, leaseID, 7) {
		t.Fatal("native activation without coach capability accepted")
	}
}

func TestVoiceLiveActivateRejectsCredentialsAndUnknownFields(t *testing.T) {
	leaseID, err := newVoiceLivePreflightLeaseIDFrom(
		bytes.NewReader(bytes.Repeat([]byte{4}, voiceLiveLeaseIDBytes)),
	)
	if err != nil {
		t.Fatal(err)
	}
	frame := validActivateFixture(leaseID)
	payload, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"idToken", "appCheckToken", "audio", "caption"} {
		candidate := append([]byte(nil), payload[:len(payload)-1]...)
		candidate = append(candidate, []byte(`,"`+field+`":"secret"}`)...)
		var decoded voiceLiveActivateFrame
		if decodeErr := decodeStrictVoiceLiveJSON(candidate, &decoded); decodeErr == nil {
			t.Fatalf("activate accepted forbidden field %q", field)
		}
	}
}

func TestVoiceLivePreflightEntropyErrorsAreSanitized(t *testing.T) {
	source := errorReader{}
	if _, err := newVoiceLivePreflightLeaseIDFrom(source); err == nil ||
		err.Error() != "create voice live preflight lease" {
		t.Fatalf("entropy error = %v", err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("secret provider detail")
}
