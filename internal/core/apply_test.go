package core

import (
	"errors"
	"testing"
	"time"
)

// fakeApplier records what the coordinator asked for and can fail on demand.
// Every failure mode below is one that a real router produces: a snapshot that
// cannot be taken (no space), a commit that breaks the link, a revert that does
// not come back.
type fakeApplier struct {
	// id is what the next Snapshot() returns. Tests that run two transactions
	// change it in between: a coordinator that reports a stale snapshot id
	// would otherwise look correct, because both transactions would be
	// "snap-1".
	id string

	snapshots int
	commits   int
	reverts   int

	snapErr   error
	commitErr error
	revertErr error

	revertedID string
}

func (f *fakeApplier) Snapshot() (Snapshot, error) {
	f.snapshots++
	if f.snapErr != nil {
		return Snapshot{}, f.snapErr
	}
	id := f.id
	if id == "" {
		id = "snap-1"
	}
	return Snapshot{ID: id, Taken: time.Unix(0, 0)}, nil
}

func (f *fakeApplier) Commit() error {
	f.commits++
	return f.commitErr
}

func (f *fakeApplier) Revert(s Snapshot) error {
	f.reverts++
	f.revertedID = s.ID
	return f.revertErr
}

// fakeTimer lets a test fire the watchdog instead of waiting for it.
type fakeTimer struct {
	fn      func()
	stopped bool
}

func (t *fakeTimer) Stop() bool {
	t.stopped = true
	return true
}

// newTestCoordinator wires a coordinator to a controllable clock and timer.
// Each Apply gets its own timer and its own token, because the interesting
// failures live exactly there: a timer from an older transaction firing during
// a newer one.
func newTestCoordinator(f *fakeApplier) (*ApplyCoordinator, *[]*fakeTimer) {
	c := NewApplyCoordinator(f)
	timers := &[]*fakeTimer{}
	c.afterFunc = func(_ time.Duration, fn func()) timer {
		tm := &fakeTimer{fn: fn}
		*timers = append(*timers, tm)
		return tm
	}
	c.now = func() time.Time { return time.Unix(1000, 0) }
	n := 0
	c.newToken = func() string {
		n++
		return "tok-" + string(rune('0'+n))
	}
	return c, timers
}

// last returns the timer armed by the most recent Apply.
func last(timers *[]*fakeTimer) *fakeTimer {
	if len(*timers) == 0 {
		return &fakeTimer{fn: func() {}}
	}
	return (*timers)[len(*timers)-1]
}

func TestApplyTakesSnapshotBeforeCommitting(t *testing.T) {
	f := &fakeApplier{}
	c, _ := newTestCoordinator(f)

	st, err := c.Apply(90 * time.Second)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if f.snapshots != 1 || f.commits != 1 {
		t.Fatalf("want one snapshot and one commit, got %d/%d", f.snapshots, f.commits)
	}
	if st.Phase != PhaseAwaitingConfirm {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseAwaitingConfirm)
	}
	if st.Token == "" {
		t.Fatal("no confirm token handed out — nothing could ever be confirmed")
	}
	if want := time.Unix(1000, 0).Add(90 * time.Second); !st.Deadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", st.Deadline, want)
	}
}

// The watchdog is the whole point: a change that cuts the connection must be
// undone by the device itself, with nobody able to reach it.
func TestWatchdogRevertsWhenNobodyConfirms(t *testing.T) {
	f := &fakeApplier{}
	c, timers := newTestCoordinator(f)

	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("apply: %v", err)
	}
	last(timers).fn() // deadline reached

	st := c.State()
	if st.Phase != PhaseReverted {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseReverted)
	}
	if f.reverts != 1 || f.revertedID != "snap-1" {
		t.Fatalf("want revert to snap-1, got %d reverts of %q", f.reverts, f.revertedID)
	}
}

func TestConfirmStopsTheWatchdog(t *testing.T) {
	f := &fakeApplier{}
	c, timers := newTestCoordinator(f)

	st, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := c.Confirm(st.Token); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !last(timers).stopped {
		t.Fatal("watchdog still armed after confirm")
	}

	// Firing a stale timer must not revert a confirmed change. This is the
	// race between "confirm arrives" and "deadline fires" — on a real router
	// they can happen in the same millisecond.
	last(timers).fn()
	if f.reverts != 0 {
		t.Fatalf("confirmed change was reverted by a stale watchdog (%d reverts)", f.reverts)
	}
	if got := c.State().Phase; got != PhaseConfirmed {
		t.Fatalf("phase = %q, want %q", got, PhaseConfirmed)
	}
}

