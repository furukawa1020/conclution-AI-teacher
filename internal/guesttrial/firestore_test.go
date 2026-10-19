package guesttrial

import (
	"cloud.google.com/go/firestore"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFirestoreRecordDecoderRejectsCoercionAndExtraData(t *testing.T) {
	r := initial()
	got, err := decodeRecord(recordData(r))
	if err != nil || got != r {
		t.Fatal(got, err)
	}
	for key, value := range map[string]any{"version": float64(1), "question": "unknown", "generation": int64(3), "assisted": "false", "status": "passed", "createdAt": "now", "expiresAt": r.ExpiresAt.Add(time.Second), "transcript": "must not be stored"} {
		data := recordData(r)
		data[key] = value
		if _, err := decodeRecord(data); err == nil {
			t.Fatal("accepted invalid field", key)
		}
	}
	for key := range recordData(r) {
		data := recordData(r)
		delete(data, key)
		if _, err := decodeRecord(data); err == nil {
			t.Fatal("missing", key)
		}
	}
}

// This integration test never connects to production; the emulator and a
// dedicated emulator-only project are mandatory. No credentials are required.
func TestFirestoreEmulatorConcurrentGeneration(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	host, _, err := net.SplitHostPort(os.Getenv("FIRESTORE_EMULATOR_HOST"))
	if err != nil || (host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		t.Fatal("emulator test requires a loopback endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := firestore.NewClient(ctx, "demo-kotae-guest-trial")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	s, _ := NewFirestoreStore(client)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(random)
	ref := client.Collection(Collection).Doc(key)
	t.Cleanup(func() {
		cleanup, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if _, err := ref.Delete(cleanup); err != nil {
			t.Error("remove emulator test record:", err)
		}
	})
	r := initial()
	if err := s.Create(ctx, key, r); err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Apply(ctx, key, 1, Help, r.CreatedAt)
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("writes", wins.Load())
	}
	got, err := s.Read(ctx, key, r.CreatedAt)
	if err != nil || !got.Assisted || got.Generation != 2 {
		t.Fatal(got, err)
	}
}
