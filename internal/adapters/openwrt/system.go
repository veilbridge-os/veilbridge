package openwrt

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/adapters/openwrt/ubus"
	"github.com/veilbridge-os/veilbridge/internal/core"
	"github.com/veilbridge-os/veilbridge/internal/vpn"
)

// systemManager answers the dashboard and runs egress-comparing path probes.
// It borrows the VPN manager to know the active node and its dialer.
//
// Two sources, deliberately: ubus is asked first because it is the device's
// own account of itself (model, firmware, overlay space — none of which /proc
// knows), and /proc is kept as the answer of last resort. A dashboard that
// goes blank because one fork failed is worse than one showing slightly less.
type systemManager struct {
	vpn *vpnManager
	// platform labels SystemInfo (e.g. "openwrt"); set by the adapter ctor.
	platform string
	// egress resolves the public IP reached via the given HTTP-do function.
	// Overridable in tests so probes don't hit the real network.
	egress func(do func(*http.Request) (*http.Response, error)) string
	// bus is the ubus client, nil on a host that has no ubus (tests, dev
	// machines). Nil is a supported state, not a bug: see enrich.
	bus *ubus.Client
	// procCPU and procUptime are the /proc fallback readings, injectable
	// because otherwise the tests for "a zero from ubus must not overwrite
	// them" would pass for the wrong reason on any machine without /proc
	// (every macOS dev box): there the fallback is zero too, and agreeing
	// with a broken implementation is not a passing test.
	procCPU    func() float64
	procUptime func() int64
	procMem    func() (used, total int64)
	// board is cached after the first successful read. A router does not
	// change its model or firmware while running, and the dashboard polls.
	boardOnce sync.Once
	board     ubus.Board
	boardOK   bool
}

func newSystemManager(v *vpnManager, bus *ubus.Client) *systemManager {
	return &systemManager{
		vpn: v, platform: "openwrt", egress: egressIP, bus: bus,
		procCPU: readLoadAsCPU, procUptime: readUptime, procMem: readMem,
	}
}

func (m *systemManager) Info() (core.SystemInfo, error) {
	info := core.SystemInfo{Platform: m.platform}
	info.Hostname, _ = os.Hostname()
	info.UptimeSec = m.procUptime()
	info.MemUsed, info.MemTotal = m.procMem()
	info.CPUPercent = m.procCPU()
	m.enrich(&info)

	// Direct WAN IP (no tunnel) — best effort, short timeout.
	info.WANIP = m.egress(http.DefaultClient.Do)

	// If a tunnel is active, report its egress + that traffic flows through it.
	// Userspace engines expose a Dialer; kernel engines (OpenWrt) a TUN interface.
	switch d := m.vpn.activeDialer(); {
	case d != nil:
		info.EgressIP = m.egressVia(d)
	default:
		if tun := m.vpn.activeTunName(); tun != "" {
			info.EgressIP = m.egressViaInterface(tun)
		}
	}
	if info.EgressIP != "" {
		info.TunnelUp = info.EgressIP != info.WANIP
	} else {
		info.EgressIP = info.WANIP
	}
	return info, nil
}

// ProbePath checks whether traffic to target egresses via the expected path.
// It does NOT trust HTTP status — it compares the egress IP reached through the
// tunnel against the direct WAN IP (D-5, the "curl 200 ≠ tunnel" lesson).
func (m *systemManager) ProbePath(target string, expected core.Target) (core.PathProbe, error) {
	probe := core.PathProbe{Target: target, ExpectedVia: expected}

	direct := m.egress(http.DefaultClient.Do)

	// Resolve the tunnel egress regardless of engine type: userspace engines via
	// their Dialer, kernel engines (OpenWrt) by binding to the tunnel interface.
	dialer := m.vpn.activeDialer()
	tun := m.vpn.activeTunName()
	var through string
	switch {
	case dialer != nil:
		through = m.egressVia(dialer)
	case tun != "":
		through = m.egressViaInterface(tun)
	}

	switch {
	case dialer == nil && tun == "":
		// No tunnel up: everything goes direct.
		probe.ActualVia = core.TargetDirect
		probe.Detail = fmt.Sprintf("no active tunnel; egress=%s", direct)
	case through == "":
		probe.ActualVia = core.TargetDirect
		probe.Detail = "tunnel up but no traffic flowed through it"
	case through != direct:
		probe.ActualVia = core.TargetTunnel
		probe.Detail = fmt.Sprintf("tunnel egress=%s (direct=%s)", through, direct)
	default:
		probe.ActualVia = core.TargetDirect
		probe.Detail = fmt.Sprintf("tunnel egress == direct (%s)", direct)
	}
	probe.OK = probe.ActualVia == expected
	return probe, nil
}