// The dangerous version of that race: the first transaction is confirmed, a
// second one is already in flight, and only then does the first watchdog fire.
// Without a token check it would revert somebody else's transaction — using a
// stale snapshot, which on a router means silently restoring old network
// settings while the operator watches a "pending" apply-bar.
func TestStaleWatchdogCannotTouchTheNextTransaction(t *testing.T) {
	f := &fakeApplier{}
	c, timers := newTestCoordinator(f)

	first, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	staleTimer := last(timers)
	if _, err := c.Confirm(first.Token); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	second, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if second.Token == first.Token {
		t.Fatal("two transactions share a token — one could confirm the other")
	}

	staleTimer.fn() // the first watchdog fires late

	if f.reverts != 0 {
		t.Fatalf("a stale watchdog reverted the running transaction (%d reverts)", f.reverts)
	}
	st := c.State()
	if st.Phase != PhaseAwaitingConfirm || st.Token != second.Token {
		t.Fatalf("second transaction disturbed: phase %q token %q", st.Phase, st.Token)
	}
}

func TestConfirmWithWrongTokenIsRefused(t *testing.T) {
	f := &fakeApplier{}
	c, _ := newTestCoordinator(f)

	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := c.Confirm("someone-elses-token"); !errors.Is(err, ErrWrongToken) {
		t.Fatalf("err = %v, want ErrWrongToken", err)
	}
	if got := c.State().Phase; got != PhaseAwaitingConfirm {
		t.Fatalf("phase = %q, want it still awaiting confirm", got)
	}
}

func TestConfirmWithoutApplyIsRefused(t *testing.T) {
	c, _ := newTestCoordinator(&fakeApplier{})
	if _, err := c.Confirm("tok-1"); !errors.Is(err, ErrNoApplyInFlight) {
		t.Fatalf("err = %v, want ErrNoApplyInFlight", err)
	}
}

// Two overlapping transactions would mean two snapshots, and reverting to the
// older one would quietly undo a change somebody already confirmed.
func TestSecondApplyIsRefusedWhileOneIsPending(t *testing.T) {
	f := &fakeApplier{}
	c, _ := newTestCoordinator(f)

	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := c.Apply(time.Minute); !errors.Is(err, ErrApplyInFlight) {
		t.Fatalf("err = %v, want ErrApplyInFlight", err)
	}
	if f.snapshots != 1 || f.commits != 1 {
		t.Fatalf("second apply touched the device: %d snapshots, %d commits", f.snapshots, f.commits)
	}
}

func TestFailedSnapshotMeansNoCommitAtAll(t *testing.T) {
	f := &fakeApplier{snapErr: errors.New("no space left on device")}
	c, _ := newTestCoordinator(f)

	st, err := c.Apply(time.Minute)
	if err == nil {
		t.Fatal("apply reported success although the snapshot failed")
	}
	if f.commits != 0 {
		t.Fatal("committed without a snapshot — the change would be unrevertable")
	}
	if st.Phase != PhaseIdle {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseIdle)
	}
}

func TestFailedCommitRestoresImmediately(t *testing.T) {
	f := &fakeApplier{commitErr: errors.New("uci: invalid value")}
	c, _ := newTestCoordinator(f)

	st, err := c.Apply(time.Minute)
	if err == nil {
		t.Fatal("apply reported success although the commit failed")
	}
	if f.reverts != 1 {
		t.Fatalf("want one revert after a failed commit, got %d", f.reverts)
	}
	if st.Phase != PhaseReverted {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseReverted)
	}
}

// The worst case must be loud: if the revert fails, the device runs a
// configuration nobody chose and may need physical access.
func TestFailedRevertIsReportedAsItsOwnPhase(t *testing.T) {
	f := &fakeApplier{revertErr: errors.New("uci: cannot restore")}
	c, timers := newTestCoordinator(f)

	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("apply: %v", err)
	}
	last(timers).fn()

	st := c.State()
	if st.Phase != PhaseRevertFailed {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseRevertFailed)
	}
	if st.Err == "" {
		t.Fatal("revert failure reported without any explanation")
	}
}

