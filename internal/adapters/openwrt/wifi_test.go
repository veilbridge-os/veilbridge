package openwrt

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #57. Shapes captured on the reference router (25.12.5) on 30.09.2026: the
// network renamed, the password and addresses swapped for placeholders.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type wifiFixture struct {
	t *testing.T
	r *fakeRunner
	m *wifiManager
}

func newWiFiFixture(t *testing.T) *wifiFixture {
	t.Helper()
	r := newFakeRunner()
	show := readFixture(t, "wireless-25.12.5.uci")
	// The stand's password is not in the fixture; this one is.
	show = []byte(strings.ReplaceAll(string(show), "'<redacted>'", "'old-secret-47'"))
	r.out["uci -q show wireless"] = show
	// `uci -q get` answers what the file holds, so a stage reads real
	// "before" values.
	for _, line := range strings.Split(string(show), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			r.out["uci -q get "+k+"\x00"] = []byte(unquoteUCI(v) + "\n")
		}
	}
	r.out["/bin/ubus call network.wireless status"] = readFixture(t, "wireless-status-25.12.5.json")
	for _, p := range []string{"0", "1"} {
		r.out[`/bin/ubus call iwinfo phyname {"section":"radio`+p+`"}`] = []byte(`{"phyname":"phy` + p + `"}`)
		r.out[`/bin/ubus call iwinfo info {"device":"phy`+p+`"}`] = readFixture(t, "iwinfo-info-phy"+p+"-25.12.5.json")
		r.out[`/bin/ubus call iwinfo freqlist {"device":"phy`+p+`"}`] = readFixture(t, "iwinfo-freqlist-phy"+p+"-25.12.5.json")
	}
	r.out["/bin/ubus call hostapd.phy0-ap0 get_status"] = []byte(`{"status":"ENABLED","channel":1,"freq":2412}`)
	r.out["/bin/ubus call hostapd.phy1-ap0 get_status"] = []byte(`{"status":"ENABLED","channel":36,"freq":5180}`)
	r.out["/bin/ubus call hostapd.phy0-ap0 get_clients"] = []byte(clients24Empty)
	r.out["/bin/ubus call hostapd.phy1-ap0 get_clients"] = []byte(clients5)
	// The fake runner matches by prefix; the NUL keeps `…radio1.channel`
	// from answering for `…radio1.channel_x`. Strip it on the way in.
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "uci" && len(args) == 3 && args[0] == "-q" && args[1] == "get" {
			if out, ok := r.out["uci -q get "+args[2]+"\x00"]; ok {
				r.calls = append(r.calls, "uci -q get "+args[2])
				return out, nil
			}
			r.calls = append(r.calls, "uci -q get "+args[2])
			return []byte{}, errors.New("uci: Entry not found")
		}
		return r.run(ctx, name, args...)
	}
	n := networkManager{run: run}
	return &wifiFixture{t: t, r: r, m: newWiFiManager(n, ubus.NewWithRunner(run))}
}

func (f *wifiFixture) sets() []string {
	var out []string
	for _, c := range f.r.calls {
		if s, ok := strings.CutPrefix(c, "uci set "); ok {
			out = append(out, s)
		}
		if s, ok := strings.CutPrefix(c, "uci delete "); ok {
			out = append(out, "-"+s)
		}
	}
	return out
}

func (f *wifiFixture) status() core.WiFiStatus {
	f.t.Helper()
	st, err := f.m.Status()
	if err != nil {
		f.t.Fatalf("Status: %v", err)
	}
	for _, c := range f.r.calls {
		prog := strings.Fields(c)[0]
		if !allowedCommands[prog] && prog != "/bin/ubus" {
			f.t.Errorf("ran %q: the device would refuse it", c)
		}
	}
	return st
}

