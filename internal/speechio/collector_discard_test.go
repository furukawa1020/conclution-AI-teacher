package speechio

import "testing"

func TestPCMCollectorDiscardClearsOnlyOwnedCopies(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		c := newStreamingPCMCollector(2)
		input := []byte{80, 0}
		c.add(input)
		owned := c.chunks[0]
		slots := c.chunks
		cache := newStreamingPCMCache(1, 1024)
		key := newStreamingPCMCacheKey("voice", "reply")
		completed, ok := c.complete()
		if !ok {
			t.Fatal("valid collector did not complete")
		}
		cache.put(key, completed)
		if overflow {
			c.add([]byte{81, 0})
		} else {
			c.discard()
		}
		c.discard()
		c.add([]byte{82, 0})
		if _, ok := c.complete(); ok {
			t.Fatal("discarded collector reopened")
		}
		if owned[0] != 0 || slots[0] != nil || c.chunks != nil || c.size != 0 {
			t.Fatal("owned PCM or references retained")
		}
		cached, ok := cache.get(key)
		if !ok || cached.chunks[0][0] != 80 || input[0] != 80 {
			t.Fatal("discard changed independent cache or input")
		}
	}
}