func TestManualRevertUndoesAPendingApply(t *testing.T) {
	f := &fakeApplier{}
	c, timers := newTestCoordinator(f)

	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("apply: %v", err)
	}
	st, err := c.Revert()
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if !last(timers).stopped {
		t.Fatal("watchdog left armed after a manual revert")
	}
	if st.Phase != PhaseReverted || f.reverts != 1 {
		t.Fatalf("phase = %q, reverts = %d", st.Phase, f.reverts)
	}
}

// fakeJournal is an in-memory ApplyJournal; errors are injectable because the
// interesting question is what the coordinator does when persistence fails.
type fakeJournal struct {
	rec     ApplyRecord
	present bool
	saves   int
	clears  int

	saveErr error
	loadErr error
}

func (j *fakeJournal) Save(r ApplyRecord) error {
	j.saves++
	if j.saveErr != nil {
		return j.saveErr
	}
	j.rec, j.present = r, true
	return nil
}

func (j *fakeJournal) Load() (ApplyRecord, bool, error) {
	if j.loadErr != nil {
		return ApplyRecord{}, false, j.loadErr
	}
	return j.rec, j.present, nil
}

func (j *fakeJournal) Clear() error {
	j.clears++
	j.present = false
	return nil
}

// fakeLoader returns the snapshot a previous process took.
type fakeLoader struct {
	snap Snapshot
	err  error
}

func (l fakeLoader) LoadSnapshot(id string) (Snapshot, error) {
	if l.err != nil {
		return Snapshot{}, l.err
	}
	return Snapshot{ID: id, Payload: l.snap.Payload}, nil
}

func TestPendingApplyIsJournalledAndClearedOnConfirm(t *testing.T) {
	f := &fakeApplier{}
	j := &fakeJournal{}
	c, _ := newTestCoordinator(f)
	c.WithJournal(j)

	st, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if j.saves != 1 || !j.present || j.rec.SnapshotID != "snap-1" {
		t.Fatalf("pending apply not journalled: %+v", j)
	}
	if _, err := c.Confirm(st.Token); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if j.present {
		t.Fatal("journal still holds a confirmed transaction — the next start would revert it")
	}
}

// The daemon died inside the confirmation window. Nobody confirmed, so the
// change must not survive the restart just because the watchdog died with it.
func TestRecoverPendingRevertsAfterARestart(t *testing.T) {
	f := &fakeApplier{}
	j := &fakeJournal{rec: ApplyRecord{SnapshotID: "snap-1", Token: "tok-1"}, present: true}
	c, _ := newTestCoordinator(f)
	c.WithJournal(j)

	recovered, err := c.RecoverPending(fakeLoader{snap: Snapshot{Payload: []byte("x")}})
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !recovered || f.reverts != 1 || f.revertedID != "snap-1" {
		t.Fatalf("recovery did not restore snap-1: recovered=%v reverts=%d id=%q",
			recovered, f.reverts, f.revertedID)
	}
	if j.present {
		t.Fatal("journal not cleared after a successful recovery")
	}
	if got := c.State().Phase; got != PhaseReverted {
		t.Fatalf("phase = %q, want %q", got, PhaseReverted)
	}
}

func TestRecoverPendingDoesNothingWhenNothingWasPending(t *testing.T) {
	f := &fakeApplier{}
	c, _ := newTestCoordinator(f)
	c.WithJournal(&fakeJournal{})

	recovered, err := c.RecoverPending(fakeLoader{})
	if err != nil || recovered {
		t.Fatalf("recovered=%v err=%v, want a quiet no-op", recovered, err)
	}
	if f.reverts != 0 {
		t.Fatalf("a clean start reverted something (%d reverts)", f.reverts)
	}
}

