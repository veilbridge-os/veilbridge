package openwrt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// Waking a device (#55, D-87). Measured in #50: the router needs no extra
// package (etherwake) — the daemon sends the wake packet itself, a UDP
// broadcast on the local network, pinned to the local network's device so it
// cannot leave through the uplink or a tunnel.
//
// The packet is the standard one: six 0xFF bytes, then the device's hardware
// address sixteen times. Port 9 ("discard") is what wakeonlan and most
// firmware use; the network card matches the payload, not the port.

const (
	wakePort    = 9
	wakeRepeats = 3 // as etherwake and wakeonlan do: one lost broadcast is common
	wakeGap     = 100 * time.Millisecond
)

// magicPacket is the wake packet for mac.
func magicPacket(mac net.HardwareAddr) []byte {
	out := make([]byte, 0, 6+16*6)
	for i := 0; i < 6; i++ {
		out = append(out, 0xff)
	}
	for i := 0; i < 16; i++ {
		out = append(out, mac...)
	}
	return out
}

// sendBroadcast sends payload as a UDP broadcast out of dev only.
func sendBroadcast(ctx context.Context, dev string, payload []byte) error {
	lc := net.ListenConfig{Control: bindToDevice(dev)}
	conn, err := lc.ListenPacket(ctx, "udp4", ":0")
	if err != nil {
		return fmt.Errorf("openwrt: open a socket on %s: %w", dev, err)
	}
	defer conn.Close()
	// Go enables SO_BROADCAST on datagram sockets itself.
	_, err = conn.WriteTo(payload, &net.UDPAddr{IP: net.IPv4bcast, Port: wakePort})
	return err
}

var errWakeWiFi = errors.New("a device that sleeps on Wi-Fi cannot be woken over the network — only over a cable")

// WakeDevice sends the wake packet for mac on the local network.
func (m *deviceManager) WakeDevice(raw string) error {
	mac, err := core.NormalizeMAC(raw)
	if err != nil {
		return core.Refuse("mac", err)
	}
	m.mu.Lock()
	last := m.seen[mac]
	m.mu.Unlock()
	if last.link.Kind == core.LinkWiFi {
		return core.Refuse("mac", errWakeWiFi)
	}
	dev, err := m.lanDevice()
	if err != nil {
		return err
	}
	hw, _ := net.ParseMAC(mac)
	send := m.send
	if send == nil {
		send = sendBroadcast
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pkt := magicPacket(hw)
	for i := 0; i < wakeRepeats; i++ {
		if i > 0 {
			time.Sleep(wakeGap)
		}
		if err := send(ctx, dev, pkt); err != nil {
			return fmt.Errorf("openwrt: send the wake packet: %w", err)
		}
	}
	return nil
}

var _ core.DeviceWaker = (*deviceManager)(nil)
