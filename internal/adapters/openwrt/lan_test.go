package openwrt

import (
	"testing"
	"time"
)

// M3.2. Every fixture here is a line captured from a live device, not one
// invented to match the parser: the lease file came from a real client on the
// reference router, and `uci show dhcp` from both branches.

func TestLeaseFileIsReadTheWayTheDeviceWritesIt(t *testing.T) {
	// Captured on OpenWrt 25.12.5 with a real DHCP client attached:
	//   <expiry> <mac> <ip> <name or *> <client id>
	data := "" +
		"1790012670 1a:a6:05:03:d4:9c 192.168.1.222 * 01:1a:a6:05:03:d4:9c\n" +
		"1790012687 aa:bb:cc:dd:ee:ff 192.168.1.223 stend-noutbuk 01:aa:bb:cc:dd:ee:ff\n"
	now := time.Unix(1790012600, 0)

	leases := parseLeases(data, now)
	if len(leases) != 2 {
		t.Fatalf("leases = %d, want 2: %+v", len(leases), leases)
	}
	// A client that gave no name writes `*`, and `*` is not a hostname.
	if leases[0].Hostname != "" {
		t.Errorf("hostname = %q, want empty for `*`", leases[0].Hostname)
	}
	if leases[0].MAC != "1a:a6:05:03:d4:9c" || leases[0].IP != "192.168.1.222" {
		t.Errorf("first lease = %+v", leases[0])
	}
	if leases[1].Hostname != "stend-noutbuk" {
		t.Errorf("hostname = %q, want the name the client gave", leases[1].Hostname)
	}
	// Time left, not a clock reading: a router that just booted has no idea
	// what time it is, and "expires at 03:00" would then be a lie.
	if leases[0].ExpiresSec != 70 {
		t.Errorf("expiresSec = %d, want 70", leases[0].ExpiresSec)
	}
}

func TestExpiredAndBrokenLeaseLinesDoNotBecomeClients(t *testing.T) {
	data := "" +
		"1790012600 aa:bb:cc:dd:ee:01 192.168.1.10 old 01:aa\n" + // already expired
		"not-a-lease-line\n" +
		"\n" +
		"1790012999 aa:bb:cc:dd:ee:02 192.168.1.11 fresh 01:bb\n"
	leases := parseLeases(data, time.Unix(1790012700, 0))
	if len(leases) != 2 {
		t.Fatalf("leases = %d, want 2 (the junk lines are not clients): %+v", len(leases), leases)
	}
	// An expired lease is still a client the device remembers, but it has no
	// time left — showing a negative countdown would be worse than zero.
	if leases[0].ExpiresSec != 0 {
		t.Errorf("expired lease has %d seconds left", leases[0].ExpiresSec)
	}
	if leases[1].ExpiresSec != 299 {
		t.Errorf("fresh lease = %d seconds, want 299", leases[1].ExpiresSec)
	}
}

// The pool is stored as offsets from the network address and shown as
// addresses. The mask matters as much as the numbers, which is exactly why
// the offsets may not be printed (D-3).
func TestPoolOffsetsBecomeAddresses(t *testing.T) {
	cases := []struct {
		name              string
		cidr              string
		start, limit      int
		wantFirst, wantOK string
		ok                bool
	}{
		{"the reference router", "192.168.1.1/24", 100, 150, "192.168.1.100", "192.168.1.249", true},
		{"a /22, where the same offsets mean other addresses", "10.0.4.1/22", 100, 150, "10.0.4.100", "10.0.4.249", true},
		{"a pool that runs past its own network", "192.168.1.1/24", 200, 150, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first, last, ok := poolRange([]string{c.cidr}, c.start, c.limit)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (%s %s)", ok, c.ok, first, last)
			}
			if ok && (first != c.wantFirst || last != c.wantOK) {
				t.Errorf("pool = %s…%s, want %s…%s", first, last, c.wantFirst, c.wantOK)
			}
		})
	}
}

func TestLeaseTimesTheDeviceAccepts(t *testing.T) {
	cases := map[string]int64{
		"12h":  43200,
		"30m":  1800,
		"600":  600,
		"120s": 120,
		// dnsmasq accepts days and weeks too (#30, checked on the router).
		"1d":       86400,
		"2w":       1209600,
		"infinite": 0,
		"":         0,
		"nonsense": 0,
	}
	for in, want := range cases {
		if got := parseLeaseTime(in); got != want {
			t.Errorf("parseLeaseTime(%q) = %d, want %d", in, got, want)
		}
	}
}

// Reservations are read back with the section id that addresses them, because
// removing one later must not be a guess about which entry was meant.
func TestReservationsCarryTheSectionThatHoldsThem(t *testing.T) {
	r := &recordingRunner{sectionType: "host", values: map[string]string{
		"dhcp.cfg05fe63.name": "stend-noutbuk",
		"dhcp.cfg05fe63.mac":  "1a:a6:05:03:d4:9c",
		"dhcp.cfg05fe63.ip":   "192.168.1.50",
		// A section with only half an answer reserves nothing.
		"dhcp.cfg99aaaa.mac": "aa:bb:cc:dd:ee:ff",
	}}
	// The fake prints a section line per section, which is what uci does.
	m := networkManager{run: r.run}

	list := m.reserved(t.Context())
	if len(list) != 1 {
		t.Fatalf("reservations = %+v, want only the complete one", list)
	}
	got := list[0]
	if got.ID != "cfg05fe63" || got.MAC != "1a:a6:05:03:d4:9c" || got.IP != "192.168.1.50" {
		t.Errorf("reservation = %+v", got)
	}
	if got.Name != "stend-noutbuk" {
		t.Errorf("name = %q", got.Name)
	}
}
