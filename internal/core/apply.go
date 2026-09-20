package core

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Applying a config change to a router can cut the wire you are sitting on: a
// wrong WAN setting, a firewall rule, a changed LAN address. On a VM that costs
// a hypervisor console; on a router in another room it costs a walk and a reset
// button. So VeilBridge never just writes and hopes — every change goes through
// Snapshot → Apply → Confirm, and silence means revert. See roadmap M1.3 (Q11).
//
// The state machine lives here, in core, and knows nothing about uci, nft or
// procd: it only knows that somebody can take a snapshot, make staged changes
// live, and put a snapshot back. That is the whole reason NFR-1 holds — the OS
// specifics stay behind ConfigApplier, in the adapter.

// Snapshot is an opaque, adapter-owned capture of the configuration as it was
// before a change. Core never looks inside Payload; it only hands it back to
// the adapter on revert.
type Snapshot struct {
	// ID identifies this snapshot for logs and for the revert path.
	ID string `json:"id"`
	// Taken is when the configuration was captured.
	Taken time.Time `json:"taken"`
	// Payload is adapter-private (e.g. a uci export). Opaque to core.
	Payload []byte `json:"-"`
}

// ConfigApplier is implemented by the adapter. Three operations, deliberately
// no more: anything richer would drag OS concepts into core.
type ConfigApplier interface {
	// Snapshot captures the current configuration so it can be restored.
	Snapshot() (Snapshot, error)
	// Commit makes the staged changes live. Staging is the manager's job
	// (e.g. uci set …) — Commit is the single moment things start to apply.
	Commit() error
	// Revert restores a snapshot taken earlier. It must be usable when the
	// machine is in a half-broken state, because that is exactly when it runs.
	Revert(Snapshot) error
}

// ApplyRecord is what must outlive the daemon: enough to undo a change nobody
// confirmed. The payload is not here — it belongs to the adapter, which stores
// snapshots where they survive a restart.
type ApplyRecord struct {
	SnapshotID string    `json:"snapshot_id"`
	Token      string    `json:"token"`
	Deadline   time.Time `json:"deadline"`
}

// ApplyJournal persists the pending transaction. Without it the watchdog only
// holds while this process lives — and a config change that kills the network
// often takes the daemon with it (a restart, an OOM, a reboot caused by the
// very change being tested).
type ApplyJournal interface {
	Save(ApplyRecord) error
	Load() (ApplyRecord, bool, error)
	Clear() error
}

// SnapshotLoader fetches a snapshot the coordinator did not take itself — the
// one from before a restart.
type SnapshotLoader interface {
	LoadSnapshot(id string) (Snapshot, error)
}

// ApplyPhase is the observable state of a transaction. It is what the UI shows
// in the apply-bar and what the API returns.
type ApplyPhase string

const (
	// PhaseIdle: no transaction in flight.
	PhaseIdle ApplyPhase = "idle"
	// PhaseAwaitingConfirm: changes are live and the watchdog is ticking.
	PhaseAwaitingConfirm ApplyPhase = "awaiting_confirm"
	// PhaseConfirmed: a human confirmed the panel is still reachable.
	PhaseConfirmed ApplyPhase = "confirmed"
	// PhaseReverted: nobody confirmed in time, the snapshot was restored.
	PhaseReverted ApplyPhase = "reverted"
	// PhaseRevertFailed: the revert itself failed. This is the state that
	// must never be hidden — it means the device may need physical access.
	PhaseRevertFailed ApplyPhase = "revert_failed"
)

// Errors returned by ApplyCoordinator. They are values, not strings, because
// the API layer maps them to status codes and the UI to different screens.
var (
	// ErrApplyInFlight: a second apply was attempted while one is pending.
	ErrApplyInFlight = errors.New("veilbridge: an apply is already awaiting confirmation")
	// ErrNoApplyInFlight: confirm arrived with nothing to confirm (late
	// confirm after an auto-revert looks exactly like this).
	ErrNoApplyInFlight = errors.New("veilbridge: no apply is awaiting confirmation")
	// ErrWrongToken: confirm carried a token from another transaction.
	ErrWrongToken = errors.New("veilbridge: this token does not match the pending apply")
)

