package openwrt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #27: every row of the diff carries a key the panel translates, and the key
// comes from the same table as the English words. The panel used to derive the
// key from `detail` by itself, knew nothing about roles, and showed a change to
// the local network address as "Address on the internet side" — measured on
// the stand, not imagined.

// assertKnownKeys fails for a change whose key is empty or not one the panel
// has been told about (LabelKeys, and through it the generated TS file).
func assertKnownKeys(t *testing.T, what string, cs []core.ConfigChange) {
	t.Helper()
	known := LabelKeys()
	if len(cs) == 0 {
		t.Fatalf("%s: nothing staged, so nothing was checked", what)
	}
	for _, c := range cs {
		if c.LabelKey == "" {
			t.Errorf("%s: %q (%s) has no label key", what, c.Label, c.Detail)
			continue
		}
		if !slices.Contains(known, c.LabelKey) {
			t.Errorf("%s: key %q is not in LabelKeys(), so the panel has no words for it", what, c.LabelKey)
		}
		if got := describeKey(c.LabelKey); got != c.Label {
			t.Errorf("%s: key %q means %q, but the row says %q", what, c.LabelKey, got, c.Label)
		}
	}
}

// describeKey inverts describe through the same tables, so a key and the words
// sent next to it can be checked against each other.
func describeKey(key string) string {
	switch key {
	case "section":
		return "Configuration section"
	case "setting":
		return "System setting"
	}
	for _, p := range entryRoles {
		if p.key == key {
			return p.words
		}
	}
	for _, p := range rulePhrases {
		if p.key == key {
			return p.words
		}
	}
	if l, ok := optionLabels[key]; ok {
		return l
	}
	config, kind, _ := strings.Cut(key, ".")
	switch kind {
	case "section":
		return sectionLabels[config]
	case "setting":
		return configLabels[config]
	}
	return ""
}

func TestEveryStagedChangeCarriesAKeyThePanelKnows(t *testing.T) {
	wan, _ := writerWith(map[string]string{"network.wan.proto": "dhcp"})
	cs, err := wan.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoStatic,
		Address: "198.51.100.9", Netmask: "255.255.255.0", Gateway: "198.51.100.1",
		DNS: []string{"198.51.100.53"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "uplink", cs)

	lan, _ := lanWriter(t, map[string]string{"network.lan.proto": "static"})
	cs, err = lan.StageLAN(core.LANConfig{Address: "192.168.1.2", Netmask: "255.255.255.0"})
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "local network", cs)

	cs, err = lan.StageHandout(core.HandoutConfig{
		Enabled: true, First: "192.168.1.120", Last: "192.168.1.200", LeaseSeconds: 7200,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "handout", cs)

	res, r := lanWriter(t, nil)
	r.sectionType = "host"
	cs, err = res.StageReservation(core.ReservationConfig{MAC: "1a:a6:05:03:d4:9c", IP: "192.168.1.222"})
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "reservation", cs)

	// Removal builds its row by hand, so it is its own path to check — a
	// mutation that kept the key and changed the words survived until it was.
	gone, r := lanWriter(t, map[string]string{
		"dhcp.cfg05fe63":     "host",
		"dhcp.cfg05fe63.mac": "1a:a6:05:03:d4:9c",
		"dhcp.cfg05fe63.ip":  "192.168.1.222",
	})
	r.sectionType = "host"
	r.anonymous = map[string]string{"cfg05fe63": "@host[0]"}
	cs, err = gone.RemoveReservation("@host[0]")
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "removed reservation", cs)
}

// The regression itself: the same option on two roles must reach the panel as
// two different keys, otherwise one translation serves both and one of them is
// wrong for the person reading it.
func TestTheLocalAddressAndTheUplinkAddressAreDifferentKeys(t *testing.T) {
	lan, _ := lanWriter(t, map[string]string{"network.lan.proto": "static"})
	cs, err := lan.StageLAN(core.LANConfig{Address: "192.168.1.2", Netmask: "255.255.255.0"})
	if err != nil {
		t.Fatal(err)
	}
	var lanKey string
	for _, c := range cs {
		if c.Detail == "network.lan.ipaddr" {
			lanKey = c.LabelKey
		}
	}
	if lanKey != "network.lan.ipaddr" {
		t.Errorf("local address key = %q, want network.lan.ipaddr", lanKey)
	}
	if up := describe("network", roleUplink, "ipaddr").key; up == lanKey {
		t.Errorf("uplink and local address share the key %q", up)
	}
}

