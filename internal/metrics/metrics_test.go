package metrics

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

func sampleAt(sec int64, cpu float64) Sample {
	return Sample{At: sec, CPUPercent: cpu, MemUsed: 100, MemTotal: 200, StorageUsed: 10}
}

// The buffer must not grow. That is the whole reason it is allowed to live in
// RAM on a 256 MB device (D-13), so it is asserted rather than assumed.
func TestRingNeverGrowsAndKeepsTheNewest(t *testing.T) {
	r := newRing(10*time.Second, time.Second) // capacity 10
	capacity := len(r.buf)
	if capacity != 10 {
		t.Fatalf("capacity = %d, want 10", capacity)
	}

	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 25; i++ {
		r.offer(sampleAt(base.Add(time.Duration(i)*time.Second).Unix(), float64(i)), base.Add(time.Duration(i)*time.Second))
	}
	if len(r.buf) != capacity {
		t.Errorf("buffer grew from %d to %d", capacity, len(r.buf))
	}

	got := r.samples()
	if len(got) != 10 {
		t.Fatalf("samples = %d, want 10", len(got))
	}
	// Oldest first, newest last, and the oldest five are gone.
	if got[0].CPUPercent != 15 || got[9].CPUPercent != 24 {
		t.Errorf("window = [%v … %v], want [15 … 24]", got[0].CPUPercent, got[9].CPUPercent)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].At > got[i].At {
			t.Fatalf("samples out of order at %d: %d > %d", i, got[i-1].At, got[i].At)
		}
	}
}

// A partially filled ring must not report the zero values it was allocated
// with — an empty slot is not a reading of zero.
func TestPartialRingReportsOnlyRealSamples(t *testing.T) {
	r := newRing(10*time.Second, time.Second)
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 3; i++ {
		r.offer(sampleAt(base.Add(time.Duration(i)*time.Second).Unix(), float64(i+1)), base.Add(time.Duration(i)*time.Second))
	}
	got := r.samples()
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3 (not the whole allocated buffer)", len(got))
	}
	for i, s := range got {
		if s.CPUPercent == 0 {
			t.Errorf("sample %d is an unwritten slot", i)
		}
	}
}

// The coarse ring exists to make 24 hours affordable. It must ignore the
// samples that arrive between its own ticks, or it becomes the fine ring with
// a bigger buffer.
func TestCoarseRingKeepsItsOwnCadence(t *testing.T) {
	h := NewHistory()
	base := time.Unix(1_700_000_000, 0)
	// 40 samples, 3 seconds apart: two minutes of wall clock.
	for i := 0; i < 40; i++ {
		at := base.Add(time.Duration(i) * 3 * time.Second)
		h.Add(sampleAt(at.Unix(), float64(i)))
	}
	live, day := h.Live(), h.Day()
	if len(live) != 40 {
		t.Errorf("live samples = %d, want all 40", len(live))
	}
	// 40 samples 3s apart span 117 seconds, so the coarse ring takes the one
	// at 0s and the one at 60s — and not the 38 in between. (117 < 120, so
	// there is no third: the arithmetic matters more than the round number.)
	if len(day) != 2 {
		t.Errorf("day samples = %d, want 2 (one per minute over 117s)", len(day))
	}
	if day[0].At != base.Unix() || day[1].At != base.Add(60*time.Second).Unix() {
		t.Errorf("coarse samples at %d/%d, want %d/%d",
			day[0].At, day[1].At, base.Unix(), base.Add(60*time.Second).Unix())
	}
}

// The promise "metrics live in RAM only" is only acceptable while the amount
// is known. This pins the number, and pins it to the struct: a new field
// silently doubling the cost has to break this test.
func TestBufferCostIsBoundedAndMatchesTheStruct(t *testing.T) {
	if got := int(reflect.TypeOf(Sample{}).Size()); got != sampleBytes {
		t.Fatalf("Sample is %d bytes but sampleBytes says %d — update both", got, sampleBytes)
	}
	h := NewHistory()
	size := h.Size()
	// 60 live + 1440 day samples at 40 bytes = 60 000 bytes.
	if size != (60+1440)*sampleBytes {
		t.Errorf("buffer size = %d bytes, want %d", size, (60+1440)*sampleBytes)
	}
	if size > 128*1024 {
		t.Errorf("metrics buffer is %d bytes: too much for a 256 MB router (D-13)", size)
	}
	// Filling it must not change the cost.
	base := time.Unix(1_700_000_000, 0)
	for i := 0; i < 5000; i++ {
		h.Add(sampleAt(base.Add(time.Duration(i)*time.Second).Unix(), 1))
	}
	if after := h.Size(); after != size {
		t.Errorf("buffer grew while filling: %d → %d", size, after)
	}
}

