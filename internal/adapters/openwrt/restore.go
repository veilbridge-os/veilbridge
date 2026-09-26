package openwrt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Emergency access (M3.5, #38, DESIGN D-77/D-78): a way back when a network
// change stuck and took the panel with it — confirmed from a path that still
// worked, say, and then the other path was gone.
//
// It is a command, not a mode of the daemon: it has to work with no panel, no
// network and no panel configuration — over ssh with a cable in a LAN port, on
// a serial or hypervisor console, or in OpenWrt's failsafe mode after
// `mount_root`. It uses the snapshots the apply transaction already keeps, one
// taken before every apply, and puts back only the files the panel writes.

// ErrChangePending is returned when an earlier change is still waiting for
// confirmation: the device is about to undo it by itself, and restoring now
// would race that (D-78). -force overrides.
var ErrChangePending = errors.New("a change is waiting for confirmation")

// RestoreOptions is what the command line asked for.
type RestoreOptions struct {
	// List only prints the restore points.
	List bool
	// Point restores that snapshot instead of the newest one that differs.
	Point string
	// Force restores even while a change waits for confirmation.
	Force bool
}

// restorePoint is one snapshot, read for the files the panel writes.
type restorePoint struct {
	id    string
	taken time.Time
	files map[string]restoredFile // by configuration name
	// differs names the configurations whose content is not what it is now.
	differs []string
}

type restoredFile struct {
	data []byte
	mode os.FileMode
}

// emergency is the restore procedure with its paths and seams, so a test can
// point it at a temporary directory.
type emergency struct {
	configDir   string
	snapshotDir string
	journal     *applyJournal
	applier     *uciApplier
	run         commandRunner
	out         io.Writer
	now         func() time.Time
}

func newEmergency(out io.Writer) *emergency {
	a := newUCIApplier()
	return &emergency{
		configDir:   filepath.Join(a.configRoot, a.configDir),
		snapshotDir: a.snapshotDir,
		journal:     a.journal,
		applier:     a,
		run:         a.run,
		out:         out,
		now:         time.Now,
	}
}

// EmergencyRestore runs the emergency command. The error is for the exit code;
// everything a person needs to read has been printed to out already.
func EmergencyRestore(opts RestoreOptions, out io.Writer) error {
	return newEmergency(out).restore(opts)
}

func (e *emergency) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(e.out, format, args...)
}

