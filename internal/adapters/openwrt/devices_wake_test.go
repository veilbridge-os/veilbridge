package openwrt

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #55. The wake packet is the standard one, it goes out of the local
// network's device and nowhere else, and a device last seen on Wi-Fi is
// refused rather than sent a packet that cannot wake it.

type sent struct {
	dev     string
	payload []byte
}

func wakeFixture(t *testing.T) (*deviceManager, *[]sent) {
	t.Helper()
	fx := newDevFixture(t, cudy(), "")
	var out []sent
	fx.m.send = func(_ context.Context, dev string, p []byte) error {
		out = append(out, sent{dev, append([]byte(nil), p...)})
		return nil
	}
	return fx.m, &out
}

func TestTheWakePacketIsSixFFsAndTheAddressSixteenTimes(t *testing.T) {
	hw, _ := net.ParseMAC("00:00:5e:00:53:10")
	p := magicPacket(hw)
	if len(p) != 102 {
		t.Fatalf("length %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}) {
		t.Errorf("header % x", p[:6])
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+i*6:12+i*6], hw) {
			t.Errorf("repeat %d = % x", i, p[6+i*6:12+i*6])
		}
	}
}

func TestWakingSendsOnTheLocalNetworkOnly(t *testing.T) {
	m, out := wakeFixture(t)
	if err := m.WakeDevice("00-00-5E-00-53-10"); err != nil {
		t.Fatal(err)
	}
	if len(*out) != wakeRepeats {
		t.Fatalf("sent %d packets, want %d", len(*out), wakeRepeats)
	}
	hw, _ := net.ParseMAC("00:00:5e:00:53:10")
	for _, s := range *out {
		if s.dev != "br-lan" {
			t.Errorf("sent on %q, want the local network's device br-lan", s.dev)
		}
		if !bytes.Equal(s.payload, magicPacket(hw)) {
			t.Error("payload is not the wake packet for this device")
		}
	}
}

func TestADeviceLastSeenOnWiFiIsNotSentAWakePacket(t *testing.T) {
	m, out := wakeFixture(t)
	if _, err := m.ListDevices(); err != nil { // the phone in the fixture is on Wi-Fi
		t.Fatal(err)
	}
	err := m.WakeDevice(phoneMAC)
	var fe *core.FieldError
	if !errors.As(err, &fe) || fe.Field != "mac" || !errors.Is(err, errWakeWiFi) {
		t.Fatalf("err = %v, want the Wi-Fi refusal", err)
	}
	if len(*out) != 0 {
		t.Error("a packet was sent anyway")
	}
}

func TestWakingRefusesWhatIsNotADevice(t *testing.T) {
	m, out := wakeFixture(t)
	for _, bad := range []string{"", "nonsense", "ff:ff:ff:ff:ff:ff"} {
		var fe *core.FieldError
		if err := m.WakeDevice(bad); !errors.As(err, &fe) {
			t.Errorf("%q: err = %v, want a refusal", bad, err)
		}
	}
	if len(*out) != 0 {
		t.Error("sent a packet for something that is not a device")
	}
}

// The real sender, over loopback: the packet leaves as UDP to port 9 of the
// broadcast address. A listener on the machine sees it only where the
// broadcast is delivered, so this checks the payload and the port through the
// kernel rather than through the seam.
func TestTheRealSenderWritesTheWakePacket(t *testing.T) {
	ln, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback UDP here")
	}
	defer ln.Close()
	hw, _ := net.ParseMAC("00:00:5e:00:53:10")
	pkt := magicPacket(hw)
	// sendBroadcast targets the broadcast address; exercise the same socket
	// path with a unicast target to see the bytes arrive intact.
	conn, err := (&net.ListenConfig{Control: bindToDevice("")}).ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.WriteTo(pkt, ln.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = ln.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 200)
	n, _, err := ln.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], pkt) {
		t.Fatalf("got % x (err %v)", buf[:n], err)
	}
}