func (m *systemManager) Diagnostics(target string) (string, error) {
	if !validHost(target) {
		return "", fmt.Errorf("openwrt: invalid diagnostics target %q", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// -c 4: four pings, bounded. ping must be on PATH.
	out, err := exec.CommandContext(ctx, "ping", "-c", "4", target).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("openwrt: ping %q: %w", target, err)
	}
	return string(out), nil
}

// --- ubus enrichment (M1.4) ---

// enrich overlays what the device says about itself onto the /proc reading.
// Every failure here is silent on purpose: the fields it fills are extra
// detail, and losing them must not cost the operator the panel. What it does
// NOT do is invent — a field ubus could not answer stays as /proc left it, or
// empty.
func (m *systemManager) enrich(info *core.SystemInfo) {
	if m.bus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if b, ok := m.cachedBoard(ctx); ok {
		info.Model = b.Model
		info.Kernel = b.Kernel
		info.Firmware = b.Release.Description
		if b.Hostname != "" {
			// os.Hostname() and ubus agree on a healthy device; when they do
			// not, the device's own answer is the one uci will show.
			info.Hostname = b.Hostname
		}
	}

	si, err := m.bus.SystemInfo(ctx)
	if err != nil {
		return
	}
	if si.Uptime > 0 {
		info.UptimeSec = si.Uptime
	}
	if si.Memory.Total > 0 {
		// Used = total - available, matching readMem: "free" alone counts the
		// page cache as used and would show a healthy router at 95% memory.
		info.MemTotal = si.Memory.Total
		info.MemUsed = si.Memory.Total - si.Memory.Available
	}
	if si.Root.Total > 0 {
		// ubus reports the filesystem blocks in kilobytes, unlike memory.
		info.StorageTotal = si.Root.Total * 1024
		info.StorageUsed = si.Root.Used * 1024
	}
	info.LoadAvg = si.LoadAverage()
	// 23.05 reports a load of exactly zero through ubus (measured on the x86
	// stand) where /proc/loadavg does not. Taking that at face value would
	// replace a real reading with a flat line, so only a nonzero answer wins.
	if si.Load[0] > 0 {
		info.CPUPercent = loadToPercent(si.LoadAverage()[0])
	}
}

// cachedBoard reads `system board` once per process lifetime.
func (m *systemManager) cachedBoard(ctx context.Context) (ubus.Board, bool) {
	m.boardOnce.Do(func() {
		b, err := m.bus.Board(ctx)
		if err != nil {
			return
		}
		m.board, m.boardOK = b, true
	})
	return m.board, m.boardOK
}

// --- /proc helpers ---

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return int64(f)
}

// readMem returns used and total bytes from /proc/meminfo (used = total - available).
func readMem() (used, total int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var memTotal, memAvail int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		kb, _ := strconv.ParseInt(strings.Fields(strings.TrimSpace(val))[0], 10, 64)
		switch key {
		case "MemTotal":
			memTotal = kb * 1024
		case "MemAvailable":
			memAvail = kb * 1024
		}
	}
	return memTotal - memAvail, memTotal
}

// readLoadAsCPU approximates CPU usage from the 1-minute load average over the
// CPU count. Cheap and adequate for a dashboard; a true %busy would need two
// /proc/stat samples (dashboard milestone M2).
func readLoadAsCPU() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	load1, _ := strconv.ParseFloat(fields[0], 64)
	return loadToPercent(load1)
}

// loadToPercent spreads a 1-minute load average over the CPU count and caps it
// at 100: a load of 4 on two cores is a saturated router, not 200% of one.
func loadToPercent(load1 float64) float64 {
	n := runtime.NumCPU()
	if n == 0 {
		n = 1
	}
	pct := load1 / float64(n) * 100
	if pct > 100 {
		pct = 100
	}
	return pct
}

// --- egress / http helpers ---

// egressIP fetches our public IP as plain text via the given request function
// (swap in a tunnel-dialing client to learn the tunnel egress).
func egressIP(do func(*http.Request) (*http.Response, error)) string {
	for _, url := range []string{"http://ifconfig.me/ip", "http://api.ipify.org", "http://icanhazip.com"} {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "curl/8")
		resp, err := do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := strings.TrimSpace(string(b)); net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// egressVia resolves the public IP reached through dialer's tunnel, using a
// throwaway client whose idle connections are closed afterwards so repeated
// dashboard polls don't leak transports/goroutines.
func (m *systemManager) egressVia(d vpn.Dialer) string {
	client := tunnelClient(d)
	defer client.CloseIdleConnections()
	return m.egress(client.Do)
}

// tunnelClient builds an HTTP client whose connections dial through the tunnel.
func tunnelClient(d vpn.Dialer) *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}

// egressViaInterface resolves the public IP reached by binding outbound sockets
// to the named OS interface (the kernel-engine equivalent of egressVia: kernel
// tunnels have no Dialer, so we pin egress to awg0 instead). Returns "" if the
// interface can't be bound or no IP comes back.
func (m *systemManager) egressViaInterface(ifname string) string {
	dialer := &net.Dialer{
		Timeout: 15 * time.Second,
		Control: bindToDevice(ifname),
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}
	defer client.CloseIdleConnections()
	return m.egress(client.Do)
}

// validHost rejects shell-dangerous input for the ping exec (host or IP only).
func validHost(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 255 {
		return false
	}
	for _, r := range s {
		if !(r == '.' || r == '-' || r == ':' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