// A snapshot that cannot be read is the one case where the daemon must not
// pretend: the device is running an unconfirmed configuration and nothing here
// can undo it.
func TestRecoverPendingReportsAMissingSnapshot(t *testing.T) {
	f := &fakeApplier{}
	j := &fakeJournal{rec: ApplyRecord{SnapshotID: "snap-gone"}, present: true}
	c, _ := newTestCoordinator(f)
	c.WithJournal(j)

	recovered, err := c.RecoverPending(fakeLoader{err: errors.New("no such file")})
	if err == nil || recovered {
		t.Fatal("a missing snapshot was reported as a successful recovery")
	}
	st := c.State()
	if st.Phase != PhaseRevertFailed {
		t.Fatalf("phase = %q, want %q", st.Phase, PhaseRevertFailed)
	}
	// The worst state the device can be in is also the one where naming the
	// transaction matters most: somebody has to know which snapshot went
	// missing.
	if st.SnapshotID != "snap-gone" {
		t.Fatalf("snapshot id = %q, want snap-gone", st.SnapshotID)
	}
}

// If the journal cannot be written the change is already live, so the honest
// answer is "applied, but the safety net is thinner than advertised".
func TestApplyReportsAnUnjournalledTransaction(t *testing.T) {
	f := &fakeApplier{}
	j := &fakeJournal{saveErr: errors.New("read-only filesystem")}
	c, _ := newTestCoordinator(f)
	c.WithJournal(j)

	st, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if st.Phase != PhaseAwaitingConfirm {
		t.Fatalf("phase = %q, want the change to be live", st.Phase)
	}
	if st.Err == "" {
		t.Fatal("a transaction that cannot survive a restart was reported as fully safe")
	}
}

// A finished transaction must still say which snapshot it was. The client that
// started it usually never saw the reply — the change cut its own link — so
// "which change just got undone" can only come from the state endpoint. It is
// also the only way an operator (or the M1 gate script) can tell this run's
// transaction from the one before it.
func TestFinishedTransactionStillNamesItsSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   func(*ApplyCoordinator, *[]*fakeTimer)
		phase ApplyPhase
	}{
		{"auto-revert", func(c *ApplyCoordinator, tm *[]*fakeTimer) { last(tm).fn() }, PhaseReverted},
		{"manual revert", func(c *ApplyCoordinator, _ *[]*fakeTimer) { _, _ = c.Revert() }, PhaseReverted},
		{"confirm", func(c *ApplyCoordinator, _ *[]*fakeTimer) {
			_, _ = c.Confirm(c.State().Token)
		}, PhaseConfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeApplier{}
			c, timers := newTestCoordinator(f)
			if _, err := c.Apply(time.Minute); err != nil {
				t.Fatalf("apply: %v", err)
			}
			tc.end(c, timers)

			st := c.State()
			if st.Phase != tc.phase {
				t.Fatalf("phase = %q, want %q", st.Phase, tc.phase)
			}
			if st.SnapshotID != "snap-1" {
				t.Fatalf("snapshot id = %q after %s, want snap-1", st.SnapshotID, tc.name)
			}
		})
	}
}

// Recovery after a restart is the case where the coordinator never held the
// snapshot in memory at all: the id comes from the journal, and it still has
// to reach the API.
func TestRecoveredTransactionNamesItsSnapshot(t *testing.T) {
	f := &fakeApplier{}
	c, _ := newTestCoordinator(f)
	j := &fakeJournal{rec: ApplyRecord{SnapshotID: "snap-from-disk", Token: "tok-old"}, present: true}
	c.WithJournal(j)

	done, err := c.RecoverPending(fakeLoader{snap: Snapshot{Payload: []byte("x")}})
	if err != nil || !done {
		t.Fatalf("recover: done=%v err=%v", done, err)
	}
	if st := c.State(); st.SnapshotID != "snap-from-disk" {
		t.Fatalf("snapshot id = %q, want snap-from-disk", st.SnapshotID)
	}
}

// The id must follow the transactions, not stick to the first one: an operator
// looking at the apply-bar after the second change has to see the second
// change.
func TestSnapshotIDFollowsTheNewestTransaction(t *testing.T) {
	f := &fakeApplier{}
	c, timers := newTestCoordinator(f)

	st, err := c.Apply(time.Minute)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := c.Confirm(st.Token); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	f.id = "snap-2"
	if _, err := c.Apply(time.Minute); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := c.State().SnapshotID; got != "snap-2" {
		t.Fatalf("snapshot id = %q while the second transaction is live, want snap-2", got)
	}

	last(timers).fn() // the second transaction auto-reverts
	if got := c.State().SnapshotID; got != "snap-2" {
		t.Fatalf("snapshot id = %q after the second revert, want snap-2", got)
	}
}
