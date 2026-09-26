package openwrt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// The shape of a snapshot is the one the stands produce: `tar czf - -C /etc
// config`, so members are named config/<file> and there is a directory entry.
func snapshotArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "config/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: "config/" + name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type emergencyFixture struct {
	e    *emergency
	root string
	out  *bytes.Buffer
	runs [][]string
}

// newEmergencyFixture lays out /etc/config and the snapshot directory in a
// temporary root, and records every command instead of running it. The
// applier takes its "current" snapshot with a real tar of that root, so the
// undo point is what a device would really keep.
func newEmergencyFixture(t *testing.T, now map[string]string) *emergencyFixture {
	t.Helper()
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	snaps := filepath.Join(root, "snapshots")
	for _, d := range []string{cfg, snaps} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range now {
		if err := os.WriteFile(filepath.Join(cfg, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := &emergencyFixture{root: root, out: &bytes.Buffer{}}
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.runs = append(f.runs, append([]string{name}, args...))
		if name == "tar" {
			return snapshotArchive(t, readConfigDir(t, cfg)), nil
		}
		return nil, nil
	}
	clock := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	a := &uciApplier{
		run: run, configRoot: root, configDir: "config", snapshotDir: snaps,
		keepSnapshots: 5, now: func() time.Time { return clock },
		journal: newApplyJournal(filepath.Join(root, "pending-apply.json")),
	}
	f.e = &emergency{
		configDir: cfg, snapshotDir: snaps, journal: a.journal, applier: a,
		run: run, out: f.out, now: func() time.Time { return clock },
	}
	return f
}

func readConfigDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func (f *emergencyFixture) point(t *testing.T, id string, files map[string]string) {
	t.Helper()
	path := filepath.Join(f.root, "snapshots", id+".tar.gz")
	if err := os.WriteFile(path, snapshotArchive(t, files), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *emergencyFixture) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, "config", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *emergencyFixture) ran(prefix string) bool {
	for _, r := range f.runs {
		if strings.HasPrefix(strings.Join(r, " "), prefix) {
			return true
		}
	}
	return false
}

// The router, measured: the newest snapshot equals the settings now (its
// change was undone by the watchdog), the one before it differs (its change
// was confirmed). The default is that one — the state before the last change
// that stuck — and only the panel's files come back.
func TestTheLastChangeThatStuckIsUndoneAndNothingElse(t *testing.T) {
	now := map[string]string{"network": "net-now", "firewall": "fw", "dhcp": "dhcp", "wireless": "wifi-now", "system": "sys-now"}
	f := newEmergencyFixture(t, now)
	f.point(t, "uci-20260926T160000Z", map[string]string{"network": "net-older", "firewall": "fw-older", "dhcp": "dhcp", "wireless": "wifi-old"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "net-before", "firewall": "fw", "dhcp": "dhcp", "wireless": "wifi-old", "system": "sys-old"})
	f.point(t, "uci-20260926T164000Z", map[string]string{"network": "net-now", "firewall": "fw", "dhcp": "dhcp", "wireless": "wifi-old"})

	if err := f.e.restore(RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := f.file(t, "network"); got != "net-before" {
		t.Errorf("network = %q, want the point before the last change that stuck", got)
	}
	if got := f.file(t, "firewall"); got != "fw" {
		t.Errorf("firewall = %q, it did not differ in the chosen point and must stay", got)
	}
	if f.file(t, "wireless") != "wifi-now" || f.file(t, "system") != "sys-now" {
		t.Error("a file the panel does not write was restored")
	}
	if !f.ran("/sbin/reload_config") {
		t.Error("the services were not told to reload")
	}
	// The state that was replaced is itself a restore point: undoing the
	// undo is the same command.
	undo := filepath.Join(f.root, "snapshots", "uci-20260926T180000Z.tar.gz")
	files, err := readSnapshotFiles(undo)
	if err != nil || string(files["network"].data) != "net-now" {
		t.Errorf("no undo point with the replaced settings: %v %v", files, err)
	}
	if !strings.Contains(f.out.String(), "-restore-point uci-20260926T180000Z") {
		t.Errorf("output does not say how to undo:\n%s", f.out)
	}
}

func TestAnExplicitPointGoesFurtherBack(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n3", "firewall": "f3", "dhcp": "d"})
	f.point(t, "uci-20260926T160000Z", map[string]string{"network": "n1", "firewall": "f1", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n2", "firewall": "f3", "dhcp": "d"})
	if err := f.e.restore(RestoreOptions{Point: "uci-20260926T160000Z"}); err != nil {
		t.Fatal(err)
	}
	if f.file(t, "network") != "n1" || f.file(t, "firewall") != "f1" {
		t.Errorf("network=%q firewall=%q", f.file(t, "network"), f.file(t, "firewall"))
	}
	if err := f.e.restore(RestoreOptions{Point: "uci-19990101T000000Z"}); err == nil {
		t.Error("an unknown point was accepted")
	}
}

func TestNothingToRestoreChangesNothing(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n", "firewall": "f", "dhcp": "d"})
	if err := f.e.restore(RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 0 {
		t.Errorf("commands ran with nothing to restore: %v", f.runs)
	}
	if !strings.Contains(f.out.String(), "Nothing to restore") {
		t.Errorf("output:\n%s", f.out)
	}
}

// D-78: the watchdog is about to undo the pending change by itself; racing it
// would let it put the whole of /etc/config back over the chosen point.
func TestAPendingChangeIsLeftToTheWatchdogUnlessForced(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n-now", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n-before", "firewall": "f", "dhcp": "d"})
	deadline := time.Date(2026, 9, 26, 18, 1, 30, 0, time.UTC)
	if err := f.e.journal.Save(core.ApplyRecord{SnapshotID: "uci-20260926T163000Z", Token: "x", Deadline: deadline}); err != nil {
		t.Fatal(err)
	}
	err := f.e.restore(RestoreOptions{})
	if !errors.Is(err, ErrChangePending) {
		t.Fatalf("err = %v, want ErrChangePending", err)
	}
	if f.file(t, "network") != "n-now" || len(f.runs) != 0 {
		t.Error("restored while a change was waiting for confirmation")
	}
	if !strings.Contains(f.out.String(), "1m30s") {
		t.Errorf("output does not say when the device undoes it:\n%s", f.out)
	}

	if err := f.e.restore(RestoreOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if f.file(t, "network") != "n-before" {
		t.Error("-force did not restore")
	}
	if _, pending, _ := f.e.journal.Load(); pending {
		t.Error("the pending transaction survived -force and would undo the restore at the next start")
	}
}

// Failsafe mode: the services are not running and reload fails. The files are
// back all the same, and the person is told to reboot — not given an error.
func TestAFailedReloadStillLeavesTheFilesBack(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n-now", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n-before", "firewall": "f", "dhcp": "d"})
	base := f.e.run
	f.e.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "/sbin/reload_config" {
			return nil, errors.New("ubus: not found")
		}
		return base(ctx, name, args...)
	}
	if err := f.e.restore(RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.file(t, "network") != "n-before" || !strings.Contains(f.out.String(), "reboot") {
		t.Errorf("network=%q output:\n%s", f.file(t, "network"), f.out)
	}
}

func TestTheListMarksWhatTheCommandWouldRestore(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T160000Z", map[string]string{"network": "n0", "firewall": "f0", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n1", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T164000Z", map[string]string{"network": "n", "firewall": "f", "dhcp": "d"})
	if err := os.WriteFile(filepath.Join(f.root, "snapshots", "uci-20260926T150000Z.tar.gz"), []byte("not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.e.restore(RestoreOptions{List: true}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	for _, want := range []string{
		"  uci-20260926T164000Z", "same as now",
		"* uci-20260926T163000Z", "network\n",
		"  uci-20260926T160000Z", "network, firewall",
		"uci-20260926T150000Z is unreadable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if len(f.runs) != 0 || f.file(t, "network") != "n" {
		t.Error("listing changed something")
	}
}

// Nothing in an archive may land anywhere but the three files.
func TestOnlyThePanelsFilesAreReadFromAnArchive(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, name := range []string{"config/../../etc/passwd", "config/network", "../network"} {
		body := "x-" + name
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "config/firewall", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"})
	_ = tw.Close()
	_ = zw.Close()
	path := filepath.Join(t.TempDir(), "uci-x.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := readSnapshotFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || string(files["network"].data) != "x-config/network" {
		t.Errorf("files = %v", files)
	}
}

// A draft left in the staging area would be committed by the next apply and
// bring back what the restore took away.
func TestAStagedDraftIsDroppedBeforeRestoring(t *testing.T) {
	f := newEmergencyFixture(t, map[string]string{"network": "n-now", "firewall": "f", "dhcp": "d"})
	f.point(t, "uci-20260926T163000Z", map[string]string{"network": "n-before", "firewall": "f", "dhcp": "d"})
	base := f.e.run
	stage := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "uci" && len(args) == 1 && args[0] == "changes" {
			f.runs = append(f.runs, []string{name, "changes"})
			return []byte("network.lan.ipaddr='192.0.2.1'\nfirewall.cfg0a1b2c='rule'\n"), nil
		}
		return base(ctx, name, args...)
	}
	f.e.run, f.e.applier.run = stage, stage
	if err := f.e.restore(RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if !f.ran("uci revert firewall") || !f.ran("uci revert network") {
		t.Errorf("draft not dropped: %v", f.runs)
	}
}
