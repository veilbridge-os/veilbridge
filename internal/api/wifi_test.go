package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

// #57, D-100: a radio edit is staged; a network's name and password are
// applied at once — and only when nothing else waits in the draft.

type fakeWiFi struct {
	asked []string
	err   error
}

func (f *fakeWiFi) Status() (core.WiFiStatus, error) {
	return core.WiFiStatus{
		Radios:   []core.WiFiRadio{{ID: "radio1", Band: "5", Enabled: true, Widths: []int{20, 80}, Channels: []core.WiFiChannel{{Channel: 36}}}},
		Networks: []core.WiFiNetwork{{ID: "net0", Kind: core.WiFiKindMain, SSID: "example-net", Security: core.WiFiWPA2, HasPassword: true}},
	}, nil
}
func (f *fakeWiFi) WiFiPassword(id string) (string, error) {
	f.asked = append(f.asked, "password "+id)
	return "old-secret-47", nil
}
func (f *fakeWiFi) StageRadio(id string, cfg core.RadioConfig) ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "radio "+id+" "+cfg.Channel)
	return []core.ConfigChange{{Label: "Channel", LabelKey: "wireless.radio.channel", From: "36", To: cfg.Channel, Dangerous: true}}, f.err
}
func (f *fakeWiFi) StageCountry(code string) ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "country "+code)
	return []core.ConfigChange{{Label: "Wi-Fi country", To: code, Dangerous: true}}, f.err
}
func (f *fakeWiFi) StageAccess(id string, cfg core.AccessConfig) ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "access "+id+" "+cfg.SSID)
	if f.err != nil {
		return nil, f.err
	}
	return []core.ConfigChange{{Label: "Network name", LabelKey: "wireless.wifiNet.ssid", From: "example-net", To: cfg.SSID}}, nil
}

// draftNetwork is the mock network with a draft that may hold edits.
type draftNetwork struct {
	core.NetworkManager
	draft    []core.ConfigChange
	draftErr error
}

func (n *draftNetwork) StageWAN(core.WANConfig) ([]core.ConfigChange, error) { return nil, nil }
func (n *draftNetwork) StagedChanges() ([]core.ConfigChange, error)          { return n.draft, n.draftErr }
func (n *draftNetwork) DiscardStaged() error                                 { n.draft = nil; return nil }

type wifiAdapter struct {
	*mock.Adapter
	wifi *fakeWiFi
	net  *draftNetwork
}

func (a wifiAdapter) WiFi() core.WiFiManager       { return a.wifi }
func (a wifiAdapter) Network() core.NetworkManager { return a.net }

