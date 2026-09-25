package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/api"
	"github.com/veilbridge-os/veilbridge/internal/config"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/core/mock"
)

// refusingNetwork is the mock network plus a writer that refuses every value,
// the way the device does, so the API's translation of a refusal is tested
// on its own.
type refusingNetwork struct {
	core.NetworkManager
	err error
}

func (n refusingNetwork) StageWAN(core.WANConfig) ([]core.ConfigChange, error) { return nil, n.err }
func (n refusingNetwork) StagedChanges() ([]core.ConfigChange, error)          { return nil, nil }
func (n refusingNetwork) DiscardStaged() error                                 { return nil }
func (n refusingNetwork) StageLAN(core.LANConfig) ([]core.ConfigChange, error) {
	return nil, n.err
}
func (n refusingNetwork) StageHandout(core.HandoutConfig) ([]core.ConfigChange, error) {
	return nil, n.err
}
func (n refusingNetwork) StageReservation(core.ReservationConfig) ([]core.ConfigChange, error) {
	return nil, n.err
}
func (n refusingNetwork) RemoveReservation(string) ([]core.ConfigChange, error) { return nil, n.err }

type refusingAdapter struct {
	*mock.Adapter
	network refusingNetwork
}

func (a refusingAdapter) Network() core.NetworkManager { return a.network }

func refusingServer(t *testing.T, err error) string {
	t.Helper()
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	doc := config.Default()
	if e := doc.SetPassword(testPassword); e != nil {
		t.Fatal(e)
	}
	if e := store.Save(doc); e != nil {
		t.Fatal(e)
	}
	m := mock.NewAdapter()
	srv, e := api.New(refusingAdapter{m, refusingNetwork{m.Network(), err}}, store, api.Options{})
	if e != nil {
		t.Fatal(e)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL + "/api/v1"
}

type problem struct {
	Detail string `json:"detail"`
	Errors []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
	} `json:"errors"`
}

// #28: a refused value comes back with the field it is about, so the screen
// needs no table of phrases to find the input.
func TestARefusedValueNamesItsFieldInTheResponse(t *testing.T) {
	refused := core.Refuse("netmask", errors.New(`openwrt: "255.0.255.0" is not a contiguous network mask`))
	// Bodies that pass the schema, so the handler — not Huma's own
	// validation — is what answers.
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodPut, "/network/wan", map[string]any{"proto": "dhcp"}},
		{http.MethodPut, "/network/lan", map[string]any{"address": "192.168.1.1", "netmask": "255.0.255.0"}},
		{http.MethodPut, "/network/lan/handout", map[string]any{"enabled": true}},
		{http.MethodPut, "/network/lan/reservations", map[string]any{"mac": "1a:a6:05:03:d4:9c", "ip": "192.168.1.5"}},
	} {
		t.Run(c.path, func(t *testing.T) {
			base := refusingServer(t, refused)
			resp := do(t, c.method, base+c.path, login(t, base), c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			var p problem
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
			if p.Detail != `"255.0.255.0" is not a contiguous network mask` {
				t.Errorf("detail = %q, want the device's sentence without the package prefix", p.Detail)
			}
			if len(p.Errors) != 1 || p.Errors[0].Location != "body.netmask" {
				t.Fatalf("errors = %+v, want exactly one at body.netmask", p.Errors)
			}
		})
	}
}

// A refusal that is not about one field stays a plain 400 with no location:
// pinning it to some input would be a guess, and a guess is worse than a
// message at the top of the card.
func TestARefusalWithoutAFieldHasNoLocation(t *testing.T) {
	base := refusingServer(t, errors.New("openwrt: the local network has no IPv4 address to count from"))
	resp := do(t, http.MethodPut, base+"/network/lan/handout", login(t, base), map[string]any{"enabled": true})
	defer resp.Body.Close()
	var p problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || len(p.Errors) != 0 {
		t.Errorf("status %d, errors %+v; want 400 and no location", resp.StatusCode, p.Errors)
	}
}
