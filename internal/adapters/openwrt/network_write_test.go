package openwrt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// M3.1: staging the uplink. The property under test throughout is that this
// code refuses more than it accepts — the uplink is the only way into the
// reference router, so a value that does not parse must never reach `uci set`.

// recordingRunner answers uci calls from a fake config and records every
// command. `uci get` returns what the fake holds; everything else is recorded.
type recordingRunner struct {
	values map[string]string
	calls  [][]string
	failOn string
}

func (r *recordingRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.failOn != "" && strings.Contains(strings.Join(args, " "), r.failOn) {
		return nil, errors.New("uci: Entry not found")
	}
	switch {
	case len(args) >= 3 && args[0] == "-q" && args[1] == "get":
		v, ok := r.values[args[2]]
		if !ok {
			return nil, errors.New("uci: Entry not found")
		}
		return []byte(v + "\n"), nil
	case args[0] == "changes":
		var b strings.Builder
		for k, v := range r.values {
			b.WriteString(k + "='" + v + "'\n")
		}
		return []byte(b.String()), nil
	}
	return nil, nil
}

// sets returns the key=value pairs the runner was asked to stage.
func (r *recordingRunner) sets() []string {
	var out []string
	for _, c := range r.calls {
		if len(c) >= 3 && c[1] == "set" {
			out = append(out, c[2])
		}
	}
	return out
}

func writerWith(values map[string]string) (networkManager, *recordingRunner) {
	r := &recordingRunner{values: values}
	return networkManager{run: r.run}, r
}

func TestStageStaticUplinkWritesEveryFieldOnce(t *testing.T) {
	m, r := writerWith(map[string]string{"network.wan.proto": "dhcp"})

	changes, err := m.StageWAN(core.WANConfig{
		Interface: "wan",
		Proto:     core.WANProtoStatic,
		Address:   "198.51.100.9",
		Netmask:   "255.255.255.0",
		Gateway:   "198.51.100.1",
		DNS:       []string{"198.51.100.1"},
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	want := []string{
		"network.wan.proto=static",
		"network.wan.ipaddr=198.51.100.9",
		"network.wan.netmask=255.255.255.0",
		"network.wan.gateway=198.51.100.1",
		"network.wan.dns=198.51.100.1",
	}
	got := r.sets()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("staged:\n got %v\nwant %v", got, want)
	}

	// Nothing may be committed here: that is the apply transaction's job, and
	// mixing the two verbs is exactly what the M1 gate exists to prevent.
	for _, c := range r.calls {
		if len(c) >= 2 && c[1] == "commit" {
			t.Fatal("staging committed the change: the watchdog would never run")
		}
	}

	// The diff carries the old value, so the operator sees what they are
	// replacing and not just what they typed.
	if len(changes) != 5 {
		t.Fatalf("changes = %d, want 5: %+v", len(changes), changes)
	}
	if changes[0].From != "dhcp" || changes[0].To != "static" {
		t.Errorf("first change = %+v, want dhcp → static", changes[0])
	}
	for _, c := range changes {
		if !c.Dangerous {
			t.Errorf("%q is not marked dangerous; every uplink edit can cut our own access", c.Label)
		}
		if strings.Contains(c.Label, "network.") {
			t.Errorf("label %q speaks uci, not the panel's language (D-3)", c.Label)
		}
	}
}

// A value that is already what was asked for is not an edit. Staging it would
// put a no-op in the diff and make the operator confirm a change to nothing.
func TestStagingSkipsValuesThatAlreadyMatch(t *testing.T) {
	m, r := writerWith(map[string]string{
		"network.wan.proto":   "static",
		"network.wan.ipaddr":  "198.51.100.9",
		"network.wan.netmask": "255.255.255.0",
	})

	changes, err := m.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoStatic,
		Address: "198.51.100.9", Netmask: "255.255.255.0",
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none: nothing actually differs", changes)
	}
	if got := r.sets(); len(got) != 0 {
		t.Errorf("wrote %v to a device that already had those values", got)
	}
}

