package factorio

import (
	"sync"
	"time"
)

// Stream identifies which pipe a log line came from.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// LogLine is a single captured line of output from the Factorio process.
type LogLine struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Stream Stream    `json:"stream"`
	Text   string    `json:"text"`
}

// LogBus fans out Factorio's stdout/stderr to any number of subscribers and
// keeps a bounded ring buffer of recent history so a newly-connected web client
// can be shown a backlog. It is safe for concurrent use.
type LogBus struct {
	mu       sync.RWMutex
	ring     []LogLine
	capacity int
	seq      uint64
	subs     map[int]chan LogLine
	nextSub  int
}

// NewLogBus creates a bus retaining up to capacity recent lines.
func NewLogBus(capacity int) *LogBus {
	if capacity <= 0 {
		capacity = 1000
	}
	return &LogBus{
		ring:     make([]LogLine, 0, capacity),
		capacity: capacity,
		subs:     make(map[int]chan LogLine),
	}
}

// Publish records a line and delivers it to all current subscribers. Delivery
// is non-blocking: a subscriber whose buffer is full drops the line rather than
// stalling the Factorio output pump.
func (b *LogBus) Publish(stream Stream, text string) LogLine {
	b.mu.Lock()
	b.seq++
	line := LogLine{Seq: b.seq, Time: time.Now().UTC(), Stream: stream, Text: text}
	if len(b.ring) >= b.capacity {
		copy(b.ring, b.ring[1:])
		b.ring[len(b.ring)-1] = line
	} else {
		b.ring = append(b.ring, line)
	}
	subs := make([]chan LogLine, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- line:
		default:
		}
	}
	return line
}

// Subscribe returns a channel of future lines plus a snapshot of recent history,
// and an unsubscribe func. The caller must call unsubscribe to avoid leaking.
func (b *LogBus) Subscribe(buffer int) (history []LogLine, lines <-chan LogLine, cancel func()) {
	if buffer <= 0 {
		buffer = 256
	}
	ch := make(chan LogLine, buffer)
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = ch
	history = make([]LogLine, len(b.ring))
	copy(history, b.ring)
	b.mu.Unlock()

	cancel = func() {
		b.mu.Lock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
		b.mu.Unlock()
	}
	return history, ch, cancel
}

// History returns a snapshot of the retained lines.
func (b *LogBus) History() []LogLine {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]LogLine, len(b.ring))
	copy(out, b.ring)
	return out
}
