package openwrt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	// stagedLines, when set, is returned verbatim by `uci changes`. It exists
	// so tests can feed the exact line shapes a real device prints, including
	// the ones that are not `key=value`.
	stagedLines []string
	// committed answers `uci -c <dir> show`: the values on disk, without the
	// draft. Keys are given as full uci keys of the real configuration.
	committed map[string]string
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
		if r.stagedLines != nil {
			return []byte(strings.Join(r.stagedLines, "\n") + "\n"), nil
		}
		var b strings.Builder
		for k, v := range r.values {
			b.WriteString(k + "='" + v + "'\n")
		}
		return []byte(b.String()), nil
	case len(args) >= 5 && args[1] == "-c" && args[3] == "show":
		// The committed read: values as they are on disk, answered under the
		// package name the caller linked the file to.
		return []byte(showLines(r.committed, args[4])), nil
	case len(args) >= 3 && args[0] == "-q" && args[1] == "show":
		// The ordinary read: values with the draft applied. `values` is the
		// device's current view, which is what a draft produces.
		return []byte(showLines(r.values, args[2])), nil
	}
	return nil, nil
}

// showLines renders a value map the way `uci show <pkg>` prints it. Three
// details are copied from a live device on purpose, because a forgiving fake
// lets mutations survive:
//
//	\u2022 a list prints as separate quoted items, `'a' 'b'`, not `'a b'`;
//	\u2022 every section prints a type line of its own, `network.wan=interface`,
//	  which carries no value and must not be mistaken for one;
//	\u2022 a package that does not exist prints nothing at all.
func showLines(values map[string]string, requested string) string {
	// The real uci answers about the package it was asked for, and knows
	// nothing about any other. `requested` is either a configuration name or
	// the alias the committed read links a file under; both name one config.
	config := strings.TrimPrefix(requested, committedPrefix)
	var b strings.Builder
	sections := map[string]bool{}
	for k := range values {
		parts := strings.Split(k, ".")
		if len(parts) >= 3 && parts[0] == config && !sections[parts[1]] {
			sections[parts[1]] = true
			b.WriteString(requested + "." + parts[1] + "=interface\n")
		}
	}
	for k, v := range values {
		pkg, rest, ok := strings.Cut(k, ".")
		if !ok || pkg != config {
			continue
		}
		quoted := make([]string, 0, 2)
		for _, item := range strings.Fields(v) {
			quoted = append(quoted, "'"+item+"'")
		}
		if len(quoted) == 0 {
			quoted = append(quoted, "''")
		}
		b.WriteString(requested + "." + rest + "=" + strings.Join(quoted, " ") + "\n")
	}
	return b.String()
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
	// The guard that was missing until M3.1a: this path used to put the raw
	// key in the label, so reloading the page printed `network.wan.proto` in
	// the apply bar (D-3). A label is what a person reads; the key belongs in
	// `detail` and nowhere else.
	for _, c := range changes {
		if strings.ContainsAny(c.Label, ".[@") {
			t.Errorf("label %q is a configuration key, not words (D-3)", c.Label)
		}
	}
}

// The operator cannot tell the two paths apart: staging an edit and reloading
// the page both end in the same apply bar. So the same key must produce the
// same words either way \u2014 two label lists would drift silently.
func TestADraftReadBackUsesTheSameWordsAsStagingIt(t *testing.T) {
	m, _ := writerWith(map[string]string{"network.wan.proto": "dhcp"})
	staged, err := m.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoStatic,
		Address: "198.51.100.9", Netmask: "255.255.255.0", Gateway: "198.51.100.1",
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	// Now the same draft as a device reports it after a restart.
	r := &recordingRunner{
		values: map[string]string{
			"network.wan.proto":   "static",
			"network.wan.ipaddr":  "198.51.100.9",
			"network.wan.netmask": "255.255.255.0",
			"network.wan.gateway": "198.51.100.1",
		},
		stagedLines: []string{
			"network.wan.proto='static'",
			"network.wan.ipaddr='198.51.100.9'",
			"network.wan.netmask='255.255.255.0'",
			"network.wan.gateway='198.51.100.1'",
		},
	}
	readBack, err := (networkManager{run: r.run}).StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}

	words := func(cs []core.ConfigChange) map[string]string {
		out := map[string]string{}
		for _, c := range cs {
			out[c.Detail] = c.Label
		}
		return out
	}
	for key, label := range words(staged) {
		if got := words(readBack)[key]; got != label {
			t.Errorf("%s: staged says %q, read back says %q", key, label, got)
		}
	}
}

