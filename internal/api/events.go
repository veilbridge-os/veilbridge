package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/metrics"
)

// The live layer (D-12, M2.2). One stream, several topics, deltas only: the
// browser takes its first picture over REST and then listens here.
//
// Why not polling: the dashboard has about ten live tiles. At the Keenetic
// refresh rate that is a request every 300 ms against a device whose whole
// budget is 256 MB of RAM and two small cores — and every one of those
// requests re-reads /proc and forks ubus. One stream that pushes a snapshot
// every 3 s costs one reader.
//
// Why not a websocket: nothing travels upstream. Commands are ordinary REST
// calls. A websocket would buy a second protocol, a second failure mode, and
// its own reconnection code — on a device whose defining feature is that it
// occasionally cuts its own network, the automatic reconnection built into
// EventSource is the feature worth having.

// systemTick is how often the system topic emits. It matches the metrics
// sampler, so a client sees each sample once.
const systemTick = metrics.LiveEvery

// maxStreams bounds concurrent SSE connections. A browser that reconnects
// faster than it disconnects (a reloading tab, a flaky link — both routine
// here) would otherwise pile up goroutines on a device that cannot spare
// them. The limit is deliberately low: this is an admin panel, not a service.
const maxStreams = 8

// SystemEvent is the `system` topic payload: the dashboard snapshot plus the
// newest metrics sample, so a sparkline can advance without a second request.
type SystemEvent struct {
	System core.SystemInfo `json:"system"`
	Sample metrics.Sample  `json:"sample"`
}

// ApplyEvent is the `apply` topic payload. It is emitted on change rather than
// on a timer: the apply bar has to react the moment a watchdog fires, and
// waiting up to 3 s to tell someone their change was rolled back is 3 s of a
// person believing the opposite.
type ApplyEvent struct {
	State core.ApplyState `json:"state"`
}

// ErrorEvent reports that the daemon could not read the device. It is a topic
// of its own because "the panel is up but the device stopped answering" is a
// state the UI must show, and an empty payload cannot say it.
type ErrorEvent struct {
	Topic  string `json:"topic"`
	Detail string `json:"detail"`
}

// registerEvents mounts GET /events.
func (s *Server) registerEvents(authed huma.Middlewares, authSec []map[string][]string) {
	sse.Register(s.api, huma.Operation{
		OperationID: "events", Method: http.MethodGet, Path: "/events",
		Summary: "Live updates: one stream, topics system/apply/error",
		Tags:    []string{"system"}, Middlewares: authed, Security: authSec,
	}, map[string]any{
		"system": SystemEvent{},
		"apply":  ApplyEvent{},
		"error":  ErrorEvent{},
	}, s.streamEvents)
}

func (s *Server) streamEvents(ctx context.Context, _ *struct{}, send sse.Sender) {
	if n := s.streams.Add(1); n > maxStreams {
		s.streams.Add(-1)
		_ = send(sse.Message{Data: ErrorEvent{
			Topic:  "error",
			Detail: "too many live connections to this device; close another tab",
		}})
		return
	}
	defer s.streams.Add(-1)

	// First frames are sent immediately: an EventSource that says nothing for
	// three seconds is indistinguishable from one that failed to connect.
	s.sendSystem(send)
	lastApply := s.apply.State()
	_ = send(sse.Message{Data: ApplyEvent{State: lastApply}})

	t := time.NewTicker(systemTick)
	defer t.Stop()
	// The apply phase is polled far more often than it is emitted: reading it
	// is a mutex and a struct copy, while missing a revert for three seconds
	// is a person staring at a lie.
	applyTick := time.NewTicker(300 * time.Millisecond)
	defer applyTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-applyTick.C:
			if st := s.apply.State(); st != lastApply {
				lastApply = st
				if err := send(sse.Message{Data: ApplyEvent{State: st}}); err != nil {
					return
				}
			}
		case <-t.C:
			if !s.sendSystem(send) {
				return
			}
		}
	}
}

// sendSystem emits one system frame. It reports false when the client is gone,
// which is how this loop learns to stop: there is no other signal that a
// browser tab was closed.
func (s *Server) sendSystem(send sse.Sender) bool {
	info, err := s.adapter.System().Info()
	if err != nil {
		return send(sse.Message{Data: ErrorEvent{Topic: "system", Detail: err.Error()}}) == nil
	}
	ev := SystemEvent{System: info}
	if live := s.history.Live(); len(live) > 0 {
		ev.Sample = live[len(live)-1]
	}
	return send(sse.Message{Data: ev}) == nil
}

// MetricsOutput is the REST half of D-12: the picture a client starts from,
// before it subscribes to deltas.
type MetricsOutput struct {
	Body struct {
		// Live is the last few minutes at the sampler's resolution.
		Live []metrics.Sample `json:"live"`
		// Day is up to 24 hours at one-minute resolution.
		Day []metrics.Sample `json:"day"`
		// BufferBytes is what this history costs in RAM. It is reported, not
		// hidden, because "metrics live in RAM only" (D-13) is a promise that
		// should be checkable from outside.
		BufferBytes int `json:"bufferBytes"`
	}
}

func (s *Server) getMetrics(_ context.Context, _ *struct{}) (*MetricsOutput, error) {
	out := &MetricsOutput{}
	out.Body.Live = s.history.Live()
	out.Body.Day = s.history.Day()
	out.Body.BufferBytes = s.history.Size()
	// Empty slices, not null: a sparkline component that iterates the response
	// should draw nothing, not throw.
	if out.Body.Live == nil {
		out.Body.Live = []metrics.Sample{}
	}
	if out.Body.Day == nil {
		out.Body.Day = []metrics.Sample{}
	}
	return out, nil
}

// StartSampling begins recording the device's vitals and returns a stop
// function. The daemon owns the lifetime; tests start and stop it explicitly.
// Sampling runs whether or not anyone is connected — see metrics.Sampler.
func (s *Server) StartSampling() (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sampler.Run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

// StreamCount is the number of live SSE connections. Exported so a test can
// prove the limit works and the counter comes back down after a disconnect.
func (s *Server) StreamCount() int { return int(s.streams.Load()) }
