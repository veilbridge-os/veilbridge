package openwrt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Wi-Fi (M4, #57). uci is the source of truth for what is configured (D-10);
// the bus says what the radios are doing now. All shapes here were captured on
// the reference router (25.12.5, wpad-basic-mbedtls) on 30.09.2026:
//
//   - `uci show wireless`: a `wifi-device` per radio (band 2g/5g, channel,
//     htmode like HE80, disabled, country) and a `wifi-iface` per network on
//     a radio (device, network, mode, ssid, encryption, key);
//   - `network.wireless status`: which interface (phy1-ap0) each wifi-iface
//     section runs as — the only place the two names meet;
//   - `hostapd.<ifname> get_status`: ENABLED, ACS while choosing a channel,
//     DFS while listening for radar, and the channel it is on;
//   - `iwinfo info` / `iwinfo freqlist` by phy name: the widths the radio can
//     do and the channels its country allows. freqlist's `restricted` drops
//     no-IR channels but NOT radar ones (channel 52 reads unrestricted while
//     the CLI says NO_IR + RADAR_DETECTION), so radar is known by frequency.

var (
	countryRe = regexp.MustCompile(`^[A-Z]{2}$`)
	digitsRe  = regexp.MustCompile(`[0-9]+$`)
)

type wifiManager struct {
	net networkManager
	bus *ubus.Client
}

func newWiFiManager(n networkManager, bus *ubus.Client) *wifiManager {
	return &wifiManager{net: n, bus: bus}
}

// wifiConfig is `uci show wireless`, taken apart.
type wifiConfig struct {
	radios []uciSection
	ifaces []uciSection // access points only
}

func (m *wifiManager) readConfig(ctx context.Context) (wifiConfig, error) {
	if m.net.run == nil {
		return wifiConfig{}, core.ErrNotImplemented
	}
	out, err := m.net.run(ctx, "uci", "-q", "show", "wireless")
	if err != nil {
		// No wireless file at all: a device without radios.
		return wifiConfig{}, core.ErrNoWiFi
	}
	var c wifiConfig
	for _, s := range parseUCISections(string(out)) {
		switch s.kind {
		case "wifi-device":
			c.radios = append(c.radios, s)
		case "wifi-iface":
			if s.options["mode"] == "ap" {
				c.ifaces = append(c.ifaces, s)
			}
		}
	}
	if len(c.radios) == 0 {
		return wifiConfig{}, core.ErrNoWiFi
	}
	return c, nil
}

func (c wifiConfig) radio(id string) (uciSection, bool) {
	for _, r := range c.radios {
		if r.id == id {
			return r, true
		}
	}
	return uciSection{}, false
}

// bandWord turns uci's band into the words the device list uses.
func bandWord(b string) string {
	switch b {
	case "2g":
		return "2.4"
	case "5g":
		return "5"
	case "6g":
		return "6"
	case "60g":
		return "60"
	}
	return b
}

// widthOf reads the MHz out of an htmode: HE80 → 80.
func widthOf(htmode string) int {
	n, _ := strconv.Atoi(digitsRe.FindString(htmode))
	return n
}

// radioFacts is what the device can do with one radio right now.
type radioFacts struct {
	phy      string
	htmodes  []string
	widths   []int
	channels []core.WiFiChannel
	country  string
}

func (m *wifiManager) facts(ctx context.Context, id string) radioFacts {
	var f radioFacts
	var phy struct {
		Name string `json:"phyname"`
	}
	if m.bus == nil || m.bus.CallWith(ctx, "iwinfo", "phyname", map[string]string{"section": id}, &phy) != nil || phy.Name == "" {
		return f
	}
	f.phy = phy.Name
	var info struct {
		Country string   `json:"country"`
		HTModes []string `json:"htmodes"`
	}
	if m.bus.CallWith(ctx, "iwinfo", "info", map[string]string{"device": f.phy}, &info) == nil {
		f.htmodes = info.HTModes
		f.country = info.Country
		seen := map[int]bool{}
		for _, h := range info.HTModes {
			if w := widthOf(h); w > 0 && !seen[w] {
				seen[w] = true
				f.widths = append(f.widths, w)
			}
		}
		sort.Ints(f.widths)
	}
	var freq struct {
		Results []struct {
			Channel    int  `json:"channel"`
			MHz        int  `json:"mhz"`
			Restricted bool `json:"restricted"`
		} `json:"results"`
	}
	if m.bus.CallWith(ctx, "iwinfo", "freqlist", map[string]string{"device": f.phy}, &freq) == nil {
		noCountry := f.country == "" || f.country == "00"
		for _, r := range freq.Results {
			if r.Restricted {
				continue
			}
			radar := r.MHz >= 5250 && r.MHz <= 5730
			if radar && noCountry {
				// D-102: without a country the kernel has no radar rules
				// to follow, and the access point will not come up there.
				continue
			}
			f.channels = append(f.channels, core.WiFiChannel{Channel: r.Channel, Radar: radar})
		}
	}
	return f
}

// running maps a wifi-iface section to the interface it runs as.
func (m *wifiManager) running(ctx context.Context) map[string]string {
	out := map[string]string{}
	if m.bus == nil {
		return out
	}
	var st map[string]struct {
		Interfaces []struct {
			Section string `json:"section"`
			IfName  string `json:"ifname"`
		} `json:"interfaces"`
	}
	if m.bus.Call(ctx, "network.wireless", "status", &st) != nil {
		return out
	}
	for _, r := range st {
		for _, i := range r.Interfaces {
			if i.IfName != "" {
				out[i.Section] = i.IfName
			}
		}
	}
	return out
}

// apState is what hostapd says about one access point.
func (m *wifiManager) apState(ctx context.Context, ifname string) (string, int) {
	var st struct {
		Status  string `json:"status"`
		Channel int    `json:"channel"`
	}
	if m.bus == nil || m.bus.Call(ctx, "hostapd."+ifname, "get_status", &st) != nil {
		return core.RadioDown, 0
	}
	switch st.Status {
	case "ENABLED":
		return core.RadioUp, st.Channel
	case "DFS":
		return core.RadioRadar, 0
	case "DISABLED":
		return core.RadioDown, 0
	default: // ACS, HT_SCAN, UNINITIALIZED, COUNTRY_UPDATE
		return core.RadioStarting, 0
	}
}

// connected counts devices the access point let in (D-101: authorized, not
// merely associated).
func (m *wifiManager) connected(ctx context.Context, ifname string) int {
	var body json.RawMessage
	if m.bus == nil || m.bus.Call(ctx, "hostapd."+ifname, "get_clients", &body) != nil {
		return 0
	}
	ap, ok := parseClients(body)
	if !ok {
		return 0
	}
	return len(ap.clients)
}

func securityOf(encryption string) string {
	base, _, _ := strings.Cut(encryption, "+")
	switch base {
	case "psk2":
		return core.WiFiWPA2
	case "sae-mixed":
		return core.WiFiWPA2WPA3
	case "sae":
		return core.WiFiWPA3
	case "", "none":
		return core.WiFiOpen
	}
	return core.WiFiOther
}

func encryptionFor(security string) string {
	switch security {
	case core.WiFiWPA2:
		return "psk2"
	case core.WiFiWPA2WPA3:
		return "sae-mixed"
	}
	return ""
}

// onLAN reports whether a wifi-iface bridges into the local network.
func onLAN(s uciSection) bool {
	for _, n := range strings.Fields(s.options["network"]) {
		if n == lanSection {
			return true
		}
	}
	return false
}

// wifiNet is one network: the wifi-iface sections that share a name,
// password and security.
type wifiNet struct {
	sections []uciSection
}

func (n wifiNet) id() string { return n.sections[0].id }

func groupNetworks(ifaces []uciSection) []wifiNet {
	var nets []wifiNet
	for _, s := range ifaces {
		key := func(x uciSection) string {
			return x.options["ssid"] + "\x00" + x.options["key"] + "\x00" + x.options["encryption"] + "\x00" + x.options["network"]
		}
		placed := false
		for i := range nets {
			if key(nets[i].sections[0]) == key(s) {
				nets[i].sections = append(nets[i].sections, s)
				placed = true
				break
			}
		}
		if !placed {
			nets = append(nets, wifiNet{sections: []uciSection{s}})
		}
	}
	return nets
}

// Status reads the whole Wi-Fi screen.
func (m *wifiManager) Status() (core.WiFiStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), devicesTimeout)
	defer cancel()
	c, err := m.readConfig(ctx)
	if err != nil {
		return core.WiFiStatus{}, err
	}
	ifnames := m.running(ctx)
	counts := map[string]int{} // by wifi-iface section
	for sec, ifname := range ifnames {
		counts[sec] = m.connected(ctx, ifname)
	}

	st := core.WiFiStatus{Radios: []core.WiFiRadio{}, Networks: []core.WiFiNetwork{}}
	for _, r := range c.radios {
		f := m.facts(ctx, r.id)
		radio := core.WiFiRadio{
			ID: r.id, Band: bandWord(r.options["band"]),
			Enabled: r.options["disabled"] != "1",
			Width:   widthOf(r.options["htmode"]),
			Widths:  f.widths, Channels: f.channels,
			State: core.RadioDown,
		}
		if radio.Widths == nil {
			radio.Widths = []int{}
		}
		if radio.Channels == nil {
			radio.Channels = []core.WiFiChannel{}
		}
		if ch := r.options["channel"]; ch == "" || ch == "auto" {
			radio.Auto = true
		} else {
			radio.Channel, _ = strconv.Atoi(ch)
		}
		if cc := r.options["country"]; cc != "" && cc != "00" && st.Country == "" {
			st.Country = cc
		}
		// The radio's state is its first running access point's state.
		for _, i := range c.ifaces {
			if i.options["device"] != r.id {
				continue
			}
			radio.Devices += counts[i.id]
			if ifname, ok := ifnames[i.id]; ok && radio.Enabled && radio.ChannelNow == 0 && radio.State != core.RadioUp {
				radio.State, radio.ChannelNow = m.apState(ctx, ifname)
			}
		}
		if !radio.Enabled {
			radio.State = core.RadioDown
		}
		st.Radios = append(st.Radios, radio)
	}
	for _, n := range groupNetworks(c.ifaces) {
		first := n.sections[0]
		net := core.WiFiNetwork{
			ID: n.id(), Kind: core.WiFiKindOther, SSID: first.options["ssid"],
			Security:    securityOf(first.options["encryption"]),
			HasPassword: first.options["key"] != "",
			Radios:      []string{},
		}
		if onLAN(first) {
			net.Kind = core.WiFiKindMain
		}
		for _, s := range n.sections {
			net.Radios = append(net.Radios, s.options["device"])
			net.Devices += counts[s.id]
		}
		st.Networks = append(st.Networks, net)
	}
	return st, nil
}

