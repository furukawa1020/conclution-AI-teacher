package conversation

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestVoiceTurnValidate(t *testing.T) {
	valid := VoiceTurn{SchemaVersion: SchemaVersion, Utterance: "どう進めればいい？"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid turn: %v", err)
	}
	longForm := VoiceTurn{
		SchemaVersion:  SchemaVersion,
		Utterance:      strings.Repeat("界", MaxUtteranceRunes),
		ExtendedSpeech: true,
	}
	if err := longForm.Validate(); err != nil {
		t.Fatalf("bounded long-form turn: %v", err)
	}
	foreground := VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "続きの質問",
		Ambient:       true,
		Foreground:    true,
	}
	if err := foreground.Validate(); err != nil {
		t.Fatalf("valid foreground turn: %v", err)
	}

	tooLargePDF := append([]byte("%PDF-"), bytes.Repeat([]byte("x"), MaxInlinePDFBytes)...)
	tests := []VoiceTurn{
		{SchemaVersion: 0, Utterance: "質問"},
		{SchemaVersion: SchemaVersion, Utterance: " \n "},
		{SchemaVersion: SchemaVersion, Utterance: strings.Repeat("界", MaxUtteranceRunes+1)},
		{SchemaVersion: SchemaVersion, Utterance: "質問", StateToken: " token "},
		{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
			Foreground:    true,
		},
		{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
			PDF:           &InlinePDF{MIMEType: "text/plain", Data: []byte("%PDF-x")},
		},
		{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
			PDF:           &InlinePDF{MIMEType: "application/pdf", Data: []byte("not-pdf")},
		},
		{
			SchemaVersion: SchemaVersion,
			Utterance:     "質問",
			PDF:           &InlinePDF{MIMEType: "application/pdf", Data: tooLargePDF},
		},
	}
	for index, turn := range tests {
		if err := turn.Validate(); !errors.Is(err, ErrInvalidTurn) {
			t.Errorf("case %d: got %v, want ErrInvalidTurn", index, err)
		}
	}
}

func TestVoiceTurnValidateAcceptsPDF(t *testing.T) {
	turn := VoiceTurn{
		SchemaVersion: SchemaVersion,
		Utterance:     "この論文の限界は？",
		PDF: &InlinePDF{
			MIMEType: " Application/PDF ",
			Data:     []byte("%PDF-1.7\nbody"),
		},
	}
	if err := turn.Validate(); err != nil {
		t.Fatalf("valid PDF turn: %v", err)
	}
}