// --- sampler ---

type fakeSource struct {
	info core.SystemInfo
	err  error
	n    int
}

func (f *fakeSource) Info() (core.SystemInfo, error) {
	f.n++
	if f.err != nil {
		return core.SystemInfo{}, f.err
	}
	return f.info, nil
}

// A dashboard opened a second after start up must not be empty for a full
// tick, so the first sample is taken immediately.
func TestSamplerRecordsImmediately(t *testing.T) {
	src := &fakeSource{info: core.SystemInfo{CPUPercent: 7, MemUsed: 5, MemTotal: 10, StorageUsed: 3}}
	h := NewHistory()
	s := NewSampler(src, h)
	s.every = time.Hour // only the immediate reading can land

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for len(h.Live()) == 0 {
		select {
		case <-deadline:
			t.Fatal("no sample recorded within 2s of starting")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-done

	got := h.Live()[0]
	if got.CPUPercent != 7 || got.MemUsed != 5 || got.StorageUsed != 3 {
		t.Errorf("sample = %+v, want the source's values", got)
	}
}

// A device that stopped answering leaves a gap. It must not leave a zero: a
// zero draws a CPU that went idle at the exact moment the router got sick.
func TestSamplerSkipsFailuresInsteadOfRecordingZero(t *testing.T) {
	src := &fakeSource{err: errors.New("ubus: Command failed: Not found")}
	h := NewHistory()
	s := NewSampler(src, h)
	var seen error
	s.onError = func(err error) { seen = err }

	s.once()

	if len(h.Live()) != 0 {
		t.Errorf("a failed read was recorded: %+v", h.Live())
	}
	if seen == nil {
		t.Error("the failure was swallowed without telling anyone")
	}
}

// Sampling runs on its own clock, not on a client's: history has to exist
// before anybody opens the panel.
func TestSamplerKeepsSamplingWithoutReaders(t *testing.T) {
	src := &fakeSource{info: core.SystemInfo{CPUPercent: 1, MemTotal: 10}}
	h := NewHistory()
	s := NewSampler(src, h)
	s.every = 10 * time.Millisecond
	// Distinct timestamps, or the ring's own cadence filter drops them.
	var tick int64
	s.now = func() time.Time { tick++; return time.Unix(1_700_000_000+tick, 0) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	if n := len(h.Live()); n < 3 {
		t.Errorf("samples after 120ms at 10ms = %d, want several", n)
	}
	if src.n < 3 {
		t.Errorf("source read %d times, want several", src.n)
	}
}

// A source that can answer the cheap read must never be asked for the full
// snapshot: Info() resolves the public address by asking a third party, and
// this runs on a timer.
type vitalsSource struct {
	fakeSource
	vitalsCalls int
}

func (v *vitalsSource) Vitals() (core.Vitals, error) {
	v.vitalsCalls++
	return core.Vitals{CPUPercent: 4, MemUsed: 1, MemTotal: 2, StorageUsed: 3}, nil
}

func TestSamplerPrefersTheCheapReadOverTheFullSnapshot(t *testing.T) {
	src := &vitalsSource{}
	h := NewHistory()
	s := NewSampler(src, h)

	s.once()

	if src.vitalsCalls != 1 {
		t.Errorf("Vitals called %d times, want 1", src.vitalsCalls)
	}
	if src.n != 0 {
		t.Errorf("Info() called %d times: the polling path went through the expensive read", src.n)
	}
	got := h.Live()
	if len(got) != 1 || got[0].CPUPercent != 4 || got[0].StorageUsed != 3 {
		t.Errorf("sample = %+v, want the vitals values", got)
	}
}

// A source without the cheap read still works — that is what the fallback is
// for — but it is the exception, not the path the product takes.
func TestSamplerFallsBackToInfo(t *testing.T) {
	src := &fakeSource{info: core.SystemInfo{CPUPercent: 2, MemTotal: 5}}
	h := NewHistory()
	NewSampler(src, h).once()

	if src.n != 1 {
		t.Errorf("Info() called %d times, want 1", src.n)
	}
	if got := h.Live(); len(got) != 1 || got[0].CPUPercent != 2 {
		t.Errorf("sample = %+v, want the Info values", got)
	}
}