func (m *wifiManager) network(c wifiConfig, id string) (wifiNet, bool) {
	for _, n := range groupNetworks(c.ifaces) {
		for _, s := range n.sections {
			if s.id == id {
				return n, true
			}
		}
	}
	return wifiNet{}, false
}

// WiFiPassword returns a network's password (D-103).
func (m *wifiManager) WiFiPassword(id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	c, err := m.readConfig(ctx)
	if err != nil {
		return "", err
	}
	n, ok := m.network(c, id)
	if !ok {
		return "", fmt.Errorf("openwrt: no Wi-Fi network %q on this device", id)
	}
	return n.sections[0].options["key"], nil
}

// radioSubject names a radio in a diff row: "Wi-Fi 5 GHz".
func radioSubject(r uciSection) string {
	return "Wi-Fi " + bandWord(r.options["band"]) + " GHz"
}

// htmodeFor keeps the radio's standard (HE, VHT…) and changes the width. A
// radio with no htmode yet gets the newest standard it offers at that width.
func htmodeFor(current string, htmodes []string, width int) (string, bool) {
	has := func(h string) bool {
		for _, x := range htmodes {
			if x == h {
				return true
			}
		}
		return false
	}
	if prefix := strings.TrimRight(current, "0123456789"); prefix != "" && has(prefix+strconv.Itoa(width)) {
		return prefix + strconv.Itoa(width), true
	}
	for _, p := range []string{"EHT", "HE", "VHT", "HT"} {
		if has(p + strconv.Itoa(width)) {
			return p + strconv.Itoa(width), true
		}
	}
	return "", false
}

