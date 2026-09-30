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
	draft []core.ConfigChange
}

func (n *draftNetwork) StageWAN(core.WANConfig) ([]core.ConfigChange, error) { return nil, nil }
func (n *draftNetwork) StagedChanges() ([]core.ConfigChange, error)          { return n.draft, nil }
func (n *draftNetwork) DiscardStaged() error                                 { n.draft = nil; return nil }

type wifiAdapter struct {
	*mock.Adapter
	wifi *fakeWiFi
	net  *draftNetwork
}

func (a wifiAdapter) WiFi() core.WiFiManager         { return a.wifi }
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

func TestWiFiWithoutRadioSupportSaysSo(t *testing.T) {
	_, base := setup(t)
	token := login(t, base)
	resp := do(t, http.MethodGet, base+"/wifi", token, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("GET /wifi on an adapter without Wi-Fi = %d, want 501", resp.StatusCode)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
