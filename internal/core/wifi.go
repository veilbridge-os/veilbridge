package core

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Wi-Fi (M4, #57). Two kinds of edit, and they are applied differently on
// purpose (D-100):
//
//   - the RADIO (channel, width, band on or off, country) goes through the
//     apply transaction with its confirmation window, like the firewall: a bad
//     channel is cured by putting the old one back, and devices return by
//     themselves because the password did not change;
//   - ACCESS to a network (its name, password and security) is applied at
//     once, without a watchdog. Measured with two phones: after the password
//     was changed and reverted, neither came back by itself — a device that
//     saw its saved password refused marks the network and stops trying, and
//     a device that already joined with the new one is dropped a second time
//     by the revert. A timer here breaks what it promises to protect.

// ErrNoWiFi means the device has no radio: a shape of hardware, not a fault
// (D-20). The panel does not show the section at all.
var ErrNoWiFi = errors.New("veilbridge: this device has no Wi-Fi")

// ErrDraftNotEmpty refuses an access edit while other edits wait in the draft:
// an unconfirmed apply of those would roll the password back with them.
var ErrDraftNotEmpty = errors.New("veilbridge: other changes are waiting in the draft")

// WiFi security as the panel names it. Only the first two are offered
// (D-104); the others are shown as they are when somebody set them by hand.
const (
	WiFiWPA2     = "wpa2"
	WiFiWPA2WPA3 = "wpa2-wpa3"
	WiFiWPA3     = "wpa3"
	WiFiOpen     = "open"
	WiFiOther    = "other"
)

// Radio states: what the access point is doing right now.
const (
	RadioUp       = "up"
	RadioStarting = "starting"       // choosing a channel, or bringing itself up
	RadioRadar    = "checking-radar" // listening for radar before it may transmit
	RadioDown     = "down"
)

// Network kinds.
const (
	WiFiKindMain  = "main"  // on the local network
	WiFiKindGuest = "guest" // the guest network the panel made (#58)
	WiFiKindOther = "other" // anything else, including a guest network made by hand
)

// Guest network states (#58).
const (
	GuestNone    = "none"    // there is none; the panel can make one
	GuestOn      = "on"      // the panel's guest network is on the air
	GuestOff     = "off"     // it exists but its access points are off
	GuestForeign = "foreign" // somebody made a guest network by hand (LuCI); the panel leaves it alone
)

// ErrGuestExists refuses to make a second guest network over the first.
var ErrGuestExists = errors.New("veilbridge: there is a guest network already")

// ErrGuestBlocked refuses a guest network that could not be kept from the
// home network or the panel here; WiFiGuest.Blocked says why.
var ErrGuestBlocked = errors.New("veilbridge: guests could not be kept from the home network here")

// ErrNoGuest is an edit to a guest network that does not exist.
var ErrNoGuest = errors.New("veilbridge: there is no guest network")

// WiFiChannel is a channel the radio may use in its country right now.
type WiFiChannel struct {
	Channel int `json:"channel"`
	// Radar: the access point must listen for radar before it transmits on
	// this channel, and leaves it by itself when one appears.
	Radar bool `json:"radar,omitempty"`
}

// WiFiRadio is one radio of the device.
type WiFiRadio struct {
	ID string `json:"id" doc:"Stable name of the radio on the device"`
	// Band is "2.4", "5" or "6" (GHz), the same words the device list uses.
	Band    string `json:"band"`
	Enabled bool   `json:"enabled"`
	// Auto: the channel is chosen by the radio itself; Channel is then 0.
	Auto    bool `json:"auto"`
	Channel int  `json:"channel,omitempty"`
	// ChannelNow is the channel it transmits on at this moment; 0 when it
	// does not (off, starting).
	ChannelNow int `json:"channelNow,omitempty"`
	// Width in MHz and the widths this radio supports.
	Width  int   `json:"width,omitempty"`
	Widths []int `json:"widths"`
	// Channels the radio may be set to in its country (D-102): without a
	// country, only the channels that need neither radar detection nor a
	// passive scan.
	Channels []WiFiChannel `json:"channels"`
	// Devices connected to this radio now, over every network on it.
	Devices int    `json:"devices"`
	State   string `json:"state" enum:"up,starting,checking-radar,down"`
}

// WiFiNetwork is one network (SSID) the router offers. The same name,
// password and security on several radios is ONE network (the usual shape
// out of the box).
type WiFiNetwork struct {
	ID   string `json:"id" doc:"Stable name of the network on the device"`
	Kind string `json:"kind" enum:"main,guest,other"`
	// Enabled: its access points are on. A guest network that is off keeps
	// its name and password (#58).
	Enabled bool   `json:"enabled"`
	SSID    string `json:"ssid"`
	// Security is one of wpa2, wpa2-wpa3, wpa3, open, other.
	Security string `json:"security" enum:"wpa2,wpa2-wpa3,wpa3,open,other"`
	// Radios it is on, by WiFiRadio.ID.
	Radios []string `json:"radios"`
	// Devices connected to it now.
	Devices int `json:"devices"`
	// HasPassword: the password itself is never in this answer (D-103); it
	// is asked for separately when somebody presses "Show".
	HasPassword bool `json:"hasPassword"`
}

// WiFiHere says how the browser showing the panel is connected, so every
// warning can name it: "you are on 5 GHz".
type WiFiHere struct {
	Kind  string `json:"kind" enum:"wifi,cable,other"`
	Radio string `json:"radio,omitempty"`
	Band  string `json:"band,omitempty"`
}

// WiFiStatus is the whole Wi-Fi screen.
type WiFiStatus struct {
	// Country the radios are set to; empty when none is (D-102).
	Country  string        `json:"country"`
	Radios   []WiFiRadio   `json:"radios"`
	Networks []WiFiNetwork `json:"networks"`
	Here     *WiFiHere     `json:"here,omitempty"`
	// Guest is absent when this device cannot have one the panel manages.
	Guest *WiFiGuest `json:"guest,omitempty"`
}

// WiFiGuest is the guest network as the panel sees it (#58, D-105/D-106).
type WiFiGuest struct {
	State string `json:"state" enum:"none,on,off,foreign"`
	// Network is the WiFiNetwork.ID of the guest network when it is on or off.
	Network string `json:"network,omitempty"`
	// OnAir: one of its access points is on, on a radio that is on. "on"
	// with every hosting radio off is on in the settings and silent on air.
	OnAir bool `json:"onAir"`
	// IPv6: guests get IPv6, or will once the guest network is made.
	IPv6 bool `json:"ipv6"`
	// RenumbersHome: making the guest network changes the IPv6 addresses of
	// the home network once, to leave room for the guests (D-106).
	RenumbersHome bool `json:"renumbersHome"`
	// Blocked says why guests could NOT be kept from the home network here:
	// the uplink shares a firewall zone with it, or a rule accepts traffic
	// from every zone. A guest network is not made then; one that exists is
	// shown with the warning. BlockedBy names the rule.
	Blocked   string `json:"blocked,omitempty" enum:"shared-zone,open-rule,guest-rule,no-uplink,no-nat,split-uplink,no-dhcp,weakened,unchecked"`
	BlockedBy string `json:"blockedBy,omitempty"`
}

// Why a guest network cannot be isolated (WiFiGuest.Blocked).
const (
	GuestBlockedSharedZone = "shared-zone"
	// A rule accepting traffic from EVERY zone: fw4 runs it before the zones.
	GuestBlockedOpenRule = "open-rule"
	// A rule, port forward or forwarding from the guests' zone, left from an
	// earlier guest network or made by hand, would open the new one.
	GuestBlockedGuestRule = "guest-rule"
	// The uplink's firewall zone is not known: nowhere to forward guests to.
	GuestBlockedNoUplink = "no-uplink"
	// The uplink's zone does not masquerade every source: guests would have
	// no internet.
	GuestBlockedNoNAT = "no-nat"
	// IPv6 leaves through another zone than IPv4.
	GuestBlockedSplitUplink = "split-uplink"
	// The router's DHCP/DNS server is limited to named interfaces: guests
	// would get no address and no DNS.
	GuestBlockedNoDHCP = "no-dhcp"
	// The panel's own guest network had a protection changed elsewhere (a
	// rule turned off, a zone policy, an access point's isolation).
	GuestBlockedWeakened = "weakened"
	// The router's live configuration could not be read: isolation is not
	// known, so it is not promised.
	GuestBlockedUnchecked = "unchecked"
)

// GuestConfig is a guest network to make. Empty Security is wpa2-wpa3
// (D-104); no Radios is every radio.
type GuestConfig struct {
	SSID     string   `json:"ssid"`
	Password string   `json:"password"`
	Security string   `json:"security,omitempty" enum:"wpa2,wpa2-wpa3,"`
	Radios   []string `json:"radios,omitempty" doc:"Radio ids from GET /wifi; none means every radio"`
}

// RadioConfig is a radio edit. Channel is "auto" or a number.
type RadioConfig struct {
	Enabled bool   `json:"enabled"`
	Channel string `json:"channel" doc:"\"auto\" or a channel number from the radio's channels"`
	Width   int    `json:"width" doc:"Channel width in MHz, one of the radio's widths"`
}

// AccessConfig is an access edit. An empty Password keeps the current one;
// an empty Security keeps the current one.
type AccessConfig struct {
	SSID     string `json:"ssid"`
	Password string `json:"password,omitempty"`
	Security string `json:"security,omitempty" enum:"wpa2,wpa2-wpa3,"`
}

// WiFiManager is the Wi-Fi half of an adapter. Like every writer it only
// stages; applying is the transaction's business — except for the access
// edit, which the API applies at once (D-100).
type WiFiManager interface {
	Status() (WiFiStatus, error)
	// WiFiPassword returns a network's password (D-103).
	WiFiPassword(network string) (string, error)
	StageRadio(id string, cfg RadioConfig) ([]ConfigChange, error)
	StageCountry(code string) ([]ConfigChange, error)
	StageAccess(network string, cfg AccessConfig) ([]ConfigChange, error)
}

// WiFiGuestManager stages the guest network (#58). Every edit goes through
// the apply bar and its confirmation window: it changes the network and the
// firewall, not only who may join (D-100). The guest's password is changed
// like any network's, by StageAccess.
type WiFiGuestManager interface {
	StageGuest(cfg GuestConfig) ([]ConfigChange, error)
	StageGuestEnabled(enabled bool) ([]ConfigChange, error)
	StageGuestRemove() ([]ConfigChange, error)
}

// WiFiProvider is how an adapter offers Wi-Fi; asked for by type assertion,
// since not every adapter has it.
type WiFiProvider interface {
	WiFi() WiFiManager
}

// ValidSSID refuses a name a radio will not broadcast: 1–32 BYTES, which is
// 16 letters of Cyrillic.
func ValidSSID(ssid string) error {
	n := len(ssid)
	switch {
	case n == 0:
		return Refuse("ssid", errors.New("the network needs a name"))
	case n > 32:
		return Refuse("ssid", fmt.Errorf("the name is %d bytes long, at most 32 fit", n))
	case !utf8.ValidString(ssid):
		return Refuse("ssid", errors.New("the name is not valid text"))
	}
	for _, r := range ssid {
		if r < 0x20 || r == 0x7f {
			return Refuse("ssid", errors.New("the name contains a control character"))
		}
	}
	return nil
}

// ValidWiFiPassword refuses what WPA will not take: 8–63 printable ASCII
// characters, or 64 hexadecimal digits.
func ValidWiFiPassword(p string) error {
	if len(p) == 64 && strings.Trim(strings.ToLower(p), "0123456789abcdef") == "" {
		return nil
	}
	if len(p) < 8 || len(p) > 63 {
		return Refuse("password", fmt.Errorf("the password is %d characters long, it has to be 8 to 63", len(p)))
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] > 0x7e {
			return Refuse("password", errors.New("only Latin letters, digits and punctuation fit in a Wi-Fi password"))
		}
	}
	return nil
}

// WiFiHereFrom finds how the browser is connected from the device list with
// "here" marked (MarkHere): a device on Wi-Fi sits on the radio of its band.
func WiFiHereFrom(devices []Device, radios []WiFiRadio) *WiFiHere {
	for _, d := range devices {
		if !d.Here {
			continue
		}
		switch d.Link.Kind {
		case LinkCable:
			return &WiFiHere{Kind: "cable"}
		case LinkWiFi:
			h := &WiFiHere{Kind: "wifi", Band: d.Link.Band}
			for _, r := range radios {
				if r.Band == d.Link.Band {
					h.Radio = r.ID
					break
				}
			}
			return h
		}
		return &WiFiHere{Kind: "other"}
	}
	return &WiFiHere{Kind: "other"}
}
