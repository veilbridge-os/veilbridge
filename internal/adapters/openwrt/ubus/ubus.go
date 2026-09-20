// Package ubus is a typed client for OpenWrt's system bus.
//
// Why the CLI and not the socket: ubus speaks a binary blobmsg protocol over
// /var/run/ubus/ubus.sock, and talking it natively means carrying our own
// serialiser for a format whose only consumer is this package. The `ubus`
// binary is part of every OpenWrt image (it is how procd itself is driven),
// prints plain JSON with -S, and costs one fork per poll. If that fork ever
// shows up in a measurement on a 256 MB router, this is the one file that has
// to change: everything above it sees typed structs, not commands.
//
// The types here follow what real devices actually return. The fixtures in
// testdata/ were captured from two live stands — OpenWrt 23.05.5 (x86) and
// 25.12.5 (aarch64) — because the two branches do not agree: 25.12 adds
// release.firmware_url and release.builddate. Anything parsed here must
// survive both.
//
// The load average is NOT one of those differences, though an earlier note
// here said it was: the 23.05 fixture shows zeros only because that stand was
// idle when it was captured. Measured under load on 20.09.2026, both branches
// report the same fixed-point value /proc/loadavg does.
package ubus

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner is the single seam through which this package touches the OS, mirroring
// the one in the uci applier. Tests replace it; production uses runCommand.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// allowedCommands is the complete list of programs this package may execute.
// One entry, and adding a second one has to be a deliberate, reviewable act.
//
// The entry is an absolute path, not a name: a bare "ubus" would be resolved
// through PATH, and PATH is not ours to trust in a daemon that runs as root.
// Measured on both stands (23.05.5 and 25.12.5) the binary is /bin/ubus.
var allowedCommands = map[string]bool{ubusBin: true}

// ubusBin is where OpenWrt keeps the client. Both supported branches agree.
const ubusBin = "/bin/ubus"

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !allowedCommands[name] {
		return nil, fmt.Errorf("refusing to run %q: not in the adapter's allow-list", name)
	}
	var stdout, stderr strings.Builder
	// `name` is checked against allowedCommands above, so it is one of a fixed
	// set of literals; arguments are passed as a slice and never see a shell.
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// ubus reports failures on stderr with a nonzero status ("Command failed:
	// Not found", status 4 — measured on 25.12.5). stdout is then empty or
	// partial, so it must not be parsed as JSON.
	if err := cmd.Run(); err != nil {
		return nil, callError(args, err, stdout.String(), stderr.String())
	}
	return []byte(stdout.String()), nil
}

// callError builds the error for a failed ubus invocation. It is separate from
// runCommand so it can be tested on a machine that has no ubus at all, and it
// exists at all because the device's own words are the only diagnosis
// available on a router: "exit status 4" says nothing, "Command failed: Not
// found" says everything.
func callError(args []string, err error, stdout, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		return fmt.Errorf("ubus %s: %w (no message from ubus)", strings.Join(args, " "), err)
	}
	return fmt.Errorf("ubus %s: %w: %s", strings.Join(args, " "), err, msg)
}

// Client calls ubus objects. The zero value is not usable; use New.
type Client struct {
	run Runner
	// timeout bounds a single call. ubus itself can block forever waiting for
	// an object that never answers (a wedged netifd), and a dashboard poll
	// that never returns is how a panel stops responding.
	timeout time.Duration
}

// New builds a client that shells out to ubus.
func New() *Client { return &Client{run: runCommand, timeout: 10 * time.Second} }

// NewWithRunner builds a client over an injected runner. Tests use it to
// replay captured output instead of needing a router.
func NewWithRunner(r Runner) *Client { return &Client{run: r, timeout: 10 * time.Second} }

// Call invokes path.method and decodes the JSON reply into out. Pass a nil out
// to ignore the reply.
func (c *Client) Call(ctx context.Context, path, method string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Deliberately WITHOUT -S. The flag produces compact output, but measured
	// on 25.12.5 it also throws the failure message away: `ubus -S call nosuch
	// method` exits 4 with both stdout and stderr empty, while the same call
	// without -S prints "Command failed: Not found" on stderr. A few saved
	// bytes are not worth a silent failure on a device nobody can attach a
	// debugger to.
	raw, err := c.run(ctx, ubusBin, "call", path, method)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	// A method with no reply prints nothing at all. Decoding "" would fail
	// with a confusing EOF, so say what happened instead.
	if len(strings.TrimSpace(string(raw))) == 0 {
		return fmt.Errorf("ubus %s %s: empty reply where an object was expected", path, method)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("ubus %s %s: decode reply: %w", path, method, err)
	}
	return nil
}

// --- system board ---

// Release is the distribution block of `ubus call system board`. Fields that
// only exist on newer branches (FirmwareURL, BuildDate on 25.12) are simply
// empty on older ones — a missing field is normal, not an error.
type Release struct {
	Distribution string `json:"distribution"`
	Version      string `json:"version"`
	Revision     string `json:"revision"`
	Target       string `json:"target"`
	Description  string `json:"description"`
	FirmwareURL  string `json:"firmware_url,omitempty"`
	BuildDate    string `json:"builddate,omitempty"`
}

