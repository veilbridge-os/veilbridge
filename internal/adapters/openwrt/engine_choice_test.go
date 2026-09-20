package openwrt

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// M1.7 / D-34: the engine is chosen by probing, and the choice is visible
// through the product API. Before this, the userspace path existed only in
// cmd/p4smoke — a path no product test ever walked.

// probeWithTUN points the probe at a real character device (/dev/null is one
// on every Unix), which is what the kernel-tun probe actually checks for: a
// node that exists, is a char device, and opens read-write. Creating a real
// /dev/net/tun in a test would need root.
func probeWithTUN() sysProbe {
	p := newSysProbe()
	p.devNetTUN = "/dev/null"
	return p
}

func probeWithoutTUN(t *testing.T) sysProbe {
	t.Helper()
	p := newSysProbe()
	p.devNetTUN = filepath.Join(t.TempDir(), "no-such-tun")
	return p
}

func TestEngineIsChosenByProbingTheKernel(t *testing.T) {
	kernelEng, kernelKind := chooseEngine(probeWithTUN())
	if kernelKind != core.TunnelEngineKernel {
		t.Errorf("with a usable tun device: kind = %q, want %q", kernelKind, core.TunnelEngineKernel)
	}
	// The kernel engine is the one that can hand nftables an interface. If it
	// cannot, calling it "kernel" is just a label.
	if _, ok := kernelEng.(vpn.InterfaceEngine); !ok {
		t.Errorf("kernel choice returned %T, which exposes no OS interface", kernelEng)
	}

	userEng, userKind := chooseEngine(probeWithoutTUN(t))
	if userKind != core.TunnelEngineUserspace {
		t.Errorf("without a tun device: kind = %q, want %q", userKind, core.TunnelEngineUserspace)
	}
	if _, ok := userEng.(vpn.InterfaceEngine); ok {
		t.Errorf("userspace choice returned %T, which claims an OS interface", userEng)
	}
}

// The choice must reach the panel: a device silently running the userspace
// engine looks identical to a working router until someone asks why the LAN
// is not being routed.
func TestSystemInfoReportsTheEngineInUse(t *testing.T) {
	cases := map[string]struct {
		engine vpn.Engine
		want   string
	}{
		"kernel engine":    {engine: &fakeKernelEngine{}, want: core.TunnelEngineKernel},
		"userspace engine": {engine: &fakeEngine{dialer: stubDialer{}}, want: core.TunnelEngineUserspace},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, _ := newTestVPN(t, tc.engine)
			sys := newSystemManager(v, nil)
			sys.egress = func(func(*http.Request) (*http.Response, error)) string { return "" }

			info, err := sys.Info()
			if err != nil {
				t.Fatalf("info: %v", err)
			}
			if info.TunnelEngine != tc.want {
				t.Errorf("tunnelEngine = %q, want %q", info.TunnelEngine, tc.want)
			}
		})
	}
}

// The engine choice and the kernel-tun capability come from one measurement,
// so they can never contradict each other on the same device — which is what
// would make the panel's explanation a lie.
func TestEngineChoiceAgreesWithTheReportedCapability(t *testing.T) {
	for _, p := range []sysProbe{probeWithTUN(), probeWithoutTUN(t)} {
		_, kind := chooseEngine(p)
		cap := p.Capabilities()[core.CapKernelTUN]
		if cap.Available != (kind == core.TunnelEngineKernel) {
			t.Errorf("capability kernel-tun=%v but engine=%q (reason: %q)",
				cap.Available, kind, cap.Reason)
		}
		if !cap.Available && cap.Reason == "" {
			t.Error("the userspace fallback is reported with no reason a human can act on")
		}
	}
}
