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

// #53. A router that can turn a device's internet off says so in the list,
// and the change is a staged firewall change. One that cannot says that too,
// and the action answers 501 rather than pretending.
type blockingDevices struct {
	*mock.Device
	asked []string
}

func (b *blockingDevices) StageDeviceInternet(mac string, allowed bool) ([]core.ConfigChange, error) {
	b.asked = append(b.asked, mac)
	if allowed {
		return nil, nil
	}
	return []core.ConfigChange{{Label: "No internet for a device", LabelKey: "firewall.noInternet.section",
		To: mac, Dangerous: true, Detail: "firewall.vb_noinet_x"}}, nil
}

func (b *blockingDevices) StageDeviceSchedule(mac string, s *core.InternetSchedule) ([]core.ConfigChange, error) {
	b.asked = append(b.asked, "schedule "+mac)
	to := ""
	if s != nil {
		to = s.Words()
	}
	return []core.ConfigChange{{Label: "Internet schedule", LabelKey: "firewall.schedule.section",
		To: to, Subject: mac, Dangerous: true}}, nil
}

type blockingAdapter struct {
	*mock.Adapter
	dev *blockingDevices
}

func (a blockingAdapter) Device() core.DeviceManager { return a.dev }

func TestTurningADevicesInternetOffIsAStagedFirewallChange(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	demo := mock.NewDemoAdapter()
	dev := &blockingDevices{Device: demo.Device().(*mock.Device)}
	srv, err := api.New(blockingAdapter{Adapter: demo, dev: dev}, store, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := newServer(t, srv).URL + "/api/v1"
	token := login(t, base)

	resp := do(t, http.MethodGet, base+"/devices", token, nil)
	var list core.DeviceList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !list.InternetControl {
		t.Error("internetControl = false on a router that can do it")
	}
	for _, d := range list.Devices {
		if d.Here {
			t.Errorf("%s marked as here for a request from the loopback", d.MAC)
		}
		if d.Internet == "" {
			t.Errorf("%s: internet state missing", d.MAC)
		}
	}

	resp = do(t, http.MethodPut, base+"/devices/02:0D:33:7A:55:C2/internet", token, map[string]bool{"allowed": false})
	var out struct {
		Changes   []core.ConfigChange `json:"changes"`
		Dangerous bool                `json:"dangerous"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(out.Changes) != 1 || !out.Dangerous || out.Changes[0].To != "02:0d:33:7a:55:c2" {
		t.Fatalf("PUT internet = %d %+v, want one dangerous staged row", resp.StatusCode, out)
	}

	resp = do(t, http.MethodPut, base+"/devices/nonsense/internet", token, map[string]bool{"allowed": false})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a path that is not a device = %d, want 400", resp.StatusCode)
	}
	if len(dev.asked) != 1 {
		t.Errorf("adapter asked %v, want only the valid request", dev.asked)
	}
}

func TestAScheduleIsStagedAndRemovedThroughTheAPI(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	demo := mock.NewDemoAdapter()
	dev := &blockingDevices{Device: demo.Device().(*mock.Device)}
	srv, err := api.New(blockingAdapter{Adapter: demo, dev: dev}, store, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := newServer(t, srv).URL + "/api/v1"
	token := login(t, base)

	resp := do(t, http.MethodPut, base+"/devices/02:0d:33:7a:55:c2/schedule", token,
		map[string]any{"days": []string{"fri"}, "from": "22:00", "to": "07:00"})
	var out struct {
		Changes []core.ConfigChange `json:"changes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(out.Changes) != 1 || out.Changes[0].To != "fri 22:00-07:00" {
		t.Fatalf("PUT schedule = %d %+v", resp.StatusCode, out)
	}
	resp = do(t, http.MethodDelete, base+"/devices/02:0d:33:7a:55:c2/schedule", token, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("DELETE schedule = %d", resp.StatusCode)
	}
	want := []string{"schedule 02:0d:33:7a:55:c2", "schedule 02:0d:33:7a:55:c2"}
	if strings.Join(dev.asked, "|") != strings.Join(want, "|") {
		t.Errorf("adapter asked %v, want %v", dev.asked, want)
	}
}

func TestARouterThatCannotTurnInternetOffSaysSo(t *testing.T) {
	base, token, _ := setupDevices(t)
	resp := do(t, http.MethodGet, base+"/devices", token, nil)
	var list core.DeviceList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if list.InternetControl {
		t.Error("internetControl = true on the demo, which cannot do it")
	}
	resp = do(t, http.MethodPut, base+"/devices/02:0d:33:7a:55:c2/internet", token, map[string]bool{"allowed": false})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("PUT internet on the demo = %d, want 501", resp.StatusCode)
	}
}
