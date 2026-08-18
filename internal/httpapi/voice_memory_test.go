package httpapi

import (
	"errors"
	"reflect"
	"testing"

	"github.com/furukawa1020/conclution-ai-teacher/internal/identity"
	"github.com/furukawa1020/conclution-ai-teacher/internal/longmemory"
)

type voiceMemoryOpener struct {
	calls      int
	wantUID    string
	wantAppID  string
	wantToken  string
	payload    longmemory.Payload
	generation int64
	err        error
}

func (opener *voiceMemoryOpener) OpenSessionContext(uid, appID, token string) (longmemory.Payload, int64, error) {
	opener.calls++
	if uid != opener.wantUID || appID != opener.wantAppID || token != opener.wantToken {
		return longmemory.Payload{}, 0, errors.New("unexpected session context binding")
	}
	payload := longmemory.Payload{
		Topics:      append([]string(nil), opener.payload.Topics...),
		Preferences: append([]string(nil), opener.payload.Preferences...),
		OpenLoops:   append([]string(nil), opener.payload.OpenLoops...),
	}
	return payload, opener.generation, opener.err
}

func TestVoiceMemoryUsesOneTypedBindingForEveryTransport(t *testing.T) {
	want := longmemory.Payload{
		Topics:      []string{"small voice"},
		Preferences: []string{"brief answer"},
		OpenLoops:   []string{"continue the same topic"},
	}
	opener := &voiceMemoryOpener{
		wantUID:    "user-123",
		wantAppID:  "app-123",
		wantToken:  "kms1.opaque",
		payload:    want,
		generation: 7,
	}
	server := &Server{voice: VoiceOptions{SessionContextOpener: opener}}
	principal := identity.Principal{UID: "user-123", AppID: "app-123", AccountVerified: true}

	for _, transport := range []string{"buffered-http", "ndjson-stream", "websocket-live"} {
		input := VoiceTurnInput{}
		server.attachVoiceMemory(principal, &input, "kms1.opaque")
		if input.MemoryStatus != VoiceMemoryAccepted || input.MemoryGeneration != 7 ||
			input.Memory == nil || !reflect.DeepEqual(*input.Memory, want) {
			t.Fatalf("%s did not receive the common typed binding: %+v", transport, input)
		}
		clearVoiceInput(&input)
		if input.Memory != nil || input.MemoryGeneration != 0 {
			t.Fatalf("%s retained decrypted memory", transport)
		}
	}
	if opener.calls != 3 {
		t.Fatalf("opener calls=%d, want 3", opener.calls)
	}
}

func TestVoiceMemoryFailsClosedWithoutChangingVoiceAvailability(t *testing.T) {
	principal := identity.Principal{UID: "user-123", AppID: "app-123", AccountVerified: true}
	guest := identity.Principal{
		UID: "guest-123", AppID: "app-123", Provider: "anonymous", AuthMethod: "guest-v1",
	}
	tests := []struct {
		name      string
		principal identity.Principal
		input     VoiceTurnInput
		token     string
		opener    *voiceMemoryOpener
		wantCalls int
		want      VoiceMemoryStatus
	}{
		{name: "absent", principal: principal, want: VoiceMemoryAbsent},
		{name: "guest", principal: guest, token: "kms1.opaque", opener: &voiceMemoryOpener{}, want: VoiceMemoryRejected},
		{name: "strict", principal: principal, input: VoiceTurnInput{StrictCloudMinimization: true}, token: "kms1.opaque", opener: &voiceMemoryOpener{}, want: VoiceMemoryRejected},
		{name: "tampered", principal: principal, token: "kms1.opaque", opener: &voiceMemoryOpener{wantUID: "user-123", wantAppID: "app-123", wantToken: "kms1.opaque", err: longmemory.ErrInvalid}, wantCalls: 1, want: VoiceMemoryRejected},
		{name: "invalid-generation", principal: principal, token: "kms1.opaque", opener: &voiceMemoryOpener{wantUID: "user-123", wantAppID: "app-123", wantToken: "kms1.opaque"}, wantCalls: 1, want: VoiceMemoryRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{voice: VoiceOptions{SessionContextOpener: test.opener}}
			server.attachVoiceMemory(test.principal, &test.input, test.token)
			if test.input.MemoryStatus != test.want || test.input.Memory != nil || test.input.MemoryGeneration != 0 {
				t.Fatalf("unexpected memory result: %+v", test.input)
			}
			if test.opener != nil && test.opener.calls != test.wantCalls {
				t.Fatalf("opener calls=%d want=%d", test.opener.calls, test.wantCalls)
			}
		})
	}
}

func TestVoiceMemoryAbsentDoesNoAdditionalWork(t *testing.T) {
	opener := &voiceMemoryOpener{}
	server := &Server{voice: VoiceOptions{SessionContextOpener: opener}}
	principal := identity.Principal{UID: "user-123", AppID: "app-123", AccountVerified: true}
	for range 1000 {
		input := VoiceTurnInput{}
		server.attachVoiceMemory(principal, &input, "")
		if input.MemoryStatus != VoiceMemoryAbsent {
			t.Fatal("absent context changed the voice path")
		}
	}
	if opener.calls != 0 {
		t.Fatalf("absent context invoked opener %d times", opener.calls)
	}
}