// ApplyState is a snapshot of the coordinator for the API and the UI.
type ApplyState struct {
	Phase ApplyPhase `json:"phase"`
	// Token must be echoed back to Confirm. Empty unless awaiting confirm.
	Token string `json:"token,omitempty"`
	// Deadline is when the automatic revert fires. omitzero, not omitempty:
	// time.Time is a struct, so omitempty would never drop it and the UI would
	// receive a meaningless "0001-01-01T00:00:00Z" while idle.
	Deadline time.Time `json:"deadline,omitzero"`
	// SnapshotID ties the transaction to its snapshot in the logs.
	SnapshotID string `json:"snapshot_id,omitempty"`
	// Err is the last failure, if any (revert failures live here).
	Err string `json:"error,omitempty"`
}

// timer is the slice of time.Timer the coordinator needs. Tests inject a fake
// one instead of sleeping: a watchdog verified by waiting is a watchdog tested
// once and flaky forever.
type timer interface{ Stop() bool }

// ApplyCoordinator runs one transaction at a time and owns the watchdog.
//
// One at a time is not a simplification, it is the safety property: two
// overlapping applies mean two snapshots, and reverting to the older one would
// silently undo a change somebody already confirmed.
type ApplyCoordinator struct {
	applier ConfigApplier

	// afterFunc is time.AfterFunc by default; tests replace it.
	afterFunc func(time.Duration, func()) timer
	// now is time.Now by default; tests replace it.
	now func() time.Time
	// newToken produces the confirm token; tests replace it.
	newToken func() string

	// journal is optional: without one the coordinator still works, it just
	// cannot survive its own process dying.
	journal ApplyJournal

	mu       sync.Mutex
	phase    ApplyPhase
	token    string
	deadline time.Time
	snapshot Snapshot
	watchdog timer
	lastErr  error
}

// WithJournal makes a pending transaction survive a restart of the daemon.
func (c *ApplyCoordinator) WithJournal(j ApplyJournal) *ApplyCoordinator {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.journal = j
	return c
}

// RecoverPending undoes a transaction that was still awaiting confirmation when
// the daemon stopped, and reports whether anything was rolled back.
//
// The rule is deliberately one-sided: an unconfirmed change is reverted even if
// its deadline had not expired. The daemon disappearing inside the confirmation
// window is itself evidence that something went wrong, and the operator can
// always apply again — while the opposite mistake leaves a router running a
// configuration nobody ever confirmed.
func (c *ApplyCoordinator) RecoverPending(loader SnapshotLoader) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.journal == nil {
		return false, nil
	}
	rec, found, err := c.journal.Load()
	if err != nil {
		return false, fmt.Errorf("read pending apply: %w", err)
	}
	if !found {
		return false, nil
	}

	snap, err := loader.LoadSnapshot(rec.SnapshotID)
	if err != nil {
		c.phase = PhaseRevertFailed
		c.lastErr = fmt.Errorf("pending apply %s cannot be undone, its snapshot is gone: %w", rec.SnapshotID, err)
		return false, c.lastErr
	}
	if err := c.applier.Revert(snap); err != nil {
		c.phase = PhaseRevertFailed
		c.lastErr = fmt.Errorf("undo pending apply %s after restart: %w", rec.SnapshotID, err)
		return false, c.lastErr
	}
	if err := c.journal.Clear(); err != nil {
		return true, fmt.Errorf("pending apply undone but the journal was not cleared: %w", err)
	}
	c.phase = PhaseReverted
	return true, nil
}

// NewApplyCoordinator builds a coordinator around an adapter's applier.
func NewApplyCoordinator(applier ConfigApplier) *ApplyCoordinator {
	return &ApplyCoordinator{
		applier:   applier,
		afterFunc: func(d time.Duration, f func()) timer { return time.AfterFunc(d, f) },
		now:       time.Now,
		newToken:  func() string { return fmt.Sprintf("apply-%d", time.Now().UnixNano()) },
		phase:     PhaseIdle,
	}
}

