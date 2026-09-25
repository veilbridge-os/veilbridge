package openwrt

import (
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// M3.2, the write half. The property under test is the one D-44 states: the
// panel speaks addresses, the device stores offsets, and nobody is asked to
// do that arithmetic in their head — least of all in the apply bar, where the
// next click changes the network they are standing on.

// lanWriter builds a manager whose device already looks like the reference
// router: 192.168.1.1/24 with a pool of 100…249.
func lanWriter(t *testing.T, extra map[string]string) (networkManager, *recordingRunner) {
	t.Helper()
	values := map[string]string{
		"dhcp.lan.start":     "100",
		"dhcp.lan.limit":     "150",
		"dhcp.lan.leasetime": "12h",
	}
	for k, v := range extra {
		values[k] = v
	}
	r := &recordingRunner{values: values}
	m := networkManager{
		run:       r.run,
		bus:       nil,
		configDir: t.TempDir(),
		lookupInterfaces: func() ([]core.NetworkInterface, error) {
			return []core.NetworkInterface{
				{Name: "lan", Device: "br-lan", Up: true, IPv4: []string{"192.168.1.1/24"}},
			}, nil
		},
	}
	return m, r
}

func TestPoolIsStagedAsOffsetsAndShownAsAddresses(t *testing.T) {
	m, r := lanWriter(t, nil)

	changes, err := m.StageHandout(core.HandoutConfig{
		Enabled: true, First: "192.168.1.120", Last: "192.168.1.200", LeaseSeconds: 7200,
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	// The device gets numbers…
	staged := strings.Join(r.sets(), "|")
	for _, want := range []string{"dhcp.lan.start=120", "dhcp.lan.limit=81", "dhcp.lan.leasetime=2h"} {
		if !strings.Contains(staged, want) {
			t.Errorf("staged %q, want it to contain %q", staged, want)
		}
	}
	// …and the person gets addresses.
	byKey := map[string]core.ConfigChange{}
	for _, c := range changes {
		byKey[c.Detail] = c
	}
	if got := byKey["dhcp.lan.start"]; got.From != "192.168.1.100" || got.To != "192.168.1.120" {
		t.Errorf("first address reads as %+v, want 192.168.1.100 → 192.168.1.120", got)
	}
	if got := byKey["dhcp.lan.limit"]; got.From != "192.168.1.249" || got.To != "192.168.1.200" {
		t.Errorf("last address reads as %+v, want 192.168.1.249 → 192.168.1.200", got)
	}
	for _, c := range changes {
		if strings.ContainsAny(c.Label, ".[@") {
			t.Errorf("label %q is a configuration key, not words (D-3)", c.Label)
		}
		if c.To == "120" || c.To == "81" {
			t.Errorf("an offset reached the diff: %+v", c)
		}
	}
	for _, call := range r.calls {
		if len(call) >= 2 && call[1] == "commit" {
			t.Fatal("staging committed the change: the watchdog would never run")
		}
	}
}

func TestAPoolThatCannotWorkIsRefusedBeforeTheDeviceSeesIt(t *testing.T) {
	cases := map[string]core.HandoutConfig{
		"outside the local network": {Enabled: true, First: "10.0.0.10", Last: "10.0.0.20"},
		"ends the wrong way round":  {Enabled: true, First: "192.168.1.200", Last: "192.168.1.100"},
		"starts at the network address itself": {
			Enabled: true, First: "192.168.1.0", Last: "192.168.1.100",
		},
		"not an address at all": {Enabled: true, First: "192.168.1.ten", Last: "192.168.1.100"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m, r := lanWriter(t, nil)
			if _, err := m.StageHandout(cfg); err == nil {
				t.Fatalf("accepted %+v", cfg)
			}
			if got := r.sets(); len(got) != 0 {
				t.Errorf("wrote %v to the device before refusing", got)
			}
		})
	}
}

// Turning the handout off must not forget the pool: turning it back on should
// not mean typing the range again.
func TestTurningTheHandoutOffKeepsThePool(t *testing.T) {
	m, r := lanWriter(t, nil)

	changes, err := m.StageHandout(core.HandoutConfig{Enabled: false})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(changes) != 1 || changes[0].Detail != "dhcp.lan.ignore" {
		t.Fatalf("changes = %+v, want only the switch", changes)
	}
	for _, call := range r.calls {
		if len(call) >= 3 && call[1] == "delete" && strings.HasPrefix(call[2], "dhcp.lan.start") {
			t.Error("the pool was forgotten when the handout was switched off")
		}
	}
}

// Renumbering the local network must not leave the handout stranded outside
// the new network: that is a router that hands out addresses nobody can use,
// which looks like "connected but silent" and is the hardest fault to find.
func TestRenumberingRefusesToStrandThePool(t *testing.T) {
	m, r := lanWriter(t, nil)

	// Moving the whole network is safe on its own: the pool is stored as
	// offsets, so it moves with it. What strands the pool is a mask that no
	// longer has room for it — /28 holds 14 addresses and the pool wants 150.
	_, err := m.StageLAN(core.LANConfig{Address: "192.168.1.1", Netmask: "255.255.255.240"})
	if err == nil {
		t.Fatal("accepted a mask that leaves the pool outside its own network")
	}
	if got := r.sets(); len(got) != 0 {
		t.Errorf("wrote %v before refusing", got)
	}

	// Renumbering to another network is fine: the pool follows it.
	m2, _ := lanWriter(t, nil)
	changes, err := m2.StageLAN(core.LANConfig{Address: "10.10.0.1", Netmask: "255.255.255.0"})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("nothing staged for a legitimate renumbering")
	}
	for _, c := range changes {
		if !c.Dangerous {
			t.Errorf("%+v is not marked dangerous; renumbering cuts everyone on this network", c)
		}
	}
}

// The words follow the role of the section, not the name of the option. This
// is the M3.1a limit being paid off: `ipaddr` means two different things.
func TestTheSameKeyIsNamedByItsRole(t *testing.T) {
	m, _ := lanWriter(t, map[string]string{"network.lan.proto": "static"})
	lan, err := m.StageLAN(core.LANConfig{Address: "192.168.1.2", Netmask: "255.255.255.0"})
	if err != nil {
		t.Fatalf("stage lan: %v", err)
	}
	var lanLabel string
	for _, c := range lan {
		if c.Detail == "network.lan.ipaddr" {
			lanLabel = c.Label
		}
	}
	uplinkLabel := labelFor("network", roleUplink, "ipaddr")
	if lanLabel == "" {
		t.Fatal("the local address was not staged")
	}
	if lanLabel == uplinkLabel {
		t.Errorf("the local address is called %q, the same as the uplink's", lanLabel)
	}
	if strings.Contains(strings.ToLower(lanLabel), "internet") {
		t.Errorf("the local address is called %q, which is about the wrong side", lanLabel)
	}
}

func TestReservationsAreCheckedBeforeTheyReachTheDevice(t *testing.T) {
	cases := map[string]core.ReservationConfig{
		"not a hardware address": {MAC: "не-mac", IP: "192.168.1.50"},
		"not an address":         {MAC: "1a:a6:05:03:d4:9c", IP: "192.168.1.999"},
		"outside the local network": {
			MAC: "1a:a6:05:03:d4:9c", IP: "10.0.0.50",
		},
		"a name that would break DNS": {
			MAC: "1a:a6:05:03:d4:9c", IP: "192.168.1.50", Name: "не имя хоста",
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m, r := lanWriter(t, nil)
			if _, err := m.StageReservation(cfg); err == nil {
				t.Fatalf("accepted %+v", cfg)
			}
			for _, call := range r.calls {
				if len(call) >= 2 && (call[1] == "set" || call[1] == "add") {
					t.Errorf("touched the device before refusing: %v", call)
				}
			}
		})
	}
}

