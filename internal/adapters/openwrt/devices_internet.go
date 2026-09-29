package openwrt

import (
	"context"
	"fmt"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Turning a device's internet off (#53, D-88, D-97).
//
// The block is one firewall rule per device, in the router's own firewall
// configuration: it is visible in LuCI, survives the panel being removed, and
// goes through the same apply transaction as any firewall change (D-69).
// Its shape is the one measured with a phone in #50 — traffic from the local
// zone, from this hardware address, to ANY zone, rejected — so it covers the
// uplink, IPv6 routed without NAT, and a tunnel added later alike. It does
// not stop the device from reaching others on the same bridge: the switch
// connects them without the router, which is what the panel says.
//
// The rule is a NAMED section whose name comes from the address. That makes
// it the panel's own without a marker option firewall4 would not know, lets
// "turn off" twice be one rule, and keeps its name stable in `uci changes`,
// where anonymous sections are renamed (see keysOf).

// noInternetPrefix starts the name of every block section. 10 + 12 hex digits
// fits uci's 32-character section names.
const noInternetPrefix = "vb_noinet_"

// noInternetSection is the section that holds the block for mac.
func noInternetSection(mac string) string {
	return noInternetPrefix + strings.ReplaceAll(mac, ":", "")
}

// isNoInternetSection reports whether a firewall section is one of the
// panel's device blocks.
func isNoInternetSection(section string) bool {
	return strings.HasPrefix(section, noInternetPrefix)
}

// blockedDevices lists the devices whose internet is off, from the firewall
// as `uci show` prints it — the draft included, like the firewall screen. A
// block that was switched off by hand in LuCI (`enabled=0`) is not a block.
func blockedDevices(show string) map[string]bool {
	out := map[string]bool{}
	for _, s := range parseUCISections(show) {
		if s.kind != "rule" || !isNoInternetSection(s.id) || s.options["enabled"] == "0" {
			continue
		}
		for _, raw := range strings.Fields(s.options["src_mac"]) {
			if mac, err := core.NormalizeMAC(raw); err == nil {
				out[mac] = true
			}
		}
	}
	return out
}

// blocked reads the blocked devices. A failure reads as "none known": the
// list of devices is still worth showing, and nothing on it acts on a guess —
// turning internet on or off reads the firewall again.
func (m networkManager) blocked(ctx context.Context) map[string]bool {
	if m.run == nil {
		return map[string]bool{}
	}
	out, err := m.run(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return map[string]bool{}
	}
	return blockedDevices(string(out))
}

// StageDeviceInternet stages turning a device's internet off, or back on.
// Asking for what is already so changes nothing and returns no rows.
func (m *deviceManager) StageDeviceInternet(mac string, allowed bool) ([]core.ConfigChange, error) {
	if m.net.run == nil {
		return nil, core.ErrNotImplemented
	}
	mac, err := core.NormalizeMAC(mac)
	if err != nil {
		return nil, core.Refuse("mac", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	section := noInternetSection(mac)
	key := "firewall." + section
	exists := m.net.uciGet(ctx, key) != ""
	said := describe("firewall", roleNoInternet, "")
	row := core.ConfigChange{
		Label: said.words, LabelKey: said.key, Dangerous: dangerousConfig("firewall"), Detail: key,
	}

	if allowed {
		if !exists {
			return nil, nil
		}
		if err := m.net.uciDelete(ctx, key); err != nil {
			return nil, err
		}
		row.From = mac
		return []core.ConfigChange{row}, nil
	}
	if exists {
		// Present but switched off by hand is a block somebody paused; the
		// panel does not quietly override that by turning it back on.
		if m.net.uciGet(ctx, key+".enabled") == "0" {
			return nil, core.Refuse("mac", fmt.Errorf(
				"openwrt: this device's block is switched off in the firewall settings; switch it on there, or remove it"))
		}
		return nil, nil
	}

	fw, err := m.net.FirewallInfo()
	if err != nil {
		return nil, err
	}
	local := ""
	for _, z := range fw.Zones {
		if z.Role == core.ZoneLocal {
			local = z.Name
		}
	}
	if local == "" {
		return nil, fmt.Errorf("openwrt: the local network is in no firewall zone, so there is nowhere to put the block")
	}

	baseline, err := m.net.fw4Warnings(ctx)
	if err != nil {
		return nil, err
	}
	sets := [][2]string{
		{key, "rule"},
		{key + ".name", "VeilBridge: no internet for " + mac},
		{key + ".src", local},
		{key + ".dest", "*"},
		{key + ".src_mac", mac},
		{key + ".proto", "all"},
		{key + ".target", "REJECT"},
	}
	for _, s := range sets {
		if err := m.net.uciSet(ctx, s[0], s[1]); err != nil {
			// Only this section is taken back: the rest of the firewall
			// draft may be somebody's port forward, reviewed and waiting.
			_, _ = m.net.run(ctx, "uci", "revert", key)
			return nil, err
		}
	}
	if err := m.net.fw4Accepts(ctx, baseline); err != nil {
		_, _ = m.net.run(ctx, "uci", "revert", key)
		return nil, err
	}
	row.To = mac
	return []core.ConfigChange{row}, nil
}

var _ core.DeviceInternetWriter = (*deviceManager)(nil)