func (e *emergency) restore(opts RestoreOptions) error {
	points, err := e.points()
	if err != nil {
		return err
	}
	if opts.List {
		e.list(points)
		return nil
	}

	if rec, pending, err := e.journal.Load(); err != nil {
		e.printf("warning: %v\n", err)
	} else if pending && !opts.Force {
		e.printf("A change is waiting for confirmation. The device undoes it by itself at %s\n"+
			"(in %s) — wait for that, it is the safer way back.\n"+
			"To restore now anyway: veilbridged -restore-network -force\n",
			rec.Deadline.Local().Format("15:04:05"), e.until(rec.Deadline))
		return ErrChangePending
	}

	var chosen *restorePoint
	if opts.Point != "" {
		for i := range points {
			if points[i].id == opts.Point {
				chosen = &points[i]
			}
		}
		if chosen == nil {
			e.list(points)
			return fmt.Errorf("no restore point %q (the list is above)", opts.Point)
		}
	} else {
		for i := range points {
			if len(points[i].differs) > 0 {
				chosen = &points[i]
				break
			}
		}
		if chosen == nil {
			e.printf("Nothing to restore: the network settings are the same as before each of the last %d changes.\n", len(points))
			if len(points) == 0 {
				e.printf("(There are no restore points yet: the panel takes one before every change it applies.)\n")
			}
			return nil
		}
	}
	if len(chosen.differs) == 0 {
		e.printf("Nothing to restore: %s has the same network settings as now.\n", chosen.id)
		return nil
	}

	e.printf("Restoring %s from %s (taken %s).\n",
		strings.Join(chosen.differs, ", "), chosen.id, chosen.taken.Local().Format("2006-01-02 15:04:05"))

	// The state being replaced becomes a restore point itself, so this is
	// undone by the same command. Not fatal: a full overlay must not stand
	// between a person and their router.
	undo := ""
	if snap, err := e.applier.Snapshot(); err != nil {
		e.printf("warning: could not keep the current settings as a restore point: %v\n", err)
	} else {
		undo = snap.ID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A draft left in the staging area would be committed by the next apply
	// and quietly bring back what is being removed here.
	if err := e.applier.dropStaged(ctx); err != nil {
		e.printf("warning: %v\n", err)
	}
	for _, c := range chosen.differs {
		f, ok := chosen.files[c]
		if !ok {
			e.printf("  %s: not in the restore point, left as it is\n", c)
			continue
		}
		if err := writeAtomic(filepath.Join(e.configDir, c), f.data, f.mode); err != nil {
			return fmt.Errorf("restore %s: %w", c, err)
		}
		e.printf("  %s: restored\n", c)
	}
	if opts.Force {
		// The pending transaction would otherwise undo the whole of
		// /etc/config over the point chosen here, at the next start (D-78).
		if err := e.journal.Clear(); err != nil {
			e.printf("warning: %v\n", err)
		} else {
			e.printf("The change that was waiting for confirmation is dropped; restart the panel: /etc/init.d/veilbridge restart\n")
		}
	}

	if _, err := e.run(ctx, "/sbin/reload_config"); err != nil {
		// Expected in failsafe mode, where the services are not running.
		e.printf("The files are back, but the services could not be told to reload (%v).\nReboot to apply them: reboot\n", err)
		return nil
	}
	e.printf("Done: the network service reloaded the restored settings.\n")
	if undo != "" {
		e.printf("To undo this: veilbridged -restore-point %s\n", undo)
	}
	return nil
}

func (e *emergency) until(t time.Time) string {
	d := t.Sub(e.now()).Round(time.Second)
	if d < 0 {
		return "any moment"
	}
	return d.String()
}

func (e *emergency) list(points []restorePoint) {
	if len(points) == 0 {
		e.printf("No restore points yet: the panel takes one before every change it applies.\n")
		return
	}
	e.printf("Restore points, newest first (what differs from the settings now):\n")
	chosen := false
	for _, p := range points {
		mark := "  "
		if !chosen && len(p.differs) > 0 {
			mark, chosen = "* ", true
		}
		what := "same as now"
		if len(p.differs) > 0 {
			what = strings.Join(p.differs, ", ")
		}
		e.printf("%s%s  %s  %s\n", mark, p.id, p.taken.Local().Format("2006-01-02 15:04:05"), what)
	}
	if chosen {
		e.printf("* = what veilbridged -restore-network puts back\n")
	}
}

// points reads every snapshot, newest first, and compares the files the panel
// writes with the ones in place now.
func (e *emergency) points() ([]restorePoint, error) {
	entries, err := os.ReadDir(e.snapshotDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read restore points: %w", err)
	}
	var names []string
	for _, en := range entries {
		if !en.IsDir() && strings.HasPrefix(en.Name(), "uci-") && strings.HasSuffix(en.Name(), ".tar.gz") {
			names = append(names, en.Name())
		}
	}
	// The ID is a sortable UTC timestamp.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	current := map[string][]byte{}
	for _, c := range writtenConfigs {
		if b, err := os.ReadFile(filepath.Join(e.configDir, c)); err == nil {
			current[c] = b
		}
	}
	var out []restorePoint
	for _, name := range names {
		id := strings.TrimSuffix(name, ".tar.gz")
		files, err := readSnapshotFiles(filepath.Join(e.snapshotDir, name))
		if err != nil {
			e.printf("warning: restore point %s is unreadable, skipped: %v\n", id, err)
			continue
		}
		p := restorePoint{id: id, files: files}
		if t, err := time.Parse("20060102T150405Z", strings.TrimPrefix(id, "uci-")); err == nil {
			p.taken = t
		}
		for _, c := range writtenConfigs {
			f, inPoint := files[c]
			now, inPlace := current[c]
			if inPoint != inPlace || (inPoint && !bytes.Equal(f.data, now)) {
				p.differs = append(p.differs, c)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// readSnapshotFiles takes the panel's files out of a snapshot archive. It is
// read here rather than unpacked with tar: only three members are wanted, and
// nothing in the archive may land anywhere but where this code puts it.
func readSnapshotFiles(path string) (map[string]restoredFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, c := range writtenConfigs {
		want["config/"+c] = true
	}
	files := map[string]restoredFile{}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag != tar.TypeReg || !want[name] {
			continue
		}
		// A configuration file is a few kilobytes; bound the read anyway.
		data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		files[strings.TrimPrefix(name, "config/")] = restoredFile{data: data, mode: os.FileMode(h.Mode).Perm()}
	}
	return files, nil
}

// writeAtomic replaces a file so that a power cut leaves either the old or the
// new one, never half of each.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".vb-restore")
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