// StageRadio stages a radio edit: on/off, channel, width. It goes through the
// apply transaction (D-100): a bad channel is cured by putting the old back.
func (m *wifiManager) StageRadio(id string, cfg core.RadioConfig) ([]core.ConfigChange, error) {
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	c, err := m.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	r, ok := c.radio(id)
	if !ok {
		return nil, fmt.Errorf("openwrt: no radio %q on this device", id)
	}
	f := m.facts(ctx, id)

	sets := []wanSetting{}
	if cfg.Enabled {
		sets = append(sets, wanSetting{key: "disabled", remove: true, sameAsAbsent: "0",
			label: describe("wireless", roleRadio, "disabled")})
	} else {
		sets = append(sets, wanSetting{key: "disabled", value: "1",
			label: describe("wireless", roleRadio, "disabled")})
	}
	if ch := strings.TrimSpace(cfg.Channel); ch != "" {
		if ch != "auto" {
			n, err := strconv.Atoi(ch)
			allowed := false
			for _, a := range f.channels {
				if a.Channel == n {
					allowed = true
				}
			}
			if err != nil || !allowed {
				return nil, core.Refuse("channel", fmt.Errorf(
					"openwrt: channel %q is not allowed for this radio in its country", ch))
			}
			ch = strconv.Itoa(n)
		}
		sets = append(sets, wanSetting{key: "channel", value: ch,
			label: describe("wireless", roleRadio, "channel")})
	}
	if cfg.Width > 0 {
		hm, ok := htmodeFor(r.options["htmode"], f.htmodes, cfg.Width)
		if !ok {
			return nil, core.Refuse("width", fmt.Errorf("openwrt: this radio cannot use %d MHz", cfg.Width))
		}
		sets = append(sets, wanSetting{key: "htmode", value: hm,
			label: describe("wireless", roleRadio, "htmode"),
			// Width in MHz, not "HE80": the standard's name is the
			// operating system talking (D-3).
			shown: strconv.Itoa(cfg.Width), shownBefore: widthWords(r.options["htmode"])})
	}
	changes, err := m.net.stage(ctx, "wireless", id, roleRadio, sets)
	return withSubject(changes, radioSubject(r)), err
}

