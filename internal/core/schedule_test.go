package core_test

import (
	"errors"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #54. A schedule is checked and spelt one way before it reaches the router.
func TestScheduleIsSpeltOneWay(t *testing.T) {
	got, err := core.CleanSchedule(core.InternetSchedule{Days: []string{"FRI", "mon", "fri"}, From: "22:00", To: "07:00"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Words() != "mon,fri 22:00-07:00" {
		t.Errorf("words = %q", got.Words())
	}
	for _, bad := range []struct {
		s     core.InternetSchedule
		field string
	}{
		{core.InternetSchedule{Days: nil, From: "22:00", To: "07:00"}, "days"},
		{core.InternetSchedule{Days: []string{"funday"}, From: "22:00", To: "07:00"}, "days"},
		{core.InternetSchedule{Days: []string{"mon"}, From: "24:00", To: "07:00"}, "from"},
		{core.InternetSchedule{Days: []string{"mon"}, From: "22:00", To: "7:00"}, "to"},
		{core.InternetSchedule{Days: []string{"mon"}, From: "22:00", To: "22:00"}, "to"},
	} {
		_, err := core.CleanSchedule(bad.s)
		var fe *core.FieldError
		if !errors.As(err, &fe) || fe.Field != bad.field {
			t.Errorf("%+v: err = %v, want a refusal of %s", bad.s, err, bad.field)
		}
	}
}

// Friday 22:00–07:00 is Friday night AND Saturday morning, and not Friday
// morning: the days are the evenings the window starts on.
func TestAnOvernightWindowRunsIntoTheNextMorning(t *testing.T) {
	s := core.InternetSchedule{Days: []string{"fri"}, From: "22:00", To: "07:00"}
	const mon, thu, fri, sat, sun = 0, 3, 4, 5, 6
	for _, tc := range []struct {
		day, minute int
		off         bool
	}{
		{fri, 21*60 + 59, false},
		{fri, 22 * 60, true},
		{fri, 23*60 + 59, true},
		{sat, 0, true},
		{sat, 6*60 + 59, true},
		{sat, 7 * 60, false},
		{fri, 3 * 60, false}, // Thursday night is not scheduled
		{thu, 23 * 60, false},
		{sun, 23 * 60, false},
		{mon, 3 * 60, false},
	} {
		if got := s.OffAt(tc.day, tc.minute); got != tc.off {
			t.Errorf("day %d %02d:%02d: off = %v, want %v", tc.day, tc.minute/60, tc.minute%60, got, tc.off)
		}
	}
	// Sunday night runs into Monday morning: the week wraps.
	sunday := core.InternetSchedule{Days: []string{"sun"}, From: "23:00", To: "01:00"}
	if !sunday.OffAt(mon, 30) || sunday.OffAt(sat, 30) {
		t.Error("Sunday 23:00-01:00 must cover Monday 00:30 and not Saturday")
	}
}

func TestADayWindowAndOneThatEndsAtMidnight(t *testing.T) {
	day := core.InternetSchedule{Days: []string{"mon"}, From: "09:00", To: "17:00"}
	if !day.OffAt(0, 9*60) || day.OffAt(0, 17*60) || day.OffAt(1, 10*60) || day.Overnight() {
		t.Error("Mon 09:00-17:00 is Monday daytime only")
	}
	midnight := core.InternetSchedule{Days: []string{"mon"}, From: "22:00", To: "00:00"}
	if !midnight.OffAt(0, 23*60) || midnight.OffAt(1, 0) || midnight.Overnight() {
		t.Error("Mon 22:00-00:00 ends at midnight: nothing on Tuesday")
	}
}

func TestNextChangeIsWhatTheRowSays(t *testing.T) {
	s := core.InternetSchedule{Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "22:00", To: "07:00"}
	if got := s.NextChange(0, 14*60); got != "22:00" {
		t.Errorf("Monday 14:00: next change %q, want 22:00 (goes off)", got)
	}
	if got := s.NextChange(1, 3*60); got != "07:00" {
		t.Errorf("Tuesday 03:00: next change %q, want 07:00 (back on)", got)
	}
}

// A block wins over a schedule; the schedule is still reported, so lifting
// the block does not surprise anyone. The row reads the router's clock.
func TestABlockWinsOverASchedule(t *testing.T) {
	clock := &core.RouterClock{Synced: true, Now: "23:10", Weekday: "fri"}
	s := &core.InternetSchedule{Days: []string{"fri"}, From: "22:00", To: "07:00"}

	d := core.Device{Internet: core.InternetAllowed, Schedule: s}
	core.ApplySchedule(&d, clock)
	if d.Internet != core.InternetScheduled || !d.OffBySchedule || d.ScheduleChangeAt != "07:00" {
		t.Errorf("scheduled device = %+v", d)
	}
	b := core.Device{Internet: core.InternetBlocked, Schedule: s}
	core.ApplySchedule(&b, clock)
	if b.Internet != core.InternetBlocked || b.Schedule == nil {
		t.Errorf("blocked device = %+v, want blocked with its schedule kept", b)
	}
	n := core.Device{Internet: core.InternetAllowed, Schedule: s}
	core.ApplySchedule(&n, nil) // the router could not say what time it is
	if n.Internet != core.InternetScheduled || n.OffBySchedule || n.ScheduleChangeAt != "" {
		t.Errorf("without a clock = %+v, want scheduled and nothing claimed about now", n)
	}
}
