package openwrt

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
)

// networkManager answers "what is this router's network doing right now" from
// netifd, via ubus, and stages changes to it via uci (see network_write.go).
//
// Reading and writing use different tools on purpose. netifd knows what the
// device is actually doing; uci knows what it was told to do. A getter that
// read /etc/config would report a DHCP uplink as having no address, and a
// setter that wrote through netifd would change the running state without
// changing the configuration that survives a reboot.
//
// Nothing here commits: staging is one verb and committing is another, owned
// by the apply transaction (uci.go) under a watchdog. That split is what the
// M1 risk gate proved, so a getter — or a rejected edit — can never lock
// anybody out.
//
// It asks netifd rather than reading /proc/net/route, and the rejected option
// says why: /proc/net/route knows kernel devices (br-lan, eth1) but not the
// logical names the panel and uci speak (lan, wan), so every reading would
// have to be mapped back by guesswork.
//

type networkManager struct {
	bus *ubus.Client
	// run is the single seam through which this manager touches the OS for
	// writes, mirroring the one in the applier. Nil means "this build cannot
	// write" (tests, a dev machine) and every write method says so rather
	// than pretending to have staged something.
	run commandRunner
	// configDir is where the committed configuration lives. It is a field and
	// not a constant because reading a value as it is *without* the staged
	// draft needs a second view of that directory — see committedValues.
	configDir string
}

func newNetworkManager(bus *ubus.Client) networkManager {
	return networkManager{bus: bus, run: runCommand, configDir: "/etc/config"}
}

// ifaceTimeout bounds a dump. netifd normally answers in milliseconds; when it
// does not, a dashboard poll must fail rather than hang.
const ifaceTimeout = 10 * time.Second

// loopbackNames are interfaces that exist on every device and answer no
// question a panel asks. They are excluded from WAN candidacy but still
// listed: hiding an interface that exists is its own kind of lie.
var loopbackNames = map[string]bool{"loopback": true, "lo": true}

func (m networkManager) Interfaces() ([]core.NetworkInterface, error) {
	if m.bus == nil {
		return nil, core.ErrNotImplemented
	}
	ctx, cancel := context.WithTimeout(context.Background(), ifaceTimeout)
	defer cancel()

	raw, err := m.bus.Interfaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("openwrt: list interfaces: %w", err)
	}
	out := make([]core.NetworkInterface, 0, len(raw))
	for _, i := range raw {
		out = append(out, toCoreInterface(i))
	}
	// netifd returns interfaces in its own internal order, which changes when
	// an interface is reconfigured. A list that reshuffles under the operator
	// is a UI bug born in the adapter, so sort by name here.
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// toCoreInterface maps one netifd interface into the domain model. The address
// lists are rendered as CIDR because a mask of 24 means nothing next to an
// address without it, and every consumer would re-join them anyway.
func toCoreInterface(i ubus.Interface) core.NetworkInterface {
	out := core.NetworkInterface{
		Name:      i.Name,
		Device:    i.L3Device,
		Up:        i.Up,
		Proto:     i.Proto,
		UptimeSec: i.Uptime,
		DNS:       append([]string(nil), i.DNSServer...),
	}
	// L3Device is empty on an interface that never came up; the configured
	// device is still worth showing, so fall back to it rather than to "".
	if out.Device == "" {
		out.Device = i.Device
	}
	for _, a := range i.IPv4Address {
		out.IPv4 = append(out.IPv4, cidr(a))
	}
	for _, a := range i.IPv6Address {
		out.IPv6 = append(out.IPv6, cidr(a))
	}
	if gw, ok := i.DefaultRoute(); ok {
		out.Gateway = gw
	}
	if gw, ok := i.DefaultRoute6(); ok {
		out.Gateway6 = gw
	}
	return out
}

func cidr(a ubus.Address) string {
	return a.Address + "/" + strconv.Itoa(a.Mask)
}

// WANInfo picks the uplink. The rules, in order, and why there are three:
//
//  1. An interface with a default route. On a normal router exactly one has
//     it, and the question is answered by measurement.
//  2. Several candidates — a real case, not a hypothetical: the x86 stand has
//     two DHCP interfaces on the same segment, both with a default route. Then
//     the conventional name wins ("wan" exactly, else a name containing it),
//     because on OpenWrt that name is a convention netifd itself has no
//     opinion about.
//  3. Still tied: take the first by name and say so. A panel that silently
//     picks is worse than one that admits it picked.
//
// The chosen rule travels with the answer in SelectedBy, and every candidate
// is listed, so nothing about this decision is invisible to the operator.
func (m networkManager) WANInfo() (core.WANStatus, error) {
	ifaces, err := m.Interfaces()
	if err != nil {
		return core.WANStatus{}, err
	}
	var candidates []core.NetworkInterface
	for _, i := range ifaces {
		if loopbackNames[i.Name] || !i.HasDefaultRoute() {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) == 0 {
		return core.WANStatus{}, core.ErrNoWAN
	}

	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Name)
	}
	status := core.WANStatus{Candidates: names}

	if len(candidates) == 1 {
		status.Interface, status.SelectedBy = candidates[0], "default-route"
		return status, nil
	}
	if i, ok := pickByName(candidates); ok {
		status.Interface, status.SelectedBy = i, "name"
		return status, nil
	}
	status.Interface, status.SelectedBy = candidates[0], "first-candidate"
	return status, nil
}

// pickByName resolves a tie by the OpenWrt naming convention: an interface
// literally called "wan" wins; failing that, exactly one whose name contains
// "wan" (the x86 stand's "lanwan"). Two such names are not a tie-break, they
// are a coin toss, and it says so by returning false.
func pickByName(candidates []core.NetworkInterface) (core.NetworkInterface, bool) {
	for _, c := range candidates {
		if strings.EqualFold(c.Name, "wan") {
			return c, true
		}
	}
	var found core.NetworkInterface
	n := 0
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Name), "wan") {
			found, n = c, n+1
		}
	}
	return found, n == 1
}

var _ core.NetworkManager = networkManager{}