// Removing a reservation addresses a section, and sections are reused after a
// commit. Removing one that is not there must refuse rather than delete
// whatever now carries that id.
func TestRemovingAReservationThatIsNotThereIsRefused(t *testing.T) {
	m, r := lanWriter(t, nil)
	if _, err := m.RemoveReservation("cfgdeadbe"); err == nil {
		t.Fatal("accepted the removal of an entry that does not exist")
	}
	for _, call := range r.calls {
		if len(call) >= 2 && call[1] == "delete" {
			t.Errorf("deleted something on a device that had nothing to delete: %v", call)
		}
	}
}

// A reservation the panel created has no name of its own, so the device calls
// it by position — `@host[0]` — and that is the id the panel is handed. It has
// to be an id the panel can hand back.
//
// Measured on the reference router before this test existed: the panel staged
// a reservation, listed it with `"id": "@host[0]"`, and then refused its own
// id with 400 "is not an entry on this device". The reservation could be made
// from the panel and never removed from it.
func TestAReservationWithoutANameCanStillBeRemoved(t *testing.T) {
	m, r := lanWriter(t, map[string]string{
		"dhcp.cfg05fe63":     "host",
		"dhcp.cfg05fe63.mac": "1a:a6:05:03:d4:9c",
		"dhcp.cfg05fe63.ip":  "192.168.1.222",
	})
	// `uci show` prints the position, never the internal name — as the device
	// does.
	r.sectionType = "host"
	r.anonymous = map[string]string{"cfg05fe63": "@host[0]"}

	entries := m.reserved(t.Context())
	if len(entries) != 1 {
		t.Fatalf("read %d reservations, want 1: %+v", len(entries), entries)
	}
	id := entries[0].ID
	if id != "@host[0]" {
		t.Fatalf("the device names the entry %q; the test's device is not the real one", id)
	}

	changes, err := m.RemoveReservation(id)
	if err != nil {
		t.Fatalf("the panel refused the id the panel itself reported: %v", err)
	}
	if len(changes) != 1 || changes[0].To != "" {
		t.Fatalf("removal reads as %+v, want one row ending in nothing", changes)
	}
	var deleted string
	for _, call := range r.calls {
		if len(call) >= 3 && call[1] == "delete" {
			deleted = call[2]
		}
	}
	if deleted != "dhcp.@host[0]" {
		t.Errorf("staged %q, want the device's own name for the entry", deleted)
	}
	for _, call := range r.calls {
		if len(call) >= 2 && call[1] == "commit" {
			t.Fatal("removal committed instead of staging it")
		}
	}
}