// Reading a draft back off the device must hand the panel the same keys as
// staging it did — F5 switches between the two paths and a person does not
// notice which one they are looking at.
func TestADraftReadBackCarriesTheSameKeys(t *testing.T) {
	m, _ := writerWith(map[string]string{"network.wan.proto": "dhcp"})
	staged, err := m.StageWAN(core.WANConfig{
		Interface: "wan", Proto: core.WANProtoStatic,
		Address: "198.51.100.9", Netmask: "255.255.255.0", Gateway: "198.51.100.1",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &recordingRunner{
		values: map[string]string{
			"network.wan.proto": "static", "network.wan.ipaddr": "198.51.100.9",
			"network.wan.netmask": "255.255.255.0", "network.wan.gateway": "198.51.100.1",
		},
		stagedLines: []string{
			"network.wan.proto='static'", "network.wan.ipaddr='198.51.100.9'",
			"network.wan.netmask='255.255.255.0'", "network.wan.gateway='198.51.100.1'",
		},
	}
	read, err := (networkManager{run: r.run}).StagedChanges()
	if err != nil {
		t.Fatal(err)
	}
	assertKnownKeys(t, "read back", read)
	keys := func(cs []core.ConfigChange) map[string]string {
		out := map[string]string{}
		for _, c := range cs {
			out[c.Detail] = c.LabelKey
		}
		return out
	}
	for detail, key := range keys(staged) {
		if got := keys(read)[detail]; got != key {
			t.Errorf("%s: staged key %q, read back key %q", detail, key, got)
		}
	}
}

// diffKeysFile is the panel's copy of LabelKeys. vue-tsc refuses to build the
// panel when one of these keys has no translation in a locale, so the list has
// to be current: a phrase added here without a TS update would reach people as
// untranslated English with nothing failing anywhere.
const diffKeysFile = "../../../web/src/i18n/diffLabelKeys.ts"

func renderDiffKeys() string {
	var b strings.Builder
	b.WriteString("// Generated from internal/adapters/openwrt (LabelKeys). Do not edit by hand:\n")
	b.WriteString("//   VB_UPDATE_GOLDEN=1 go test ./internal/adapters/openwrt/ -run TestDiffLabelKeysFileIsCurrent\n")
	b.WriteString("// Every key the device can send as `labelKey`. check.ts makes the build fail\n")
	b.WriteString("// when one of them has no translation under `diff` in a locale bundle.\n")
	b.WriteString("export const DIFF_LABEL_KEYS = [\n")
	for _, k := range LabelKeys() {
		b.WriteString("  '" + k + "',\n")
	}
	b.WriteString("] as const\n")
	return b.String()
}

func TestDiffLabelKeysFileIsCurrent(t *testing.T) {
	want := renderDiffKeys()
	path := filepath.FromSlash(diffKeysFile)
	if os.Getenv("VB_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", diffKeysFile, err)
	}
	if string(got) != want {
		t.Errorf("%s is stale; regenerate it with\n  VB_UPDATE_GOLDEN=1 go test ./internal/adapters/openwrt/ -run TestDiffLabelKeysFileIsCurrent", diffKeysFile)
	}
}

// Discard must reset every file the panel writes. The list is kept by hand, so
// it is tied here to the one thing that grows whenever the panel learns to
// write somewhere new: the words for that file's settings. The firewall got
// words and was left out of the list once — a discarded port forward stayed
// on the router, staged, for the next apply to commit.
func TestDiscardResetsEveryFileThePanelWrites(t *testing.T) {
	for key := range optionLabels {
		config, _, _ := strings.Cut(key, ".")
		if !slices.Contains(writtenConfigs, config) {
			t.Errorf("the panel has words for %q but discarding a draft leaves %s alone", key, config)
		}
	}
	m, r := writerWith(map[string]string{})
	if err := m.DiscardStaged(); err != nil {
		t.Fatal(err)
	}
	for _, config := range writtenConfigs {
		found := false
		for _, c := range r.calls {
			if strings.Join(c, " ") == "uci revert "+config {
				found = true
			}
		}
		if !found {
			t.Errorf("discard did not revert %s: %v", config, r.calls)
		}
	}
}