// Board is what the device says about itself: the model line a dashboard shows
// and the target an image has to be built for.
type Board struct {
	Kernel     string  `json:"kernel"`
	Hostname   string  `json:"hostname"`
	System     string  `json:"system"`
	Model      string  `json:"model"`
	BoardName  string  `json:"board_name"`
	RootfsType string  `json:"rootfs_type"`
	Release    Release `json:"release"`
}

// Board reads `ubus call system board`.
func (c *Client) Board(ctx context.Context) (Board, error) {
	var b Board
	if err := c.Call(ctx, "system", "board", &b); err != nil {
		return Board{}, err
	}
	return b, nil
}

// --- system info ---

// Memory is the memory block of `ubus call system info`, in bytes.
type Memory struct {
	Total     int64 `json:"total"`
	Free      int64 `json:"free"`
	Shared    int64 `json:"shared"`
	Buffered  int64 `json:"buffered"`
	Available int64 `json:"available"`
	Cached    int64 `json:"cached"`
}

// Swap is the swap block of system info, in bytes. It is all zeros on a stock
// router, and that is worth reporting rather than hiding: a device that
// started swapping is a device in trouble.
type Swap struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

// Filesystem is one of the root/tmp blocks of system info, in kilobytes — the
// units procd inherits from statfs, not the bytes used for memory.
type Filesystem struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
	Used  int64 `json:"used"`
	Avail int64 `json:"avail"`
}

// Info is `ubus call system info`.
type Info struct {
	Localtime int64 `json:"localtime"`
	Uptime    int64 `json:"uptime"`
	// Load is the raw triple as ubus reports it: fixed point, scaled by 65536
	// (a load of 1.0 is 65536). Use LoadAverage for numbers a human reads.
	Load   [3]int64   `json:"load"`
	Memory Memory     `json:"memory"`
	Swap   Swap       `json:"swap"`
	Root   Filesystem `json:"root"`
	Tmp    Filesystem `json:"tmp"`
}

// loadScale is the fixed-point divisor of the kernel's sysinfo load averages,
// which ubus passes through unchanged (SI_LOAD_SHIFT = 16).
const loadScale = 65536.0

// LoadAverage converts the raw fixed-point triple into 1/5/15-minute averages.
func (i Info) LoadAverage() [3]float64 {
	return [3]float64{
		float64(i.Load[0]) / loadScale,
		float64(i.Load[1]) / loadScale,
		float64(i.Load[2]) / loadScale,
	}
}

// SystemInfo reads `ubus call system info`.
func (c *Client) SystemInfo(ctx context.Context) (Info, error) {
	var i Info
	if err := c.Call(ctx, "system", "info", &i); err != nil {
		return Info{}, err
	}
	return i, nil
}

// --- network.interface dump ---

// Address is an assigned address with its prefix length.
type Address struct {
	Address string `json:"address"`
	Mask    int    `json:"mask"`
}

// Route is one route netifd installed for an interface. Nexthop is empty for
// on-link routes.
type Route struct {
	Target  string `json:"target"`
	Mask    int    `json:"mask"`
	Nexthop string `json:"nexthop"`
	Source  string `json:"source,omitempty"`
}

// Interface is one entry of `ubus call network.interface dump`.
//
// Device vs L3Device is not a duplicate: for a bridge or a tunnel they differ,
// and the one traffic actually leaves through is L3Device. Anything that binds
// a socket or reads counters wants L3Device.
type Interface struct {
	Name      string `json:"interface"`
	Up        bool   `json:"up"`
	Pending   bool   `json:"pending"`
	Available bool   `json:"available"`
	Autostart bool   `json:"autostart"`
	Dynamic   bool   `json:"dynamic"`
	Uptime    int64  `json:"uptime"`
	Proto     string `json:"proto"`
	Device    string `json:"device"`
	L3Device  string `json:"l3_device"`

	IPv4Address []Address `json:"ipv4-address"`
	IPv6Address []Address `json:"ipv6-address"`
	Route       []Route   `json:"route"`
	DNSServer   []string  `json:"dns-server"`
}

// DefaultRoute reports this interface's IPv4 gateway, if it has one. A default
// route is target 0.0.0.0/0 — the mask matters: without it a host route to
// 0.0.0.0 would read as a default gateway.
func (i Interface) DefaultRoute() (string, bool) {
	return i.defaultVia("0.0.0.0")
}

// DefaultRoute6 reports the IPv6 gateway. Separate from DefaultRoute rather
// than merged with it because an interface can have both, and a caller trying
// to reach an IPv4 address is not helped by an fe80:: nexthop.
func (i Interface) DefaultRoute6() (string, bool) {
	return i.defaultVia("::")
}

func (i Interface) defaultVia(unspecified string) (string, bool) {
	for _, r := range i.Route {
		if r.Target == unspecified && r.Mask == 0 && r.Nexthop != "" {
			return r.Nexthop, true
		}
	}
	return "", false
}

// Interfaces reads `ubus call network.interface dump`.
func (c *Client) Interfaces(ctx context.Context) ([]Interface, error) {
	var reply struct {
		Interface []Interface `json:"interface"`
	}
	if err := c.Call(ctx, "network.interface", "dump", &reply); err != nil {
		return nil, err
	}
	return reply.Interface, nil
}
