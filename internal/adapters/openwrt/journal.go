package openwrt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// applyJournal is the on-disk record of a transaction that is live but not yet
// confirmed. It exists because the watchdog in core runs in this process, and
// the changes most worth guarding are exactly the ones that can take the
// process down with them — a reboot, an OOM, a service restart triggered by the
// new configuration.
//
// It is a single small file, rewritten atomically: a half-written journal would
// be worse than none, because startup would either skip a needed revert or
// revert with a corrupted snapshot id.
type applyJournal struct {
	path string
}

func newApplyJournal(path string) *applyJournal { return &applyJournal{path: path} }

func (j *applyJournal) Save(rec core.ApplyRecord) error {
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return fmt.Errorf("journal dir: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode pending apply: %w", err)
	}

	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write pending apply: %w", err)
	}
	// Rename is the atomic step: either the old file or the complete new one.
	if err := os.Rename(tmp, j.path); err != nil {
		return fmt.Errorf("commit pending apply: %w", err)
	}
	return nil
}

func (j *applyJournal) Load() (core.ApplyRecord, bool, error) {
	data, err := os.ReadFile(j.path)
	if os.IsNotExist(err) {
		return core.ApplyRecord{}, false, nil
	}
	if err != nil {
		return core.ApplyRecord{}, false, fmt.Errorf("read pending apply: %w", err)
	}

	var rec core.ApplyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		// A corrupted journal must not be silently ignored: it means a
		// transaction may be live with nothing watching it.
		return core.ApplyRecord{}, false, fmt.Errorf("pending apply file %s is unreadable: %w", j.path, err)
	}
	if rec.SnapshotID == "" {
		return core.ApplyRecord{}, false, fmt.Errorf("pending apply file %s names no snapshot", j.path)
	}
	return rec, true, nil
}

func (j *applyJournal) Clear() error {
	if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear pending apply: %w", err)
	}
	return nil
}

// LoadSnapshot reads back a snapshot written by Snapshot(). It is what makes
// recovery after a restart possible: the payload lives on disk, not in the
// memory of a process that may be gone.
func (u *uciApplier) LoadSnapshot(id string) (core.Snapshot, error) {
	// The id comes from a file written earlier, so treat it as untrusted
	// input before it becomes a path.
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return core.Snapshot{}, fmt.Errorf("refusing to load snapshot with unsafe id %q", id)
	}
	path := filepath.Join(u.snapshotDir, id+".tar.gz")
	payload, err := os.ReadFile(path)
	if err != nil {
		return core.Snapshot{}, fmt.Errorf("load snapshot %s: %w", id, err)
	}
	if len(payload) == 0 {
		return core.Snapshot{}, fmt.Errorf("load snapshot %s: file is empty", id)
	}
	return core.Snapshot{ID: id, Payload: payload}, nil
}
