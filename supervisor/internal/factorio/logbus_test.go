package factorio

import "testing"

func TestLogBusRingBufferBounded(t *testing.T) {
	b := NewLogBus(3)
	for i := range 5 {
		b.Publish(StreamStdout, string(rune('a'+i)))
	}
	hist := b.History()
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	if hist[0].Text != "c" || hist[2].Text != "e" {
		t.Fatalf("ring buffer kept wrong lines: %q..%q", hist[0].Text, hist[2].Text)
	}
	// Seq keeps counting across evictions.
	if hist[2].Seq != 5 {
		t.Fatalf("last seq = %d, want 5", hist[2].Seq)
	}
}

func TestLogBusSubscribeReceivesHistoryAndLive(t *testing.T) {
	b := NewLogBus(10)
	b.Publish(StreamStdout, "past")

	hist, lines, cancel := b.Subscribe(4)
	defer cancel()
	if len(hist) != 1 || hist[0].Text != "past" {
		t.Fatalf("expected 1 history line 'past', got %+v", hist)
	}

	b.Publish(StreamStderr, "live")
	select {
	case l := <-lines:
		if l.Text != "live" || l.Stream != StreamStderr {
			t.Fatalf("unexpected live line %+v", l)
		}
	default:
		t.Fatal("expected a live line")
	}
}

func TestLogBusSlowSubscriberDropsRatherThanBlocks(t *testing.T) {
	b := NewLogBus(10)
	_, _, cancel := b.Subscribe(1) // tiny buffer, never drained
	defer cancel()
	// Publishing more than the buffer must not block.
	done := make(chan struct{})
	go func() {
		for range 100 {
			b.Publish(StreamStdout, "x")
		}
		close(done)
	}()
	<-done
}
