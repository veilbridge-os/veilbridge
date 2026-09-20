// Package metrics keeps a short history of the device's own vital signs in
// RAM, and only in RAM (D-13).
//
// Why nothing is written to disk: the target is a router whose writable space
// is flash. The reference device has ~46 MB of overlay in total, and flash
// wears out by writing. A metrics file sampled every few seconds is a slow way
// to destroy the device it is supposed to monitor. Long history is therefore
// not a smaller file — it is a different product (a market app with a disk),
// which is exactly how D-13 draws the line.
//
// Why two resolutions instead of one: the dashboard wants a live sparkline of
// the last few minutes, and an operator wants to know whether the router was
// already swapping an hour ago. One ring fine enough for the first is far too
// expensive for the second — 24 hours at 3 s is 28 800 samples per series. So
// there are two: a fine ring for the live window and a coarse one for the day.
// Measured cost of both together is in Size(), and it is tens of kilobytes.
package metrics

import (
	"sync"
	"time"
)

// Sample is one reading. Times are unix seconds, not time.Time: a time.Time is
// 24 bytes with a monotonic clock and a location pointer, none of which
// survives being sent to a browser as JSON anyway.
type Sample struct {
	At         int64   `json:"at"`
	CPUPercent float64 `json:"cpuPercent"`
	MemUsed    int64   `json:"memUsed"`
	MemTotal   int64   `json:"memTotal"`
	// StorageUsed is included because it is the number that actually runs out
	// on a flash router, and it moves slowly enough to be worth a day of
	// history.
	StorageUsed int64 `json:"storageUsed"`
}

// sampleBytes is the in-memory size of one Sample: five 8-byte fields, no
// pointers, no padding. It is a constant so Size() cannot drift from the
// struct silently — the test asserts the two agree.
const sampleBytes = 40

// ring is a fixed-capacity circular buffer. It never grows and never
// allocates after construction: on a device with 256 MB of RAM, a metrics
// buffer that can grow is a metrics buffer that can take the panel down.
type ring struct {
	buf   []Sample
	next  int
	full  bool
	every time.Duration
	// last is when this ring last accepted a sample, so a coarse ring can
	// ignore the samples that arrive between its own ticks.
	last time.Time
}

func newRing(window, every time.Duration) *ring {
	n := int(window / every)
	if n < 1 {
		n = 1
	}
	return &ring{buf: make([]Sample, n), every: every}
}

// offer stores s if enough time has passed since this ring's last sample.
// It reports whether the sample was kept, which is what makes the coarse ring
// testable without waiting a minute.
func (r *ring) offer(s Sample, now time.Time) bool {
	if !r.last.IsZero() && now.Sub(r.last) < r.every {
		return false
	}
	r.last = now
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	return true
}

// samples returns the contents oldest-first. It copies: handing out the
// backing array would let a caller read entries that the writer is
// overwriting underneath it.
func (r *ring) samples() []Sample {
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	out := make([]Sample, 0, n)
	if r.full {
		out = append(out, r.buf[r.next:]...)
	}
	out = append(out, r.buf[:r.next]...)
	return out
}

// Resolutions of the two rings. LiveWindow matches the dashboard's graph
// window (D-12/M2.2: 3 minutes at a 3 second refresh); DayWindow is the
// "was it already bad an hour ago" history D-13 asks for.
const (
	LiveEvery  = 3 * time.Second
	LiveWindow = 3 * time.Minute
	DayEvery   = time.Minute
	DayWindow  = 24 * time.Hour
)

// History holds both rings behind one lock. It is safe for concurrent use:
// one sampler writes, every SSE stream and REST reader reads.
type History struct {
	mu   sync.RWMutex
	live *ring
	day  *ring
}

func NewHistory() *History {
	return &History{
		live: newRing(LiveWindow, LiveEvery),
		day:  newRing(DayWindow, DayEvery),
	}
}

// Add offers a sample to both rings. The live ring takes it if the sampler
// tick has elapsed, the coarse one only once a minute.
func (h *History) Add(s Sample) {
	now := time.Unix(s.At, 0)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.live.offer(s, now)
	h.day.offer(s, now)
}

// Live returns the last few minutes, oldest first.
func (h *History) Live() []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.live.samples()
}

// Day returns up to 24 hours at one-minute resolution, oldest first.
func (h *History) Day() []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.day.samples()
}

// Size reports the fixed memory cost of the buffers in bytes. It exists to be
// asserted in a test and printed at start up: "metrics live in RAM" is only an
// acceptable design while the amount of RAM is known and small.
func (h *History) Size() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return (len(h.live.buf) + len(h.day.buf)) * sampleBytes
}
