package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

const testPassword = "hunter2"

// setup builds a Server backed by the mock adapter with the test password set,
// served over httptest, and returns a client + base URL.
func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
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
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, ts.URL + "/api/v1"
}

func login(t *testing.T, base string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": testPassword})
	resp, err := http.Post(base+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatal("empty token")
	}
	return out.Token
}

func do(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func TestLoginWrongPassword(t *testing.T) {
	_, base := setup(t)
	body, _ := json.Marshal(map[string]string{"password": "wrong"})
	resp, err := http.Post(base+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong password status = %d, want 401", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	_, base := setup(t)
	// No token → 401 on a protected endpoint.
	resp := do(t, http.MethodGet, base+"/nodes", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("no-token status = %d, want 401", resp.StatusCode)
	}
}

func TestNodesFlow(t *testing.T) {
	_, base := setup(t)
	tok := login(t, base)

	// Empty initially.
	resp := do(t, http.MethodGet, base+"/nodes", tok, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("list nodes status = %d", resp.StatusCode)
	}

	// Import a node (mock fabricates one).
	resp = do(t, http.MethodPost, base+"/nodes/import", tok, map[string]string{"kind": "awg-config", "content": "dummy"})
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("import status = %d, want 201", resp.StatusCode)
	}
	var imported []struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&imported)
	if len(imported) != 1 || imported[0].ID == "" {
		t.Fatalf("import returned %+v", imported)
	}
	id := imported[0].ID

	// Activate it → 200 + SystemInfo.
	resp = do(t, http.MethodPost, base+"/nodes/"+id+"/activate", tok, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("activate status = %d", resp.StatusCode)
	}
}

func TestRoutesFlow(t *testing.T) {
	_, base := setup(t)
	tok := login(t, base)

	// Add a rule.
	resp := do(t, http.MethodPost, base+"/routes", tok, map[string]string{
		"kind": "subnet", "value": "128.116.0.0/17", "target": "tunnel",
	})
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("add route status = %d, want 201", resp.StatusCode)
	}
	var rule struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&rule)
	if rule.ID == "" {
		t.Fatal("rule got no ID")
	}

	// List shows it.
	resp2 := do(t, http.MethodGet, base+"/routes", tok, nil)
	defer resp2.Body.Close()
	var rules []map[string]any
	json.NewDecoder(resp2.Body).Decode(&rules)
	if len(rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(rules))
	}

	// Delete it → 204.
	resp3 := do(t, http.MethodDelete, base+"/routes/"+rule.ID, tok, nil)
	resp3.Body.Close()
	if resp3.StatusCode != 204 {
		t.Errorf("delete status = %d, want 204", resp3.StatusCode)
	}
}

func TestSystemAndProbe(t *testing.T) {
	_, base := setup(t)
	tok := login(t, base)

	resp := do(t, http.MethodGet, base+"/system", tok, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("system status = %d", resp.StatusCode)
	}
	var info struct {
		Platform string `json:"platform"`
		TunnelUp bool   `json:"tunnelUp"`
	}
	json.NewDecoder(resp.Body).Decode(&info)
	if info.Platform == "" {
		t.Error("system info missing platform")
	}

	resp2 := do(t, http.MethodPost, base+"/system/probe", tok, map[string]string{
		"target": "youtube.com", "expectedVia": "tunnel",
	})
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("probe status = %d", resp2.StatusCode)
	}
}

// TestDevSpecDisabledByDefault: /openapi.json must be absent in production mode.
func TestDevSpecDisabledByDefault(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Get(ts.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get spec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("OpenAPI spec should be disabled when Dev=false (D-9)")
	}
}

func TestDevSpecEnabled(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	doc.SetPassword(testPassword)
	store.Save(doc)
	srv, err := api.New(mock.NewAdapter(), store, api.Options{Dev: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get spec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("OpenAPI spec should be served when Dev=true, got %d", resp.StatusCode)
	}
}
