package core

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Devices on the local network (M4, #51; DESIGN D-85, D-86, D-92).
//
// A device is its hardware (MAC) address: it is the only key the router has
// (D-85). What the router observes — addresses, how the device is connected,
// when it was last heard — comes from the adapter; what the owner said about
// it — a name, "I know this one" — is the panel's own and lives in the
// panel's settings, not in the router's network configuration (D-86, D-58).

// Link kinds. "unknown" is a real answer: a device the panel remembers by name
// but has not seen since it started watching could be on either.
const (
	LinkCable   = "cable"
	LinkWiFi    = "wifi"
	LinkUnknown = "unknown"
)

// DeviceLink is how a device is connected, as a fact the router observed.
type DeviceLink struct {
	Kind string `json:"kind" enum:"cable,wifi,unknown" doc:"How the device reaches the router"`
	// Band and SignalDBm come from the access point the device is associated
	// with, so they are present only while it is.
	Band      string `json:"band,omitempty" enum:"2.4,5,6" doc:"Wi-Fi band in GHz, while associated"`
	SignalDBm int    `json:"signalDbm,omitempty" doc:"Signal the access point hears from the device, while associated"`
}

// Device is one device on the local network.
type Device struct {
	MAC string `json:"mac" doc:"Hardware address, lower case; the key of the device (D-85)"`
	// Name is what the owner called it in the panel; ReportedName is what the
	// device called itself. They are separate because only the first one is
	// the owner's word.
	Name         string `json:"name,omitempty" doc:"Name given in the panel; kept in the panel's settings, not published in DNS"`
	ReportedName string `json:"reportedName,omitempty" doc:"What the device called itself when it asked for an address"`
	// IPs are the addresses the router knows for the device: its running
	// lease and the addresses it was heard using. For a device that is not
	// online they are the last ones known. Link-local IPv6 is left out: it
	// means nothing to a person and every device has one.
	IPs        []string `json:"ips" doc:"Addresses the router knows for the device (the last known ones if it is not online); IPv4 first, then IPv6, no link-local"`
	ReservedIP string   `json:"reservedIp,omitempty" doc:"Address reserved for this device on the local network"`
	// PrivateAddress means the locally administered bit is set: phones use
	// such addresses to avoid being tracked, and may change them (D-85).
	PrivateAddress bool `json:"privateAddress" doc:"The device uses a private (randomised) hardware address and may change it"`
	// New is true until the owner names the device or says they know it
	// (D-86). It is not "first seen recently": a device nobody has looked at
	// is new however long it has been around.
	New    bool `json:"new" doc:"Not named and not marked as known yet"`
	Online bool `json:"online" doc:"Connected now: associated with an access point, or heard on the cable recently"`
	// LastSeenSec is how long ago the router last heard the device, for a
	// device that is not online. It is a duration and not a clock reading:
	// a router without a battery-backed clock is wrong about the time after a
	// reboot. Absent when the panel has not heard it since it started
	// watching (see DeviceList.WatchingSec).
	LastSeenSec *int64     `json:"lastSeenSec,omitempty" doc:"Seconds since the router last heard a device that is not online; absent if not heard since the panel started watching"`
	Link        DeviceLink `json:"link"`
}

// DeviceList is the answer to "who is on my network".
type DeviceList struct {
	Devices []Device `json:"devices"`
	// WatchingSec is how far back "last seen" can reach: the panel keeps it in
	// memory only (D-13), so after a restart it knows nothing older.
	WatchingSec int64 `json:"watchingSec" doc:"How long the panel has been watching the network; last-seen times cannot go further back"`
}

// DeviceNote is what the panel remembers about one device.
type DeviceNote struct {
	MAC   string `json:"mac"`
	Name  string `json:"name,omitempty"`
	Known bool   `json:"known,omitempty"`
}

// MaxDeviceNameLen bounds a name in characters, not bytes: the names people
// give are Cyrillic as often as not ("Телевизор в гостиной (LG)").
const MaxDeviceNameLen = 64

// NormalizeMAC returns a hardware address in the one spelling the panel uses:
// six lower-case octets separated by colons.
func NormalizeMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("%q is not a hardware (MAC) address", s)
	}
	if hw[0]&1 == 1 {
		return "", fmt.Errorf("%s is a group address, not a device", hw)
	}
	return hw.String(), nil
}

// IsPrivateMAC reports whether the locally administered bit is set — the
// address was not burnt in by the maker, which on a phone means a private,
// possibly changing address (D-85).
func IsPrivateMAC(mac string) bool {
	hw, err := net.ParseMAC(mac)
	return err == nil && len(hw) > 0 && hw[0]&2 == 2
}

// CleanDeviceName trims a name and refuses one the panel could not show as
// typed. An empty result means "remove the name".
func CleanDeviceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxDeviceNameLen {
		return "", fmt.Errorf("a name can be at most %d characters", MaxDeviceNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("a name cannot contain line breaks or control characters")
		}
	}
	return name, nil
}

// MergeDevices adds what the panel remembers to what the router observes.
// A device the owner named stays in the list when the router no longer sees
// it — a named device that vanishes from the list is how "the child's phone
// changed its address" would go unnoticed (D-85).
func MergeDevices(seen []Device, notes []DeviceNote) []Device {
	byMAC := make(map[string]DeviceNote, len(notes))
	for _, n := range notes {
		byMAC[n.MAC] = n
	}
	out := make([]Device, 0, len(seen)+len(notes))
	present := make(map[string]bool, len(seen))
	for _, d := range seen {
		present[d.MAC] = true
		out = append(out, annotate(d, byMAC[d.MAC]))
	}
	for _, n := range notes {
		if present[n.MAC] || (n.Name == "" && !n.Known) {
			continue
		}
		// Remembered but not seen since the panel started watching: nothing
		// about its connection is known, and it is said so.
		out = append(out, annotate(Device{MAC: n.MAC, Link: DeviceLink{Kind: LinkUnknown}}, n))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out
}

func annotate(d Device, n DeviceNote) Device {
	d.Name = n.Name
	d.New = n.Name == "" && !n.Known
	d.PrivateAddress = IsPrivateMAC(d.MAC)
	if d.IPs == nil {
		d.IPs = []string{}
	}
	if d.Link.Kind == "" {
		d.Link.Kind = LinkUnknown
	}
	return d
}
