package openwrt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// uciApplier is the OpenWrt implementation of core.ConfigApplier: it turns the
// abstract Snapshot/Commit/Revert of the transaction model (see core/apply.go)
// into uci operations. Everything OS-specific about applying configuration
// lives here and nowhere else — that is NFR-1.
//
// Why a tarball of /etc/config and not `uci export`:
//
//   - `uci export` prints what uci can parse. A file it chokes on is exactly
//     the file most likely to be involved in a broken apply, and it would be
//     silently missing from the snapshot.
//   - Restoring by `uci import` means parsing, merging and re-serialising while
//     the machine may already be half-broken. Unpacking a tarball is a write,
//     not an interpretation.
//   - It is small: measured on an OpenWrt 23.05 stand, /etc/config is 2421
//     bytes packed, so keeping several snapshots costs nothing.
type uciApplier struct {
	run commandRunner

	// configRoot/configDir together address /etc/config: the tar is taken as
	// `-C <configRoot> <configDir>` so restoring cannot escape the directory.
	configRoot string
	configDir  string

	// snapshotDir persists snapshots, because the watchdog is only as good as
	// its ability to revert after the daemon itself restarts.
	snapshotDir string
	// keepSnapshots bounds the directory on a device with ~40 MB of overlay.
	keepSnapshots int

	now func() time.Time
}

// commandRunner is the single seam through which this package touches the OS.
// Tests replace it; production uses runCommand. Keeping it one function makes
// "which OS calls does VeilBridge make" a question with a grep-able answer.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// allowedCommands is the complete list of programs this package may execute.
// The seam takes the program name as a value, which is what makes it testable
// — and what would otherwise make it a place where a future caller could shell
// out to anything. An allow-list keeps both properties: injectable in tests,
// and impossible to widen by accident. Adding an entry here is a deliberate,
// reviewable act.
var allowedCommands = map[string]bool{
	"uci":                 true,
	"tar":                 true,
	"/sbin/reload_config": true,
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !allowedCommands[name] {
		return nil, fmt.Errorf("refusing to run %q: not in the adapter's allow-list", name)
	}
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	// `name` is checked against allowedCommands above, so it is one of a fixed
	// set of literals; arguments are passed as a slice, never through a shell.
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func newUCIApplier() *uciApplier {
	return &uciApplier{
		run:           runCommand,
		configRoot:    "/etc",
		configDir:     "config",
		snapshotDir:   "/etc/veilbridge/snapshots",
		keepSnapshots: 5,
		now:           time.Now,
	}
}

// Snapshot captures /etc/config as it is right now.
func (u *uciApplier) Snapshot() (core.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	payload, err := u.run(ctx, "tar", "czf", "-", "-C", u.configRoot, u.configDir)
	if err != nil {
		return core.Snapshot{}, fmt.Errorf("snapshot /etc/config: %w", err)
	}
	if len(payload) == 0 {
		// An empty archive would "restore" nothing while reporting success —
		// the worst possible outcome for a safety net.
		return core.Snapshot{}, fmt.Errorf("snapshot /etc/config: archive is empty")
	}

	snap := core.Snapshot{
		ID:      "uci-" + u.now().UTC().Format("20060102T150405Z"),
		Taken:   u.now().UTC(),
		Payload: payload,
	}
	if err := u.persist(snap); err != nil {
		// Persisting is best-effort: an in-memory snapshot still reverts a
		// live daemon, which is the common case. Say so instead of failing.
		return snap, nil
	}
	return snap, nil
}

// Commit makes the staged uci changes live and tells the services to reload.
func (u *uciApplier) Commit() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := u.run(ctx, "uci", "commit"); err != nil {
		return fmt.Errorf("uci commit: %w", err)
	}
	// reload_config is the OpenWrt way to let every service pick up its own
	// config (it walks ucitrack). Restarting services by hand from here would
	// duplicate that knowledge and get it wrong.
	if _, err := u.run(ctx, "/sbin/reload_config"); err != nil {
		return fmt.Errorf("reload_config after commit: %w", err)
	}
	return nil
}

