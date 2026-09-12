package rtc

import (
	"sync"
	"time"
)

type inputSpan struct {
	start, end time.Duration
	arrivedAt  time.Time
}

// ASR offsets count only the PCM samples forwarded to the recognizer, so RTP
// timestamps (which can jump across loss) cannot serve as their clock.
type inputClock struct {
	mu          sync.Mutex
	position    time.Duration
	spans       [6000]inputSpan
	next, count int
}

func (c *inputClock) record(samples int, arrivedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	duration := time.Duration(samples) * time.Second / pcmuSampleRate
	if duration <= 0 {
		return
	}
	c.spans[c.next] = inputSpan{start: c.position, end: c.position + duration, arrivedAt: arrivedAt}
	c.position += duration
	c.next = (c.next + 1) % len(c.spans)
	if c.count < len(c.spans) {
		c.count++
	}
}

func (c *inputClock) at(offsetMS int64) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if offsetMS <= 0 || offsetMS > c.position.Milliseconds() {
		return time.Time{}
	}
	offset := time.Duration(offsetMS) * time.Millisecond
	for i := 0; i < c.count; i++ {
		span := c.spans[(c.next-1-i+len(c.spans))%len(c.spans)]
		if offset > span.start && offset <= span.end {
			return span.arrivedAt.Add(offset - span.end)
		}
	}
	return time.Time{}
}
