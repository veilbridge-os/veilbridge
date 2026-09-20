package api_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

// --- live layer (M2.2, D-12/D-13) ---

// newServer serves an already-built Server over httptest. The metrics test
// needs it because it constructs the Server itself (to start sampling before
// the first request), which setup() does not expose.
func newServer(t *testing.T, srv *api.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// openStream connects to /events and returns a reader over the raw SSE frames.
func openStream(t *testing.T, base, token string) (*bufio.Reader, func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, base+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		resp.Body.Close()
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	return bufio.NewReader(resp.Body), func() { resp.Body.Close() }
}

// readEvent reads frames until one carrying the wanted event name arrives.
func readEvent(t *testing.T, r *bufio.Reader, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var name string
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && name == want:
			var out map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &out); err != nil {
				t.Fatalf("decode %s payload: %v", want, err)
			}
			return out
		}
	}
	t.Fatalf("no %q event within %s", want, timeout)
	return nil
}

// The first frames must arrive immediately: an EventSource that says nothing
// for three seconds is indistinguishable from one that failed to connect.
func TestEventsSendASnapshotImmediately(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	r, closeStream := openStream(t, base, token)
	defer closeStream()

	sys := readEvent(t, r, "system", 2*time.Second)
	if sys["system"] == nil {
		t.Fatalf("system event carries no snapshot: %v", sys)
	}
	info := sys["system"].(map[string]any)
	if info["platform"] == "" || info["platform"] == nil {
		t.Errorf("snapshot has no platform: %v", info)
	}

	st := readEvent(t, r, "apply", 2*time.Second)
	if st["state"] == nil {
		t.Fatalf("apply event carries no state: %v", st)
	}
	if phase := st["state"].(map[string]any)["phase"]; phase != "idle" {
		t.Errorf("phase = %v, want idle on a fresh server", phase)
	}
}

// A change to the apply transaction has to reach the panel in well under the
// system tick: waiting 3s to tell someone their change was rolled back is 3s
// of a person believing the opposite.
func TestApplyChangeIsPushedPromptly(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	r, closeStream := openStream(t, base, token)
	defer closeStream()
	readEvent(t, r, "apply", 2*time.Second) // the initial idle frame

	start := time.Now()
	resp := do(t, http.MethodPost, base+"/apply", token, map[string]any{"timeout_seconds": 60})
	resp.Body.Close()

	ev := readEvent(t, r, "apply", 2*time.Second)
	phase := ev["state"].(map[string]any)["phase"]
	if phase != "awaiting_confirm" {
		t.Fatalf("phase = %v, want awaiting_confirm", phase)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s to push an apply change; the tick is meant to be sub-second", elapsed)
	}

	// And the confirmation must come back the same way.
	token2 := ev["state"].(map[string]any)["token"]
	if token2 == nil || token2 == "" {
		t.Fatal("awaiting_confirm carries no token, so the UI cannot confirm")
	}
}

// The panel is an admin tool on a 256 MB device: a reloading tab must not be
// able to pile up streams without limit.
func TestTooManyStreamsAreRefused(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	var closers []func()
	defer func() {
		for _, c := range closers {
			c()
		}
	}()
	for i := 0; i < 8; i++ {
		r, c := openStream(t, base, token)
		closers = append(closers, c)
		readEvent(t, r, "system", 2*time.Second) // ensure it is really established
	}

	// The ninth is answered, but with a refusal rather than a stream: the
	// client learns why instead of watching a connection that never speaks.
	r, c := openStream(t, base, token)
	closers = append(closers, c)
	ev := readEvent(t, r, "error", 2*time.Second)
	if ev["detail"] == nil || !strings.Contains(ev["detail"].(string), "too many") {
		t.Errorf("ninth stream got %v, want a 'too many connections' refusal", ev)
	}
}

func TestEventsRequireAuth(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()

	resp := do(t, http.MethodGet, base+"/events", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: the live stream describes the device", resp.StatusCode)
	}
}

// The REST half of D-12: the picture a client starts from.
func TestMetricsEndpointReportsHistoryAndItsCost(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(mock.NewAdapter(), store, api.Options{})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	stop := srv.StartSampling()
	defer stop()

	ts := newServer(t, srv)
	base := ts.URL + "/api/v1"
	token := login(t, base)

	resp := do(t, http.MethodGet, base+"/system/metrics", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Live []struct {
			At       int64   `json:"at"`
			CPU      float64 `json:"cpuPercent"`
			MemTotal int64   `json:"memTotal"`
		} `json:"live"`
		Day         []struct{} `json:"day"`
		BufferBytes int        `json:"bufferBytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Sampling starts with the daemon, not with the reader, so history exists
	// before anyone subscribes.
	if len(out.Live) == 0 {
		t.Fatal("no samples recorded before the first request")
	}
	if out.Live[0].MemTotal == 0 || out.Live[0].At == 0 {
		t.Errorf("sample looks empty: %+v", out.Live[0])
	}
	// The RAM promise (D-13) is checkable from outside — and the number has to
	// be the real one. A plausible-looking small integer is exactly what a
	// broken implementation would also return, so assert the actual cost of
	// the two rings: 60 live + 1440 day samples at 40 bytes.
	if want := (60 + 1440) * 40; out.BufferBytes != want {
		t.Errorf("bufferBytes = %d, want %d (the real buffer cost)", out.BufferBytes, want)
	}
	if out.BufferBytes > 128*1024 {
		t.Errorf("the metrics buffer costs %d bytes: too much for a 256 MB router", out.BufferBytes)
	}
}
