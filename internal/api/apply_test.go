package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// applyState mirrors core.ApplyState for assertions over the wire. Decoding
// into a local struct rather than the core type keeps this an API contract
// test: a field renamed in core must show up here as a failure.
type applyState struct {
	Phase      string `json:"phase"`
	Token      string `json:"token"`
	SnapshotID string `json:"snapshot_id"`
	Err        string `json:"error"`
}

func decodeState(t *testing.T, resp *http.Response) applyState {
	t.Helper()
	defer resp.Body.Close()
	var st applyState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode apply state: %v", err)
	}
	return st
}

func TestApplyHandsOutATokenAndAwaitsConfirmation(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	resp := do(t, http.MethodPost, base+"/apply", token, map[string]int{"timeout_seconds": 30})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /apply = %d, want 200", resp.StatusCode)
	}
	st := decodeState(t, resp)
	if st.Phase != "awaiting_confirm" {
		t.Fatalf("phase = %q, want awaiting_confirm", st.Phase)
	}
	if st.Token == "" {
		t.Fatal("no token returned — the change could never be confirmed")
	}

	// GET must report the same pending transaction: the apply-bar is rendered
	// from it after a page reload, which is exactly what happens when the
	// operator checks whether the panel survived.
	got := decodeState(t, do(t, http.MethodGet, base+"/apply", token, nil))
	if got.Phase != "awaiting_confirm" || got.Token != st.Token {
		t.Fatalf("GET /apply = %+v, want the pending transaction %q", got, st.Token)
	}
}

func TestConfirmClosesTheTransaction(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	st := decodeState(t, do(t, http.MethodPost, base+"/apply", token, map[string]int{}))
	resp := do(t, http.MethodPost, base+"/apply/confirm", token, map[string]string{"token": st.Token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm = %d, want 200", resp.StatusCode)
	}
	if got := decodeState(t, resp); got.Phase != "confirmed" {
		t.Fatalf("phase = %q, want confirmed", got.Phase)
	}
}

// A token from another transaction (a stale browser tab, a retried request)
// must not confirm the change currently in flight.
func TestConfirmWithAForeignTokenIsRefused(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	do(t, http.MethodPost, base+"/apply", token, map[string]int{})
	resp := do(t, http.MethodPost, base+"/apply/confirm", token, map[string]string{"token": "not-mine"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("confirm with foreign token = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestSecondApplyIsRefusedWithConflict(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	do(t, http.MethodPost, base+"/apply", token, map[string]int{})
	resp := do(t, http.MethodPost, base+"/apply", token, map[string]int{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second apply = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRevertUndoesThePendingApply(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	do(t, http.MethodPost, base+"/apply", token, map[string]int{})
	resp := do(t, http.MethodPost, base+"/apply/revert", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revert = %d, want 200", resp.StatusCode)
	}
	if got := decodeState(t, resp); got.Phase != "reverted" {
		t.Fatalf("phase = %q, want reverted", got.Phase)
	}
}

func TestConfirmWithNothingPendingIsRefused(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()
	token := login(t, base)

	resp := do(t, http.MethodPost, base+"/apply/confirm", token, map[string]string{"token": "whatever"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("confirm without apply = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()
}

// The apply endpoints change the router's configuration; an unauthenticated
// caller must not reach them.
func TestApplyEndpointsRequireAuth(t *testing.T) {
	ts, base := setup(t)
	defer ts.Close()

	for _, c := range []struct {
		method, path string
	}{
		{http.MethodPost, "/apply"},
		{http.MethodPost, "/apply/confirm"},
		{http.MethodPost, "/apply/revert"},
		{http.MethodGet, "/apply"},
	} {
		resp := do(t, c.method, base+c.path, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without a token = %d, want 401", c.method, c.path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