// An id is still checked before it is pasted into a command: the positional
// form is accepted, but only in the exact shape uci prints.
//
// The check is asserted by ITS OWN refusal, not merely by "an error came
// back". There are two guards here — the shape, and the lookup in the list
// the device reported — and the second one refuses almost everything on a
// device with no reservations. A test that only asked for "an error" passed
// with the shape check widened to `^@.*$`, which is a shell injection away
// from being interesting.
func TestOnlyTheDevicesOwnShapesAreAcceptedAsAnEntryId(t *testing.T) {
	for _, id := range []string{
		"@host[0]; reboot", "@host[0]'", "@ho st[0]", "@host[]", "@host[0", "../dhcp",
		"@host[99999999]", "@host[0] ", "@host[-1]", "@host[0].mac",
	} {
		t.Run(id, func(t *testing.T) {
			// A device that HAS a reservation, so the lookup is not what is
			// doing the refusing.
			m, r := lanWriter(t, map[string]string{
				"dhcp.cfg05fe63":     "host",
				"dhcp.cfg05fe63.mac": "1a:a6:05:03:d4:9c",
				"dhcp.cfg05fe63.ip":  "192.168.1.222",
			})
			r.sectionType = "host"
			r.anonymous = map[string]string{"cfg05fe63": "@host[0]"}

			_, err := m.RemoveReservation(id)
			if err == nil {
				t.Fatalf("accepted %q as an entry id", id)
			}
			if !strings.Contains(err.Error(), "is not an entry on this device") {
				t.Errorf("%q was refused by the wrong guard: %v", id, err)
			}
			for _, call := range r.calls {
				if len(call) >= 2 && call[1] == "delete" {
					t.Errorf("deleted something for %q: %v", id, call)
				}
			}
		})
	}
}

