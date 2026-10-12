package httpapi

import "testing"

func TestStrictAudioBufferTerminalRejectsLateAudio(t *testing.T) {
	for _, mode := range []string{"clear", "release", "failed release", "nil release"} {
		t.Run(mode, func(t *testing.T) {
			b := &strictAudioBuffer{}
			b.markCommitted()
			if err := b.append([]byte{80, 0}); err != nil {
				t.Fatal(err)
			}
			owned := b.chunks[0]
			switch mode {
			case "clear":
				b.clear()
			case "nil release":
				_ = b.release(nil)
			default:
				err := b.release(func(pcm []byte) error {
					if pcm[0] != 80 {
						t.Error("audio changed before delivery")
					}
					b.markCommitted()
					lateErr := b.append([]byte{81, 0})
					if lateErr == nil {
						t.Error("release admitted reentrant audio")
					}
					if mode == "failed release" {
						return lateErr
					}
					return nil
				})
				if mode == "release" && err != nil {
					t.Fatal(err)
				}
				if mode == "failed release" && err == nil {
					t.Error("callback failure lost")
				}
			}
			b.markCommitted()
			if b.append([]byte{82, 0}) == nil || b.spoke() {
				t.Error("terminal buffer reopened")
			}
			if owned[0] != 0 || owned[1] != 0 {
				t.Error("owned audio not cleared")
			}
		})
	}
}