// Bad input must be refused before the device is touched. A half-staged
// uplink is worse than a rejected one: it is a draft nobody reviewed.
func TestBadValuesAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	cases := map[string]core.WANConfig{
		"address that is not an address": {
			Interface: "wan", Proto: core.WANProtoStatic,
			Address: "198.51.100.999", Netmask: "255.255.255.0",
		},
		"IPv6 where IPv4 is required": {
			Interface: "wan", Proto: core.WANProtoStatic,
			Address: "2001:db8::1", Netmask: "255.255.255.0",
		},
		// 255.255.0.1 parses as an address and is not a mask. netifd would
		// take it and build an unreachable network out of it.
		"mask with a hole in it": {
			Interface: "wan", Proto: core.WANProtoStatic,
			Address: "198.51.100.9", Netmask: "255.255.0.1",
		},
		"gateway that is not an address": {
			Interface: "wan", Proto: core.WANProtoStatic,
			Address: "198.51.100.9", Netmask: "255.255.255.0", Gateway: "not-a-gateway",
		},
		"resolver that is not an address": {
			Interface: "wan", Proto: core.WANProtoDHCP, DNS: []string{"nope"},
		},
		"pppoe without a login": {
			Interface: "wan", Proto: core.WANProtoPPPoE,
		},
		"connection type we do not know": {
			Interface: "wan", Proto: "carrier-pigeon",
		},
		// A section name is a uci address. One with a dot in it would write a
		// different key entirely.
		"interface name that is not one": {
			Interface: "wan.evil", Proto: core.WANProtoDHCP,
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			m, r := writerWith(map[string]string{"network.wan.proto": "dhcp"})
			if _, err := m.StageWAN(cfg); err == nil {
				t.Fatalf("accepted %+v", cfg)
			}
			if got := r.sets(); len(got) != 0 {
				t.Errorf("wrote %v to the device before rejecting the request", got)
			}
		})
	}
}

// DHCP is the one configuration with nothing to validate, so it must still
// work — a refusal-heavy validator that also refuses the simple case is not
// safe, it is broken.
func TestStagingPlainDHCPWorks(t *testing.T) {
	m, r := writerWith(map[string]string{"network.wan.proto": "static"})
	changes, err := m.StageWAN(core.WANConfig{Interface: "wan", Proto: core.WANProtoDHCP})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if len(changes) != 1 || changes[0].To != "dhcp" {
		t.Errorf("changes = %+v, want one switch to dhcp", changes)
	}
	if got := r.sets(); len(got) != 1 || got[0] != "network.wan.proto=dhcp" {
		t.Errorf("staged %v", got)
	}
}

// The PPPoE password must not travel back to the panel, not even in the diff
// the operator is about to read on a shared screen.
func TestPasswordIsNeverShownInTheDiff(t *testing.T) {
	m, _ := writerWith(map[string]string{})
	changes, err := m.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoPPPoE,
		Username: "subscriber", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	for _, c := range changes {
		if strings.Contains(c.To, "hunter2") || strings.Contains(c.From, "hunter2") {
			t.Fatalf("the password reached the diff: %+v", c)
		}
	}
	// And it still has to be staged, or the connection would not authenticate.
	var staged bool
	for _, c := range changes {
		if strings.HasSuffix(c.Detail, ".password") && c.To == "••••" {
			staged = true
		}
	}
	if !staged {
		t.Error("the password was not staged at all")
	}
}

// A failure in the middle of a batch must not leave a half-written draft: the
// operator would otherwise confirm an edit they never saw in full.
func TestAFailedStageDropsTheHalfWrittenDraft(t *testing.T) {
	r := &recordingRunner{values: map[string]string{}, failOn: "netmask"}
	m := networkManager{run: r.run}

	if _, err := m.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoStatic,
		Address: "198.51.100.9", Netmask: "255.255.255.0",
	}); err == nil {
		t.Fatal("expected the staging failure to surface")
	}
	var reverted bool
	for _, c := range r.calls {
		if len(c) >= 3 && c[1] == "revert" && c[2] == "network" {
			reverted = true
		}
	}
	if !reverted {
		t.Error("a half-written draft was left on the device")
	}
}

// Without a runner (a dev machine, a future platform) the writer says so
// rather than pretending the edit was staged.
func TestWriterWithoutARunner(t *testing.T) {
	m := networkManager{}
	if _, err := m.StageWAN(core.WANConfig{Proto: core.WANProtoDHCP}); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("StageWAN err = %v, want ErrNotImplemented", err)
	}
	if _, err := m.StagedChanges(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("StagedChanges err = %v, want ErrNotImplemented", err)
	}
	if err := m.DiscardStaged(); !errors.Is(err, core.ErrNotImplemented) {
		t.Errorf("DiscardStaged err = %v, want ErrNotImplemented", err)
	}
}

// Staged changes are read from the device, not from this process's memory:
// a draft outlives the daemon, and after a restart the panel must still be
// able to show what is pending.
func TestStagedChangesComeFromTheDevice(t *testing.T) {
	m, _ := writerWith(map[string]string{
		"network.wan.proto":          "static",
		"firewall.@zone[0].input":    "ACCEPT",
		"system.@system[0].hostname": "router",
	})
	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3: %+v", len(changes), changes)
	}
	byKey := map[string]core.ConfigChange{}
	for _, c := range changes {
		byKey[c.Detail] = c
	}
	// Network and firewall edits can lock the operator out; a hostname cannot.
	if !byKey["network.wan.proto"].Dangerous {
		t.Error("a network edit is not marked dangerous")
	}
	if !byKey["firewall.@zone[0].input"].Dangerous {
		t.Error("a firewall edit is not marked dangerous")
	}
	if byKey["system.@system[0].hostname"].Dangerous {
		t.Error("renaming the device is not a lockout risk and must not demand confirmation")
	}
}