func TestWiFiStatusReadsTheReferenceRouter(t *testing.T) {
	st := newWiFiFixture(t).status()
	if st.Country != "" {
		t.Errorf("country = %q, want none (the router is on 00)", st.Country)
	}
	if len(st.Radios) != 2 {
		t.Fatalf("radios = %+v", st.Radios)
	}
	r5 := st.Radios[1]
	if r5.ID != "radio1" || r5.Band != "5" || r5.Auto || r5.Channel != 36 || r5.ChannelNow != 36 ||
		r5.Width != 80 || !r5.Enabled || r5.State != core.RadioUp || r5.Devices != 1 {
		t.Errorf("5 GHz radio = %+v", r5)
	}
	if !slices.Equal(r5.Widths, []int{20, 40, 80, 160}) {
		t.Errorf("widths = %v", r5.Widths)
	}
	var chans []int
	for _, c := range r5.Channels {
		chans = append(chans, c.Channel)
		if c.Radar {
			t.Errorf("channel %d offered with radar while no country is set (D-102)", c.Channel)
		}
	}
	if !slices.Equal(chans, []int{36, 40, 44, 48}) {
		t.Errorf("5 GHz channels without a country = %v, want 36-48 only", chans)
	}
	if r24 := st.Radios[0]; r24.Band != "2.4" || r24.Devices != 0 || !slices.Equal(r24.Widths, []int{20, 40}) {
		t.Errorf("2.4 GHz radio = %+v", r24)
	}
	if len(st.Networks) != 1 {
		t.Fatalf("networks = %+v, want the two access points as one network", st.Networks)
	}
	n := st.Networks[0]
	if n.ID != "default_radio0" || n.Kind != core.WiFiKindMain || n.SSID != "example-net" ||
		n.Security != core.WiFiWPA2 || !n.HasPassword || n.Devices != 1 ||
		!slices.Equal(n.Radios, []string{"radio0", "radio1"}) {
		t.Errorf("network = %+v", n)
	}
}

// With a country the radar channels open up, marked (D-102).
func TestWiFiRadarChannelsNeedACountry(t *testing.T) {
	f := newWiFiFixture(t)
	info := strings.Replace(string(f.r.out[`/bin/ubus call iwinfo info {"device":"phy1"}`]), `"country": "00"`, `"country": "DE"`, 1)
	f.r.out[`/bin/ubus call iwinfo info {"device":"phy1"}`] = []byte(info)
	st := f.status()
	radar := map[int]bool{}
	for _, c := range st.Radios[1].Channels {
		radar[c.Channel] = c.Radar
	}
	if r, ok := radar[52]; !ok || !r {
		t.Errorf("channel 52 with a country: offered=%v radar=%v, want offered with radar", ok, r)
	}
	if radar[36] {
		t.Error("channel 36 marked as radar")
	}
	if _, ok := radar[149]; ok {
		t.Error("channel 149 is no-IR on this device and must not be offered")
	}
}

func TestWiFiNoRadioIsNotAnError(t *testing.T) {
	f := newWiFiFixture(t)
	f.r.fail["uci -q show wireless"] = errors.New("uci: Entry not found")
	if _, err := f.m.Status(); !errors.Is(err, core.ErrNoWiFi) {
		t.Errorf("err = %v, want ErrNoWiFi", err)
	}
}

func TestStageRadioChannelAndWidth(t *testing.T) {
	f := newWiFiFixture(t)
	changes, err := f.m.StageRadio("radio1", core.RadioConfig{Enabled: true, Channel: "44", Width: 40})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wireless.radio1.channel=44", "wireless.radio1.htmode=HE40"}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("staged %v, want %v (disabled='0' already means on: no edit)", got, want)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	w := changes[1]
	if w.LabelKey != "wireless.radio.htmode" || w.From != "80" || w.To != "40" || !w.Dangerous || w.Subject != "Wi-Fi 5 GHz" {
		t.Errorf("width row = %+v, want 80 → 40 in MHz, dangerous, about the 5 GHz radio", w)
	}
	for _, c := range f.r.calls {
		if strings.Contains(c, "commit") {
			t.Fatal("staging committed")
		}
	}
}

func TestStageRadioRefusesWhatWillNotComeUp(t *testing.T) {
	for _, tc := range []struct {
		cfg   core.RadioConfig
		field string
	}{
		{core.RadioConfig{Enabled: true, Channel: "52"}, "channel"},  // radar, no country
		{core.RadioConfig{Enabled: true, Channel: "149"}, "channel"}, // no-IR
		{core.RadioConfig{Enabled: true, Channel: "abc"}, "channel"},
		{core.RadioConfig{Enabled: true, Width: 320}, "width"},
	} {
		f := newWiFiFixture(t)
		_, err := f.m.StageRadio("radio1", tc.cfg)
		var fe *core.FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%+v: err = %v, want a refusal of %s", tc.cfg, err, tc.field)
		}
		if len(f.sets()) != 0 {
			t.Errorf("%+v: staged %v before refusing", tc.cfg, f.sets())
		}
	}
	f := newWiFiFixture(t)
	if _, err := f.m.StageRadio("radio9", core.RadioConfig{Enabled: true}); err == nil {
		t.Error("an unknown radio was accepted")
	}
}