func widthWords(htmode string) string {
	if w := widthOf(htmode); w > 0 {
		return strconv.Itoa(w)
	}
	return ""
}

// StageCountry sets the country on every radio (D-102). The country decides
// which channels a radio may use, so it is a radio edit, with the timer.
func (m *wifiManager) StageCountry(code string) ([]core.ConfigChange, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !countryRe.MatchString(code) || code == "00" {
		return nil, core.Refuse("country", fmt.Errorf("openwrt: %q is not a country code", code))
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	c, err := m.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	var out []core.ConfigChange
	for i, r := range c.radios {
		changes, err := m.net.stage(ctx, "wireless", r.id, roleRadio, []wanSetting{{
			key: "country", value: code, label: describe("wireless", roleRadio, "country"),
		}})
		if err != nil {
			return nil, err
		}
		// One row for the country, not one per radio: it is one choice.
		if i == 0 {
			out = append(out, changes...)
		}
	}
	return out, nil
}

// StageAccess stages a network's name, password and security on every radio
// it is on. The API applies it at once, without a watchdog (D-100).
func (m *wifiManager) StageAccess(id string, cfg core.AccessConfig) ([]core.ConfigChange, error) {
	if err := core.ValidSSID(cfg.SSID); err != nil {
		return nil, err
	}
	if cfg.Password != "" {
		if err := core.ValidWiFiPassword(cfg.Password); err != nil {
			return nil, err
		}
	}
	enc := ""
	if cfg.Security != "" {
		if enc = encryptionFor(cfg.Security); enc == "" {
			return nil, core.Refuse("security", fmt.Errorf("openwrt: %q is not offered (D-104)", cfg.Security))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()
	c, err := m.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	n, ok := m.network(c, id)
	if !ok {
		return nil, fmt.Errorf("openwrt: no Wi-Fi network %q on this device", id)
	}
	first := n.sections[0]
	if first.options["key"] == "" && cfg.Password == "" && enc != "" {
		return nil, core.Refuse("password", errors.New("openwrt: a protected network needs a password"))
	}

	type field struct{ key, value string }
	var fields []field
	if cfg.SSID != first.options["ssid"] {
		fields = append(fields, field{"ssid", cfg.SSID})
	}
	if cfg.Password != "" && cfg.Password != first.options["key"] {
		fields = append(fields, field{"key", cfg.Password})
	}
	if enc != "" && enc != first.options["encryption"] {
		fields = append(fields, field{"encryption", enc})
	}
	var out []core.ConfigChange
	for _, f := range fields {
		for _, s := range n.sections {
			if err := m.net.uciSet(ctx, "wireless."+s.id+"."+f.key, f.value); err != nil {
				_ = m.net.discardConfig(ctx, "wireless")
				return nil, err
			}
		}
		said := describe("wireless", roleWiFiNet, f.key)
		from, to := first.options[f.key], f.value
		if f.key == "encryption" {
			from, to = securityOf(from), securityOf(to)
		}
		out = append(out, core.ConfigChange{
			Label: said.words, LabelKey: said.key,
			From: redact(secretOption(f.key), from), To: redact(secretOption(f.key), to),
			Detail: "wireless." + first.id + "." + f.key, Subject: first.options["ssid"],
		})
	}
	return out, nil
}

var _ core.WiFiManager = (*wifiManager)(nil)