// Apply snapshots the current configuration, commits the staged changes and
// starts the watchdog. The caller is expected to come back with Confirm before
// timeout; if it does not — because the change cut its own connection — the
// snapshot is restored automatically.
//
// Order matters and is not negotiable: snapshot first. A commit that succeeds
// with no snapshot behind it is an unrevertable change.
func (c *ApplyCoordinator) Apply(timeout time.Duration) (ApplyState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase == PhaseAwaitingConfirm {
		return c.stateLocked(), ErrApplyInFlight
	}

	snap, err := c.applier.Snapshot()
	if err != nil {
		c.phase = PhaseIdle
		c.lastErr = fmt.Errorf("snapshot before apply: %w", err)
		return c.stateLocked(), c.lastErr
	}

	if err := c.applier.Commit(); err != nil {
		// Commit failed, so the change may be half-applied: put the snapshot
		// back right now rather than leaving the device in between.
		revertErr := c.applier.Revert(snap)
		c.snapshot = snap
		if revertErr != nil {
			c.phase = PhaseRevertFailed
			c.lastErr = fmt.Errorf("commit failed (%v) and revert failed too: %w", err, revertErr)
			return c.stateLocked(), c.lastErr
		}
		c.phase = PhaseReverted
		c.lastErr = fmt.Errorf("commit failed, configuration restored: %w", err)
		return c.stateLocked(), c.lastErr
	}

	c.phase = PhaseAwaitingConfirm
	c.token = c.newToken()
	c.snapshot = snap
	c.deadline = c.now().Add(timeout)
	c.lastErr = nil
	token := c.token

	if c.journal != nil {
		rec := ApplyRecord{SnapshotID: snap.ID, Token: token, Deadline: c.deadline}
		if err := c.journal.Save(rec); err != nil {
			// The change is already live, so refusing now would be a lie.
			// Report that the safety net is thinner than advertised instead.
			c.lastErr = fmt.Errorf("apply is live but not journalled, a restart will not undo it: %w", err)
		}
	}
	c.watchdog = c.afterFunc(timeout, func() { c.autoRevert(token) })

	return c.stateLocked(), nil
}

// Confirm fixes the change: the panel is still reachable, so the watchdog is
// cancelled and the snapshot dropped.
func (c *ApplyCoordinator) Confirm(token string) (ApplyState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase != PhaseAwaitingConfirm {
		return c.stateLocked(), ErrNoApplyInFlight
	}
	if token != c.token {
		return c.stateLocked(), ErrWrongToken
	}

	if c.watchdog != nil {
		c.watchdog.Stop()
		c.watchdog = nil
	}
	c.phase = PhaseConfirmed
	c.token = ""
	c.deadline = time.Time{}
	c.snapshot = Snapshot{}
	c.clearJournalLocked()
	return c.stateLocked(), nil
}

// Revert restores the snapshot immediately — the "undo" button, used when the
// human can still see the panel and does not like what happened.
func (c *ApplyCoordinator) Revert() (ApplyState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase != PhaseAwaitingConfirm {
		return c.stateLocked(), ErrNoApplyInFlight
	}
	if c.watchdog != nil {
		c.watchdog.Stop()
		c.watchdog = nil
	}
	c.revertLocked()
	return c.stateLocked(), c.lastErr
}

// State reports the current phase for the API and the apply-bar.
func (c *ApplyCoordinator) State() ApplyState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

// autoRevert is the watchdog firing. It carries the token of the transaction it
// was started for, so a timer that fires just as a confirm lands cannot revert
// the next transaction.
func (c *ApplyCoordinator) autoRevert(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.phase != PhaseAwaitingConfirm || token != c.token {
		return
	}
	c.watchdog = nil
	c.revertLocked()
}

func (c *ApplyCoordinator) revertLocked() {
	snap := c.snapshot
	err := c.applier.Revert(snap)
	c.token = ""
	c.deadline = time.Time{}
	if err != nil {
		// Do not pretend this is recoverable: the device is now running a
		// configuration nobody chose, and someone may have to walk to it.
		c.phase = PhaseRevertFailed
		c.lastErr = fmt.Errorf("automatic revert to snapshot %s failed: %w", snap.ID, err)
		return
	}
	c.phase = PhaseReverted
	c.lastErr = nil
	c.snapshot = Snapshot{}
	c.clearJournalLocked()
}

// clearJournalLocked drops the pending record once a transaction has an
// outcome. A stale record would make the next startup revert a change that was
// already confirmed.
func (c *ApplyCoordinator) clearJournalLocked() {
	if c.journal == nil {
		return
	}
	if err := c.journal.Clear(); err != nil && c.lastErr == nil {
		c.lastErr = fmt.Errorf("clear pending-apply journal: %w", err)
	}
}

func (c *ApplyCoordinator) stateLocked() ApplyState {
	st := ApplyState{
		Phase:      c.phase,
		Token:      c.token,
		Deadline:   c.deadline,
		SnapshotID: c.snapshot.ID,
	}
	if c.lastErr != nil {
		st.Err = c.lastErr.Error()
	}
	return st
}