func TestStageRadioOffAndAuto(t *testing.T) {
	f := newWiFiFixture(t)
	if _, err := f.m.StageRadio("radio0", core.RadioConfig{Enabled: false, Channel: "auto"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"wireless.radio0.disabled=1", "wireless.radio0.channel=auto"}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("staged %v, want %v", got, want)
	}
}

func TestStageCountry(t *testing.T) {
	f := newWiFiFixture(t)
	changes, err := f.m.StageCountry("de")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"wireless.radio0.country=DE", "wireless.radio1.country=DE"}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("staged %v, want %v", got, want)
	}
	if len(changes) != 1 || changes[0].To != "DE" {
		t.Errorf("changes = %+v, want one row for one choice", changes)
	}
	for _, bad := range []string{"00", "D", "RUS", "1a"} {
		var fe *core.FieldError
		if _, err := newWiFiFixture(t).m.StageCountry(bad); !errors.As(err, &fe) {
			t.Errorf("country %q accepted", bad)
		}
	}
}

func TestStageAccessWritesEveryRadioOfTheNetwork(t *testing.T) {
	f := newWiFiFixture(t)
	changes, err := f.m.StageAccess("default_radio1", core.AccessConfig{
		SSID: "Дача", Password: "river-lamp-2026", Security: core.WiFiWPA2WPA3,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"wireless.default_radio0.ssid=Дача", "wireless.default_radio1.ssid=Дача",
		"wireless.default_radio0.key=river-lamp-2026", "wireless.default_radio1.key=river-lamp-2026",
		"wireless.default_radio0.encryption=sae-mixed", "wireless.default_radio1.encryption=sae-mixed",
	}
	if got := f.sets(); !slices.Equal(got, want) {
		t.Errorf("staged:\n got %v\nwant %v", got, want)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want one row per field, not per radio", changes)
	}
	if p := changes[1]; p.LabelKey != "wireless.wifiNet.key" || strings.Contains(p.From+p.To, "secret") || strings.Contains(p.To, "river") {
		t.Errorf("password row = %+v, want it hidden", p)
	}
	if s := changes[2]; s.From != core.WiFiWPA2 || s.To != core.WiFiWPA2WPA3 {
		t.Errorf("security row = %+v", s)
	}
}

func TestStageAccessRefusals(t *testing.T) {
	for _, tc := range []struct {
		cfg   core.AccessConfig
		field string
	}{
		{core.AccessConfig{SSID: ""}, "ssid"},
		{core.AccessConfig{SSID: "Квартира Ивановых на пятом"}, "ssid"}, // 49 bytes
		{core.AccessConfig{SSID: "ok", Password: "1234567"}, "password"},
		{core.AccessConfig{SSID: "ok", Password: "пароль-кириллицей"}, "password"},
		{core.AccessConfig{SSID: "ok", Security: core.WiFiWPA3}, "security"}, // not offered, D-104
	} {
		f := newWiFiFixture(t)
		_, err := f.m.StageAccess("default_radio0", tc.cfg)
		var fe *core.FieldError
		if !errors.As(err, &fe) || fe.Field != tc.field {
			t.Errorf("%+v: err = %v, want a refusal of %s", tc.cfg, err, tc.field)
		}
		if len(f.sets()) != 0 {
			t.Errorf("%+v: staged before refusing", tc.cfg)
		}
	}
}

func TestStageAccessSameValuesIsNoEdit(t *testing.T) {
	f := newWiFiFixture(t)
	changes, err := f.m.StageAccess("default_radio0", core.AccessConfig{SSID: "example-net", Password: "old-secret-47", Security: core.WiFiWPA2})
	if err != nil || len(changes) != 0 || len(f.sets()) != 0 {
		t.Errorf("changes=%v sets=%v err=%v, want nothing", changes, f.sets(), err)
	}
}

func TestWiFiPassword(t *testing.T) {
	f := newWiFiFixture(t)
	if p, err := f.m.WiFiPassword("default_radio1"); err != nil || p != "old-secret-47" {
		t.Errorf("password = %q, %v", p, err)
	}
	if _, err := f.m.WiFiPassword("nosuch"); err == nil {
		t.Error("a password for a network that does not exist")
	}
}

func TestWiFiStatusCarriesNoPassword(t *testing.T) {
	st := newWiFiFixture(t).status()
	if b := strings.Join([]string{st.Networks[0].SSID, st.Networks[0].ID}, ""); strings.Contains(b, "old-secret") {
		t.Error("password leaked into the status")
	}
}
