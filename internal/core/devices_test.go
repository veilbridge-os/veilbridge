package core_test

import (
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

func TestMACIsSpelledOneWay(t *testing.T) {
	for in, want := range map[string]string{
		"02:00:5E:00:53:D6":  "02:00:5e:00:53:d6",
		"02-00-5e-00-53-d6":  "02:00:5e:00:53:d6",
		" 00:00:5e:00:53:10": "00:00:5e:00:53:10",
	} {
		if got, err := core.NormalizeMAC(in); err != nil || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "02:00:5e", "01:00:5e:00:00:01", "02:00:00:00:00:00:00:01", "phone"} {
		if _, err := core.NormalizeMAC(bad); err == nil {
			t.Errorf("NormalizeMAC(%q) accepted", bad)
		}
	}
}

// A phone as in #50: a burnt-in address, and a private one on the network.
func TestPrivateAddressIsTheLocallyAdministeredBit(t *testing.T) {
	if !core.IsPrivateMAC("02:00:5e:00:53:d6") {
		t.Error("the phone's private address is not recognised")
	}
	if core.IsPrivateMAC("00:00:5e:00:53:01") || core.IsPrivateMAC("00:00:5e:00:53:10") {
		t.Error("a burnt-in address is called private")
	}
}

func TestDeviceNames(t *testing.T) {
	if n, err := core.CleanDeviceName("  Телевизор в гостиной (LG) "); err != nil || n != "Телевизор в гостиной (LG)" {
		t.Errorf("name = %q, %v", n, err)
	}
	// 64 Cyrillic letters are 128 bytes and still a valid name.
	if _, err := core.CleanDeviceName(strings.Repeat("я", 64)); err != nil {
		t.Errorf("64 characters refused: %v", err)
	}
	if _, err := core.CleanDeviceName(strings.Repeat("я", 65)); err == nil {
		t.Error("65 characters accepted")
	}
	if _, err := core.CleanDeviceName("a\nb"); err == nil {
		t.Error("a line break accepted")
	}
}

func TestMergeAddsTheOwnersWordsAndKeepsNamedDevices(t *testing.T) {
	seen := []core.Device{
		{MAC: "02:00:5e:00:53:d6", Online: true, Link: core.DeviceLink{Kind: core.LinkWiFi}},
		{MAC: "00:00:5e:00:53:10", Online: true, Link: core.DeviceLink{Kind: core.LinkCable}},
		{MAC: "00:00:5e:00:53:21"},
	}
	notes := []core.DeviceNote{
		{MAC: "02:00:5e:00:53:d6", Name: "Телефон Маши", Known: true},
		{MAC: "00:00:5e:00:53:10", Known: true},
		{MAC: "02:27:eb:4c:90:1e", Name: "Старый ноутбук", Known: true}, // not here now
		{MAC: "02:00:00:00:00:99"},                                      // an empty note is nothing
	}
	got := map[string]core.Device{}
	for _, d := range core.MergeDevices(seen, notes) {
		got[d.MAC] = d
	}
	if len(got) != 4 {
		t.Fatalf("merged = %v, want the three seen and the named one that is away", got)
	}
	if d := got["02:00:5e:00:53:d6"]; d.Name != "Телефон Маши" || d.New || !d.PrivateAddress || !d.Online {
		t.Errorf("named phone = %+v", d)
	}
	if d := got["00:00:5e:00:53:10"]; d.New || d.Name != "" || d.PrivateAddress {
		t.Errorf("known TV = %+v, want not new, no name, not private", d)
	}
	if d := got["00:00:5e:00:53:21"]; !d.New || d.IPs == nil || d.Link.Kind != core.LinkUnknown {
		t.Errorf("unknown device = %+v, want new, ips [] not null, link unknown", d)
	}
	if d := got["02:27:eb:4c:90:1e"]; d.Online || d.Name != "Старый ноутбук" || d.LastSeenSec != nil || d.Link.Kind != core.LinkUnknown {
		t.Errorf("named but away = %+v, want kept, offline, nothing claimed about it", d)
	}
}

// #53. Nothing on the router holding a device back is what "allowed" means,
// and a block the adapter reported is kept through the merge.
func TestInternetIsAllowedUnlessTheRouterSaysOtherwise(t *testing.T) {
	out := core.MergeDevices([]core.Device{
		{MAC: "02:00:5e:00:53:d6"},
		{MAC: "00:00:5e:00:53:10", Internet: core.InternetBlocked},
	}, []core.DeviceNote{{MAC: "00:00:5e:00:53:21", Name: "Принтер"}})
	got := map[string]string{}
	for _, d := range out {
		got[d.MAC] = d.Internet
	}
	want := map[string]string{
		"02:00:5e:00:53:d6": core.InternetAllowed,
		"00:00:5e:00:53:10": core.InternetBlocked,
		"00:00:5e:00:53:21": core.InternetAllowed, // remembered, not seen
	}
	for mac, w := range want {
		if got[mac] != w {
			t.Errorf("%s: internet %q, want %q", mac, got[mac], w)
		}
	}
}

// "Here" is the device the request came from, by any of its addresses. Not
// knowing (a tunnel, the uplink side) marks nothing rather than guessing.
func TestHereIsTheDeviceTheRequestCameFrom(t *testing.T) {
	list := func() []core.Device {
		return []core.Device{
			{MAC: "02:00:5e:00:53:d6", IPs: []string{"192.0.2.137", "2001:db8:0:c::2e5"}},
			{MAC: "00:00:5e:00:53:10", IPs: []string{"192.0.2.50"}},
		}
	}
	for _, tc := range []struct {
		from string
		want string // the MAC marked, or "" for none
	}{
		{"192.0.2.50", "00:00:5e:00:53:10"},
		{"2001:db8:0:c:0:0:0:2e5", "02:00:5e:00:53:d6"}, // another spelling of the same address
		{"::ffff:192.0.2.137", "02:00:5e:00:53:d6"},     // IPv4 as a dual-stack listener reports it
		{"127.0.0.1", ""},
		{"198.51.100.7", ""},
		{"", ""},
	} {
		ds := list()
		core.MarkHere(ds, tc.from)
		got := ""
		for _, d := range ds {
			if d.Here {
				if got != "" {
					t.Errorf("%q: more than one device marked", tc.from)
				}
				got = d.MAC
			}
		}
		if got != tc.want {
			t.Errorf("from %q: marked %q, want %q", tc.from, got, tc.want)
		}
	}
}
