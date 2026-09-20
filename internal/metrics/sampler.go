package metrics

import (
	"context"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Source is the narrow slice of the system manager a sampler needs. It is
// declared here, at the consumer, so this package does not depend on the whole
// adapter to read four numbers.
//
// Info() is the fallback, not the preference. A manager that also implements
// core.VitalsReader is asked that instead, because Info() resolves the public
// address by asking a third party — fine once, per request, from a human
// looking at a dashboard; a permanent outbound stream when something polls it
// every three seconds. That is not hypothetical: it is what this sampler did
// on the router until conntrack showed a connection to the same public-IP
// service every 3 seconds.
type Source interface {
	Info() (core.SystemInfo, error)
}

// Sampler fills a History on a timer, independently of whether anyone is
// watching. That independence is the point: a dashboard opened at 09:00 must
// be able to show what happened at 08:00, and a graph that only starts
// recording when a browser connects would show a flat line at every moment
// anyone actually looks at it.
type Sampler struct {
	src     Source
	hist    *History
	every   time.Duration
	now     func() time.Time
	onError func(error)
}

func NewSampler(src Source, hist *History) *Sampler {
	return &Sampler{src: src, hist: hist, every: LiveEvery, now: time.Now}
}

// Run samples until ctx is cancelled. It takes one reading immediately, so a
// panel opened a second after start up is not empty for three seconds.
//
// A failing Info() is skipped, not fatal and not recorded as zero: a gap in
// the history is honest, while a zero would draw a CPU that dropped to idle at
// exactly the moment the device stopped answering.
func (s *Sampler) Run(ctx context.Context) {
	s.once()
	t := time.NewTicker(s.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.once()
		}
	}
}

func (s *Sampler) once() {
	v, err := s.read()
	if err != nil {
		if s.onError != nil {
			s.onError(err)
		}
		return
	}
	s.hist.Add(Sample{
		At:          s.now().Unix(),
		CPUPercent:  v.CPUPercent,
		MemUsed:     v.MemUsed,
		MemTotal:    v.MemTotal,
		StorageUsed: v.StorageUsed,
	})
}

// read prefers the cheap local reading and falls back to the full snapshot
// only for sources that cannot offer one.
func (s *Sampler) read() (core.Vitals, error) {
	if vr, ok := s.src.(core.VitalsReader); ok {
		return vr.Vitals()
	}
	info, err := s.src.Info()
	if err != nil {
		return core.Vitals{}, err
	}
	return core.Vitals{
		CPUPercent:  info.CPUPercent,
		MemUsed:     info.MemUsed,
		MemTotal:    info.MemTotal,
		StorageUsed: info.StorageUsed,
	}, nil
}