// Line shapes captured from a live 23.05 device. A removal carries no `=` at
// all, and splitting on it used to drop the line: the operator was shown a
// draft with a setting missing and asked to confirm it.
func TestEveryShapeOfAStagedLineReachesTheDiff(t *testing.T) {
	r := &recordingRunner{
		// The device's view with the draft applied: the gateway is gone and
		// the resolver list already holds both entries.
		values: map[string]string{
			"network.wan.proto": "static",
			"network.wan.dns":   "192.0.2.1 192.0.2.9",
		},
		stagedLines: []string{
			"network.wan.proto='static'",
			"network.wan.dns='192.0.2.1'",
			"network.wan.dns+='192.0.2.9'",
			"-network.wan.gateway",
			"this is not a uci change at all",
		},
		committed: map[string]string{
			"network.wan.proto":   "dhcp",
			"network.wan.dns":     "192.0.2.1",
			"network.wan.gateway": "192.0.2.254",
		},
	}
	m := networkManager{run: r.run, configDir: configDirWith(t, "network")}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	byKey := map[string]core.ConfigChange{}
	for _, c := range changes {
		byKey[c.Detail] = c
	}
	// Three settings changed, though the draft mentions one of them twice:
	// a key is one row, or the operator reads half an answer twice. Measured
	// on a live device \u2014 a set followed by an append is what the panel's own
	// resolver field produces.
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3 (one row per key, unknown lines dropped): %+v",
			len(changes), changes)
	}
	if got := byKey["network.wan.gateway"]; got.From != "192.0.2.254" || got.To != "" {
		t.Errorf("a removed setting reads as %+v, want 192.0.2.254 \u2192 (nothing)", got)
	}
	// Appending one resolver must not look like replacing the list.
	if got := byKey["network.wan.dns"]; got.To != "192.0.2.1 192.0.2.9" || got.From != "192.0.2.1" {
		t.Errorf("resolver list reads as %q \u2192 %q, want both resolvers in the result",
			got.From, got.To)
	}
	if got := byKey["network.wan.proto"]; got.From != "dhcp" {
		t.Errorf("before value = %q, want the committed one (dhcp)", got.From)
	}
}

// The "before" column comes from the configuration on disk. uci has no flag
// that excludes the draft (measured: -t and -P both still return it), so the
// file is read under a different package name \u2014 and this test pins that the
// read goes to the configuration directory rather than to plain `uci get`,
// which would answer with the staged value and print "static \u2192 static".
func TestTheBeforeValueIsReadWithoutTheDraft(t *testing.T) {
	r := &recordingRunner{
		values:      map[string]string{"network.wan.proto": "static"}, // the draft's result
		stagedLines: []string{"network.wan.proto='static'"},
		committed:   map[string]string{"network.wan.proto": "dhcp"},
	}
	m := networkManager{run: r.run, configDir: configDirWith(t, "network")}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(changes) != 1 || changes[0].From != "dhcp" || changes[0].To != "static" {
		t.Fatalf("changes = %+v, want dhcp \u2192 static", changes)
	}
	var readFromDisk bool
	for _, c := range r.calls {
		if len(c) >= 4 && c[2] == "-c" {
			readFromDisk = true
		}
	}
	if !readFromDisk {
		t.Error("the committed value was not read from the configuration directory")
	}
}

// A draft this process did not stage still must not show a password, and the
// rule cannot depend on the key ending in `.password` alone: the same applies
// to a key.
func TestSecretsInADraftReadBackAreRedacted(t *testing.T) {
	r := &recordingRunner{
		values: map[string]string{
			"network.wan.password": "hunter2",
			"network.awg0.key":     "deadbeef",
		},
		stagedLines: []string{
			"network.wan.password='hunter2'",
			"network.awg0.key='deadbeef'",
		},
		committed: map[string]string{"network.wan.password": "olds3cret"},
	}
	m := networkManager{run: r.run, configDir: configDirWith(t, "network")}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	for _, c := range changes {
		for _, secret := range []string{"hunter2", "deadbeef", "olds3cret"} {
			if strings.Contains(c.From+"|"+c.To, secret) {
				t.Errorf("%s leaked into the diff: %+v", secret, c)
			}
		}
	}
}

// Losing the "before" column must not lose the pending change: it is live on
// the device whether or not this code can describe it fully.
func TestADraftSurvivesAnUnreadableConfigurationDirectory(t *testing.T) {
	r := &recordingRunner{
		values:      map[string]string{"network.wan.proto": "static"},
		stagedLines: []string{"network.wan.proto='static'"},
	}
	m := networkManager{run: r.run, configDir: "/nonexistent/veilbridge-test"}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(changes) != 1 || changes[0].To != "static" {
		t.Fatalf("changes = %+v, want the pending edit to survive", changes)
	}
	if changes[0].Label == "" || strings.ContainsAny(changes[0].Label, ".[@") {
		t.Errorf("label = %q, want words even without a before value", changes[0].Label)
	}
}

