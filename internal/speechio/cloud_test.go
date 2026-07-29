package speechio

import (
	"testing"

	"cloud.google.com/go/speech/apiv2/speechpb"
)

func TestRecognizedTextUsesOnlyTopAlternatives(t *testing.T) {
	t.Parallel()

	response := &speechpb.RecognizeResponse{
		Results: []*speechpb.SpeechRecognitionResult{
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "今日は研究の話をしたい", Confidence: 0.8},
					{Transcript: "採用してはいけない候補", Confidence: 0.99},
				},
			},
			{
				Alternatives: []*speechpb.SpeechRecognitionAlternative{
					{Transcript: "仮説がまだ曖昧です", Confidence: 0.6},
				},
			},
		},
	}

	text, confidence := recognizedText(response)
	if text != "今日は研究の話をしたい 仮説がまだ曖昧です" {
		t.Fatalf("text = %q", text)
	}
	if confidence < 0.69 || confidence > 0.71 {
		t.Fatalf("confidence = %f; want about 0.70", confidence)
	}
}

func TestRecognizedTextHandlesEmptyResponse(t *testing.T) {
	t.Parallel()

	text, confidence := recognizedText(nil)
	if text != "" || confidence != 0 {
		t.Fatalf("got (%q, %f); want empty", text, confidence)
	}
}