// The two paths that can answer "what will change" must answer the same way:
// the one that stages the edit, and the one that reads the draft back off the
// device after a reload. The operator cannot tell them apart — F5 swaps one
// for the other — so a difference here is a list that changes under somebody
// halfway through reading it (M3.1a).
func TestBothPathsDescribeANewReservationTheSameWay(t *testing.T) {
	m, r := lanWriter(t, nil)
	r.sectionType = "host"

	staged, err := m.StageReservation(core.ReservationConfig{
		MAC: "1a:a6:05:03:d4:9c", IP: "192.168.1.222",
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	// The same draft as the device reports it afterwards: uci calls the new
	// section by its internal name in `changes` and by its position in
	// `show`, and the entry now exists in the device's view.
	readBack, err := (networkManager{
		run: (&recordingRunner{
			values: map[string]string{
				"dhcp.cfg05fe63":     "host",
				"dhcp.cfg05fe63.mac": "1a:a6:05:03:d4:9c",
				"dhcp.cfg05fe63.ip":  "192.168.1.222",
			},
			committed:   map[string]string{},
			sectionType: "host",
			anonymous:   map[string]string{"cfg05fe63": "@host[0]"},
			stagedLines: []string{
				"dhcp.cfg05fe63='host'",
				"dhcp.cfg05fe63.mac='1a:a6:05:03:d4:9c'",
				"dhcp.cfg05fe63.ip='192.168.1.222'",
			},
		}).run,
		configDir: t.TempDir(),
	}).StagedChanges()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if len(staged) != len(readBack) {
		t.Fatalf("staging says %d row(s) and reading back says %d:\n staged: %+v\n read:   %+v",
			len(staged), len(readBack), staged, readBack)
	}
	for i := range staged {
		if staged[i].Label != readBack[i].Label {
			t.Errorf("row %d: staged calls it %q, read back calls it %q",
				i, staged[i].Label, readBack[i].Label)
		}
		if staged[i].To != readBack[i].To {
			t.Errorf("row %d: staged shows %q, read back shows %q",
				i, staged[i].To, readBack[i].To)
		}
	}
}

// Pinning a device the panel already pinned, to another address. The panel
// creates reservations without a name, `uci show` then calls them by position,
// and staging by position was refused — so the address kept for a device
// could never be changed from the panel. Measured on the stand (400).
func TestADevicePinnedByThePanelCanBePinnedToAnotherAddress(t *testing.T) {
	m, r := lanWriter(t, map[string]string{
		"dhcp.cfg05fe63":     "host",
		"dhcp.cfg05fe63.mac": "1a:a6:05:03:d4:9c",
		"dhcp.cfg05fe63.ip":  "192.168.1.222",
	})
	r.sectionType = "host"
	r.anonymous = map[string]string{"cfg05fe63": "@host[0]"}

	if _, err := m.StageReservation(core.ReservationConfig{MAC: "1a:a6:05:03:d4:9c", IP: "192.168.1.30"}); err != nil {
		t.Fatalf("re-pinning refused: %v", err)
	}
	if got := strings.Join(r.sets(), "|"); !strings.Contains(got, "dhcp.@host[0].ip=192.168.1.30") {
		t.Errorf("staged %q, want the existing entry's address changed", got)
	}
	for _, c := range r.calls {
		if len(c) >= 2 && c[1] == "add" {
			t.Fatal("re-pinning added a second entry for the same device")
		}
	}
}
