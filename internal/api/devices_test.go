package api_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

// #51. Names and "known" are the panel's notes: they take effect at once and
// survive a restart; the router's own view comes from the adapter.

func setupDevices(t *testing.T) (base, token string, store *config.Store) {
	t.Helper()
	store = config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(mock.NewDemoAdapter(), store, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ts := newServer(t, srv)
	base = ts.URL + "/api/v1"
	return base, login(t, base), store
}

func listDevices(t *testing.T, base, token string) map[string]core.Device {
	t.Helper()
	resp := do(t, http.MethodGet, base+"/devices", token, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /devices = %d", resp.StatusCode)
	}
	var out core.DeviceList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	m := map[string]core.Device{}
	for _, d := range out.Devices {
		m[d.MAC] = d
	}
	return m
}

func TestDevicesAreNewUntilNamedOrKnown(t *testing.T) {
	base, token, store := setupDevices(t)
	before := listDevices(t, base, token)
	if !before["00:00:5e:00:53:10"].New || !before["02:0d:33:7a:55:c2"].New {
		t.Fatalf("nothing said about them yet, want new: %+v", before)
	}

	// Upper case in the path is the same device.
	resp := do(t, http.MethodPut, base+"/devices/00:00:5E:00:53:10/name", token,
		map[string]string{"name": "  Телевизор в гостиной (LG) "})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("name = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodPost, base+"/devices/known", token,
		map[string][]string{"macs": {"02:0d:33:7a:55:c2"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("known = %d", resp.StatusCode)
	}

	after := listDevices(t, base, token)
	if d := after["00:00:5e:00:53:10"]; d.New || d.Name != "Телевизор в гостиной (LG)" {
		t.Errorf("named = %+v", d)
	}
	if d := after["02:0d:33:7a:55:c2"]; d.New || d.Name != "" {
		t.Errorf("known = %+v", d)
	}
	if !after["00:00:5e:00:53:21"].New {
		t.Error("an untouched device lost its mark")
	}

	// Written where a restart finds it.
	doc, _ := store.Load()
	if len(doc.Devices) != 2 {
		t.Errorf("stored notes = %+v", doc.Devices)
	}

	// Removing the name keeps the device known; forgetting makes it new.
	resp = do(t, http.MethodPut, base+"/devices/00:00:5e:00:53:10/name", token, map[string]string{"name": ""})
	resp.Body.Close()
	if d := listDevices(t, base, token)["00:00:5e:00:53:10"]; d.New || d.Name != "" {
		t.Errorf("name removed = %+v, want no name and still known", d)
	}
	resp = do(t, http.MethodDelete, base+"/devices/00:00:5e:00:53:10", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forget = %d", resp.StatusCode)
	}
	if d := listDevices(t, base, token)["00:00:5e:00:53:10"]; !d.New {
		t.Errorf("forgotten = %+v, want new again", d)
	}
}

func TestDeviceRefusalsNameTheirField(t *testing.T) {
	base, token, _ := setupDevices(t)
	cases := []struct {
		method, path string
		body         any
		location     string
	}{
		{http.MethodPut, "/devices/phone/name", map[string]string{"name": "x"}, "path.mac"},
		{http.MethodPut, "/devices/00:00:5e:00:53:10/name", map[string]string{"name": strings.Repeat("я", 65)}, "body.name"},
		{http.MethodPost, "/devices/known", map[string][]string{"macs": {"00:00:5e:00:53:10", "01:00:5e:00:00:01"}}, "body.macs"},
		{http.MethodDelete, "/devices/nope", nil, "path.mac"},
	}
	for _, c := range cases {
		resp := do(t, c.method, base+c.path, token, c.body)
		var out struct {
			Errors []struct {
				Location string `json:"location"`
			} `json:"errors"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || len(out.Errors) == 0 || out.Errors[0].Location != c.location {
			t.Errorf("%s %s = %d %+v, want 400 at %s", c.method, c.path, resp.StatusCode, out.Errors, c.location)
		}
	}
	// A refused batch writes nothing, not the half before the bad address.
	if d := listDevices(t, base, token)["00:00:5e:00:53:10"]; !d.New {
		t.Errorf("a refused batch marked %+v known", d)
	}
}

func TestDevicesNeedALogin(t *testing.T) {
	base, _, _ := setupDevices(t)
	resp := do(t, http.MethodGet, base+"/devices", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