// Revert restores a snapshot. This runs when the configuration has probably
// already broken something, so it does the least clever thing possible: drop
// anything still staged, unpack the files, reload.
func (u *uciApplier) Revert(snap core.Snapshot) error {
	if len(snap.Payload) == 0 {
		return fmt.Errorf("revert: snapshot %q carries no data", snap.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Staged changes survive in /tmp/.uci and would be committed by the next
	// commit, quietly re-applying what we are undoing right now.
	if err := u.dropStaged(ctx); err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "veilbridge-revert-*.tar.gz")
	if err != nil {
		return fmt.Errorf("revert: temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(snap.Payload); err != nil {
		tmp.Close()
		return fmt.Errorf("revert: write archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("revert: close archive: %w", err)
	}

	if _, err := u.run(ctx, "tar", "xzf", tmp.Name(), "-C", u.configRoot); err != nil {
		return fmt.Errorf("revert: unpack snapshot %s: %w", snap.ID, err)
	}
	if _, err := u.run(ctx, "/sbin/reload_config"); err != nil {
		return fmt.Errorf("revert: reload_config after restoring %s: %w", snap.ID, err)
	}
	return nil
}

// dropStaged discards uncommitted uci changes. `uci revert` needs a package
// name (verified on the stand: the usage line is `revert <config>[.<section>…]`),
// so the staged packages are read from `uci changes` first.
func (u *uciApplier) dropStaged(ctx context.Context) error {
	out, err := u.run(ctx, "uci", "changes")
	if err != nil {
		return fmt.Errorf("revert: list staged changes: %w", err)
	}
	for _, pkg := range stagedPackages(string(out)) {
		if _, err := u.run(ctx, "uci", "revert", pkg); err != nil {
			return fmt.Errorf("revert: drop staged changes in %q: %w", pkg, err)
		}
	}
	return nil
}

// stagedPackages extracts package names from `uci changes` output, whose lines
// look like `-network.wan` or `network.lan.ipaddr='192.168.9.1'`.
func stagedPackages(out string) []string {
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line == "" {
			continue
		}
		name, _, found := strings.Cut(line, ".")
		if !found || name == "" {
			continue
		}
		seen[name] = true
	}
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	// Sorted so the command sequence is deterministic and testable.
	sort.Strings(pkgs)
	return pkgs
}

// persist writes the snapshot next to the config and prunes old ones.
func (u *uciApplier) persist(snap core.Snapshot) error {
	if u.snapshotDir == "" {
		return nil
	}
	if err := os.MkdirAll(u.snapshotDir, 0o700); err != nil {
		return err
	}
	// The ID is generated here, but a file path built from a struct field is
	// worth one cheap check: a snapshot named "../../etc/passwd" must never
	// become a write outside the snapshot directory.
	if snap.ID == "" || strings.ContainsAny(snap.ID, "/\\") || strings.Contains(snap.ID, "..") {
		return fmt.Errorf("refusing to persist snapshot with unsafe id %q", snap.ID)
	}
	path := filepath.Join(u.snapshotDir, snap.ID+".tar.gz")
	if err := os.WriteFile(path, snap.Payload, 0o600); err != nil {
		return err
	}
	return u.prune()
}

// prune keeps the newest keepSnapshots archives. Overlay space on a router is
// measured in tens of megabytes, so an unbounded directory is a real risk.
func (u *uciApplier) prune() error {
	entries, err := os.ReadDir(u.snapshotDir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "uci-") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= u.keepSnapshots {
		return nil
	}
	// The ID is a sortable UTC timestamp, so lexical order is chronological.
	sort.Strings(names)
	for _, old := range names[:len(names)-u.keepSnapshots] {
		if err := os.Remove(filepath.Join(u.snapshotDir, old)); err != nil {
			// Worth reporting: a snapshot directory that cannot be pruned
			// fills the overlay, and a full overlay breaks the next snapshot.
			return fmt.Errorf("prune old snapshot %s: %w", old, err)
		}
	}
	return nil
}
