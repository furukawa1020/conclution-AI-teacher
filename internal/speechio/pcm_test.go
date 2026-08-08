package speechio

import "testing"

func TestPCM16HasMeaningfulSample(t *testing.T) {
	for _, test := range []struct {
		name string
		pcm  []byte
		want bool
	}{
		{name: "empty", pcm: nil},
		{name: "digital silence", pcm: []byte{0, 0, 0, 0}},
		{name: "below floor positive", pcm: []byte{32, 0}},
		{name: "below floor negative", pcm: []byte{224, 255}},
		{name: "meaningful positive", pcm: []byte{33, 0}, want: true},
		{name: "meaningful negative", pcm: []byte{223, 255}, want: true},
		{name: "ignores incomplete tail", pcm: []byte{0, 0, 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := PCM16HasMeaningfulSample(test.pcm); got != test.want {
				t.Fatalf("PCM16HasMeaningfulSample(%v)=%v, want %v", test.pcm, got, test.want)
			}
		})
	}
}
