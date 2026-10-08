package nativevoice

import (
	"context"
	"testing"
)

func assertNotification(t *testing.T, notification <-chan struct{}, closed bool) {
	t.Helper()
	select {
	case <-notification:
		if !closed {
			t.Fatal("unreadable output woke the receiver")
		}
	default:
		if closed {
			t.Fatal("readable transition did not wake the receiver")
		}
	}
}

func TestNativeNotificationsTrackReadableTransitions(t *testing.T) {
	_, session := receiveCancellationFixture(t)
	before := session.notify
	pcm := []byte{1, 0}
	if err := session.enqueueEvents([]Event{{Kind: EventAudioPCM, PCM: pcm}}, false); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, false)
	if len(session.ready) != 0 || len(session.held) != 1 {
		t.Fatal("uncommitted audio escaped its gate")
	}
	if err := session.enqueueEvents([]Event{{Kind: EventInputCaption, CaptionUTF8: []byte("はい"), CaptionFinal: true}}, false); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, true)
	before = session.notify
	if err := session.enqueueEvents([]Event{{Kind: EventInputCaption, CaptionUTF8: []byte("次")}}, false); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, false)
	for _, want := range []string{"はい", "次"} {
		event, err := session.Receive(context.Background())
		if err != nil || string(event.CaptionUTF8) != want {
			t.Fatalf("event=%+v err=%v", event, err)
		}
		event.Clear()
	}
	if err := session.CommitOutput(); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, true)
	event, err := session.Receive(context.Background())
	if err != nil || event.Kind != EventAudioPCM || len(event.PCM) != 2 {
		t.Fatalf("committed event=%+v err=%v", event, err)
	}
	event.Clear()
	before = session.notify
	if err := session.enqueueEvents([]Event{{Kind: EventAudioPCM, PCM: []byte{2, 0}}}, false); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, true)
	event, err = session.Receive(context.Background())
	if err != nil || len(event.PCM) != 2 || event.PCM[0] != 2 {
		t.Fatalf("next PCM=%+v err=%v", event, err)
	}
	event.Clear()
	before = session.notify
	_ = session.Close()
	assertNotification(t, before, true)
}

func TestNativeDiscardedOutputDoesNotWakeButInterruptionDoes(t *testing.T) {
	_, session := receiveCancellationFixture(t)
	session.DiscardOutput()
	before := session.notify
	pcm := []byte{1, 0}
	if err := session.enqueueEvents([]Event{{Kind: EventAudioPCM, PCM: pcm}}, false); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, false)
	if pcm[0] != 0 || len(session.ready) != 0 || len(session.held) != 0 {
		t.Fatal("discarded output was retained")
	}
	if err := session.enqueueEvents([]Event{{Kind: EventInterrupted}}, true); err != nil {
		t.Fatal(err)
	}
	assertNotification(t, before, true)
	event, err := session.Receive(context.Background())
	if err != nil || event.Kind != EventInterrupted {
		t.Fatalf("interruption=%+v err=%v", event, err)
	}
}