// A draft can hold an edit that changes nothing: staging a value, then staging
// the old one back. uci still reports the key as pending, and showing it would
// ask somebody to confirm "dhcp \u2192 dhcp" on a screen whose whole point is that
// confirming is dangerous.
func TestAnEditThatChangesNothingIsNotShown(t *testing.T) {
	r := &recordingRunner{
		values:      map[string]string{"network.wan.proto": "dhcp", "network.wan.metric": "10"},
		stagedLines: []string{"network.wan.proto='dhcp'", "network.wan.metric='10'"},
		committed:   map[string]string{"network.wan.proto": "dhcp", "network.wan.metric": "20"},
	}
	m := networkManager{run: r.run, configDir: configDirWith(t, "network")}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	if len(changes) != 1 || changes[0].Detail != "network.wan.metric" {
		t.Fatalf("changes = %+v, want only the key that actually differs", changes)
	}
}

// A draft can add or remove a whole section, which in `network` means a whole
// connection. `uci changes` reports it as a line with no option at all, and
// dropping those lines made the biggest possible change \u2014 "this connection
// will be deleted" \u2014 the one thing the apply bar did not mention.
func TestAddingOrRemovingAWholeConnectionIsVisible(t *testing.T) {
	r := &recordingRunner{
		// After the draft: `guest` exists, `oldwan` is gone.
		values: map[string]string{
			"network.guest.proto": "static",
		},
		stagedLines: []string{
			"+network.guest='interface'",
			"network.guest.proto='static'",
			"-network.oldwan",
		},
		committed: map[string]string{
			"network.oldwan.proto": "dhcp",
		},
	}
	m := networkManager{run: r.run, configDir: configDirWith(t, "network")}

	changes, err := m.StagedChanges()
	if err != nil {
		t.Fatalf("staged: %v", err)
	}
	byKey := map[string]core.ConfigChange{}
	for _, c := range changes {
		byKey[c.Detail] = c
	}
	added, ok := byKey["network.guest"]
	if !ok || added.From != "" || added.To == "" {
		t.Errorf("a new connection reads as %+v, want (nothing) \u2192 something", added)
	}
	removed, ok := byKey["network.oldwan"]
	if !ok || removed.From == "" || removed.To != "" {
		t.Errorf("a deleted connection reads as %+v, want something \u2192 (nothing)", removed)
	}
	for _, c := range []core.ConfigChange{added, removed} {
		if strings.ContainsAny(c.Label, ".[@") || c.Label == "" {
			t.Errorf("label %q is not words (D-3)", c.Label)
		}
		if !c.Dangerous {
			t.Errorf("%+v is not marked dangerous; adding or dropping a connection can cut access", c)
		}
	}
}

// A key we have no words for is named by its configuration file, and the key
// itself stays in the technical detail. Inventing words would be worse than
// admitting we have none.
func TestUnknownKeysAreNamedWithoutSpeakingUCI(t *testing.T) {
	cases := map[string]string{
		"network.wan.metric":         "Network setting",
		"firewall.@redirect[0].dest": "Firewall setting",
		"luci.main.lang":             "System setting",
		// A section in a configuration we have no words for at all. It still
		// gets words: a blank label is a row the operator cannot read.
		"luci.main": "Configuration section",
	}
	for key, want := range cases {
		r := &recordingRunner{
			values:      map[string]string{key: "x"},
			stagedLines: []string{key + "='x'"},
		}
		changes, err := (networkManager{run: r.run}).StagedChanges()
		if err != nil {
			t.Fatalf("staged: %v", err)
		}
		if len(changes) != 1 || changes[0].Label != want {
			t.Errorf("%s labelled %+v, want %q", key, changes, want)
		}
		if changes[0].Detail != key {
			t.Errorf("%s lost its technical detail: %+v", key, changes[0])
		}
	}
}

// configDirWith makes a configuration directory the committed read can link
// to. The file only has to exist: what it contains is answered by the fake
// runner, because parsing uci files is uci's job and not ours.
func configDirWith(t *testing.T, configs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, c := range configs {
		if err := os.WriteFile(filepath.Join(dir, c), []byte("# fixture\n"), 0o600); err != nil {
			t.Fatalf("fixture %s: %v", c, err)
		}
	}
	return dir
}