func wifiServer(t *testing.T) (base, token string, a wifiAdapter) {
	t.Helper()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	m := mock.NewDemoAdapter()
	a = wifiAdapter{Adapter: m, wifi: &fakeWiFi{}, net: &draftNetwork{NetworkManager: m.Network()}}
	srv, err := api.New(a, store, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base = newServer(t, srv).URL + "/api/v1"
	return base, login(t, base), a
}

func commits(a wifiAdapter) int { return a.Adapter.Applier().(*mock.Applier).Commits }

func TestWiFiNameAndPasswordApplyAtOnce(t *testing.T) {
	base, token, a := wifiServer(t)
	resp := do(t, http.MethodPut, base+"/wifi/networks/net0", token, map[string]string{"ssid": "Дача", "password": "river-lamp-2026"})
	var out struct {
		Changes []core.ConfigChange `json:"changes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(out.Changes) != 1 {
		t.Fatalf("PUT access = %d %+v", resp.StatusCode, out)
	}
	if commits(a) != 1 {
		t.Errorf("commits = %d, want the edit applied at once", commits(a))
	}
	// No watchdog: nothing awaits confirmation, so nothing will be reverted.
	st := decodeState(t, do(t, http.MethodGet, base+"/apply", token, nil))
	if st.Phase != "idle" || st.Token != "" {
		t.Errorf("apply state after an access edit = %+v, want idle with no token", st)
	}
}

func TestWiFiAccessEditRefusedWhileTheDraftHoldsOtherEdits(t *testing.T) {
	base, token, a := wifiServer(t)
	a.net.draft = []core.ConfigChange{{Label: "Channel", Dangerous: true}}
	resp := do(t, http.MethodPut, base+"/wifi/networks/net0", token, map[string]string{"ssid": "Дача"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT access with a draft = %d, want 409", resp.StatusCode)
	}
	if commits(a) != 0 || len(a.wifi.asked) != 0 {
		t.Errorf("commits=%d asked=%v, want nothing staged or applied", commits(a), a.wifi.asked)
	}
}

func TestWiFiAccessEditRefusedWhileAnApplyAwaitsConfirmation(t *testing.T) {
	base, token, a := wifiServer(t)
	do(t, http.MethodPost, base+"/apply", token, map[string]int{}).Body.Close()
	resp := do(t, http.MethodPut, base+"/wifi/networks/net0", token, map[string]string{"ssid": "Дача"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT access during an apply = %d, want 409", resp.StatusCode)
	}
	if len(a.wifi.asked) != 0 {
		t.Errorf("staged %v into a transaction awaiting confirmation", a.wifi.asked)
	}
}

func TestWiFiAccessRefusalNamesItsField(t *testing.T) {
	base, token, a := wifiServer(t)
	a.wifi.err = core.Refuse("password", errString("too short"))
	resp := do(t, http.MethodPut, base+"/wifi/networks/net0", token, map[string]string{"ssid": "x", "password": "1"})
	var body struct {
		Errors []struct {
			Location string `json:"location"`
		} `json:"errors"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || len(body.Errors) == 0 || body.Errors[0].Location != "body.password" {
		t.Errorf("refusal = %d %+v, want 400 at body.password", resp.StatusCode, body)
	}
	if commits(a) != 0 {
		t.Error("a refused edit was committed")
	}
}

func TestWiFiRadioIsStagedNotApplied(t *testing.T) {
	base, token, a := wifiServer(t)
	resp := do(t, http.MethodPut, base+"/wifi/radios/radio1", token, map[string]any{"enabled": true, "channel": "auto", "width": 80})
	var out struct {
		Dangerous bool `json:"dangerous"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 200 || !out.Dangerous {
		t.Fatalf("PUT radio = %d dangerous=%v, want a staged dangerous change", resp.StatusCode, out.Dangerous)
	}
	if commits(a) != 0 {
		t.Error("a radio edit was applied without the confirmation window")
	}
}

func TestWiFiStatusHasNoPasswordAndThePasswordIsAskedFor(t *testing.T) {
	base, token, a := wifiServer(t)
	resp := do(t, http.MethodGet, base+"/wifi", token, nil)
	var raw strings.Builder
	var buf [4096]byte
	n, _ := resp.Body.Read(buf[:])
	raw.Write(buf[:n])
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(raw.String(), "old-secret") {
		t.Fatalf("GET /wifi = %d %s", resp.StatusCode, raw.String())
	}
	if !strings.Contains(raw.String(), `"here":{"kind":"other"}`) {
		t.Errorf("GET /wifi does not say how the caller is connected: %s", raw.String())
	}
	resp = do(t, http.MethodGet, base+"/wifi/networks/net0/password", token, nil)
	var p struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()
	if p.Password != "old-secret-47" || len(a.wifi.asked) != 1 {
		t.Errorf("password = %q asked=%v", p.Password, a.wifi.asked)
	}
}

// noWiFi hides the Wi-Fi manager: only the core.Adapter methods are promoted.
type noWiFi struct{ core.Adapter }

func TestWiFiWithoutRadioSupportSaysSo(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if err := doc.SetPassword(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(doc); err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(noWiFi{mock.NewAdapter()}, store, api.Options{})
	if err != nil {
		t.Fatal(err)
	}
	base := newServer(t, srv).URL + "/api/v1"
	token := login(t, base)
	resp := do(t, http.MethodGet, base+"/wifi", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET /wifi on an adapter without Wi-Fi = %d, want 501", resp.StatusCode)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// #58: every guest edit is staged for the apply bar (it changes the network
// and the firewall) and is refused on top of other staged edits.

func (f *fakeWiFi) StageGuest(cfg core.GuestConfig) ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "guest "+cfg.SSID)
	if f.err != nil {
		return nil, f.err
	}
	return []core.ConfigChange{{Label: "Guest network", LabelKey: "wireless.guest.section", To: "on|2.4,5|" + cfg.SSID, Dangerous: true}}, nil
}
func (f *fakeWiFi) StageGuestEnabled(on bool) ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "guest enabled")
	return []core.ConfigChange{{Label: "Guest network", Dangerous: true}}, f.err
}
func (f *fakeWiFi) StageGuestRemove() ([]core.ConfigChange, error) {
	f.asked = append(f.asked, "guest remove")
	return []core.ConfigChange{{Label: "Guest network", Dangerous: true}}, f.err
}

func TestWiFiGuestIsStagedNotApplied(t *testing.T) {
	base, token, a := wifiServer(t)
	for _, r := range []struct {
		method string
		body   any
	}{
		{http.MethodPost, map[string]string{"ssid": "Гости", "password": "guest-tea-815"}},
		{http.MethodPut, map[string]bool{"enabled": false}},
		{http.MethodDelete, nil},
	} {
		resp := do(t, r.method, base+"/wifi/guest", token, r.body)
		var out struct {
			Changes   []core.ConfigChange `json:"changes"`
			Dangerous bool                `json:"dangerous"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(out.Changes) != 1 || !out.Dangerous {
			t.Errorf("%s /wifi/guest = %d %+v", r.method, resp.StatusCode, out)
		}
	}
	if commits(a) != 0 {
		t.Errorf("commits = %d, want the guest edits only staged", commits(a))
	}
	if want := []string{"guest Гости", "guest enabled", "guest remove"}; strings.Join(a.wifi.asked, "|") != strings.Join(want, "|") {
		t.Errorf("asked = %q", a.wifi.asked)
	}
}

func TestWiFiGuestRefusals(t *testing.T) {
	body := map[string]string{"ssid": "Гости", "password": "guest-tea-815"}
	cases := []struct {
		name  string
		setup func(base, token string, a wifiAdapter)
		want  int
	}{
		{"other edits in the draft", func(_, _ string, a wifiAdapter) {
			a.net.draft = []core.ConfigChange{{Label: "Channel", Dangerous: true}}
		}, http.StatusConflict},
		{"an apply awaits confirmation", func(base, token string, _ wifiAdapter) {
			do(t, http.MethodPost, base+"/apply", token, map[string]int{}).Body.Close()
		}, http.StatusConflict},
		{"a guest network exists", func(_, _ string, a wifiAdapter) { a.wifi.err = core.ErrGuestExists }, http.StatusConflict},
		// A draft that cannot be read is not an empty one (review of #58).
		{"the draft cannot be read", func(_, _ string, a wifiAdapter) {
			a.net.draftErr = errors.New("uci: I/O error")
		}, http.StatusBadGateway},
		{"the uplink shares a zone with the home network", func(_, _ string, a wifiAdapter) {
			a.wifi.err = core.ErrGuestBlocked
		}, http.StatusUnprocessableEntity},
		{"the device cannot", func(_, _ string, a wifiAdapter) { a.wifi.err = core.ErrNotImplemented }, http.StatusNotImplemented},
		{"a refused field", func(_, _ string, a wifiAdapter) {
			a.wifi.err = core.Refuse("password", errors.New("the password is 0 characters long"))
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		base, token, a := wifiServer(t)
		tc.setup(base, token, a)
		resp := do(t, http.MethodPost, base+"/wifi/guest", token, body)
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: POST /wifi/guest = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
	base, token, a := wifiServer(t)
	a.wifi.err = core.ErrNoGuest
	resp := do(t, http.MethodDelete, base+"/wifi/guest", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE without a guest network = %d, want 404", resp.StatusCode)
	}
}

// Review of #58: while a guest network waits in the draft, other edits are
// refused - a writer whose own edit fails drops its whole file's draft, and
// would take the guest network's firewall half with it.
func TestOtherEditsRefusedWhileAGuestNetworkWaitsInTheDraft(t *testing.T) {
	base, token, a := wifiServer(t)
	a.net.draft = []core.ConfigChange{{Label: "Guest network", LabelKey: "wireless.guest.section", To: "on|2.4,5|Гости", Dangerous: true}}
	resp := do(t, http.MethodPut, base+"/wifi/radios/radio1", token, map[string]any{"enabled": true, "channel": "36", "width": 80})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("radio edit with a guest network in the draft = %d, want 409", resp.StatusCode)
	}
	if len(a.wifi.asked) != 0 {
		t.Errorf("asked = %q, want nothing staged", a.wifi.asked)
	}
	// Any other row in the draft does not stop a radio edit.
	a.net.draft = []core.ConfigChange{{Label: "Channel", LabelKey: "wireless.radio.channel", Dangerous: true}}
	resp = do(t, http.MethodPut, base+"/wifi/radios/radio1", token, map[string]any{"enabled": true, "channel": "36", "width": 80})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("radio edit next to another draft row = %d, want 200", resp.StatusCode)
	}
}

// Review of #58, round 6: a draft that cannot be read is not proof that no
// guest network waits in it.
func TestEditsRefusedWhenTheDraftCannotBeRead(t *testing.T) {
	base, token, a := wifiServer(t)
	a.net.draftErr = errors.New("uci: I/O error")
	resp := do(t, http.MethodPut, base+"/wifi/radios/radio1", token, map[string]any{"enabled": true, "channel": "36", "width": 80})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || len(a.wifi.asked) != 0 {
		t.Errorf("radio edit with an unreadable draft = %d asked %q, want 502 and nothing staged", resp.StatusCode, a.wifi.asked)
	}
}
