package core

import (
	"errors"
	"fmt"
	"strings"
)

// An internet schedule for one device (#54, D-89, D-98): on the chosen days,
// from one time to another, the router keeps the device off the internet.
//
// Days are the days a window STARTS on. A window that ends earlier in the day
// than it starts runs across midnight into the next day: "Fri 22:00–07:00"
// is Friday night until Saturday morning. The firewall checks the day of the
// week when a packet passes (measured in #50), so such a window is two rules
// on the router, and this type is the one place that knows they are one.

// Weekdays in the order the panel lists them, Monday first.
var Weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// InternetSchedule is when a device is off the internet.
type InternetSchedule struct {
	Days []string `json:"days" minItems:"1" maxItems:"7" doc:"Days the window starts on: mon, tue, wed, thu, fri, sat, sun"`
	From string   `json:"from" doc:"Start of the window, the router's local time, HH:MM"`
	// To earlier than From means the next morning; "00:00" means midnight.
	To string `json:"to" doc:"End of the window, HH:MM; earlier than from means the next day (22:00 to 07:00 is overnight)"`
}

// Overnight reports whether the window runs across midnight.
func (s InternetSchedule) Overnight() bool {
	f, _ := clockMinutes(s.From)
	t, _ := clockMinutes(s.To)
	return t < f && t != 0
}

// Words is the schedule in one canonical line, "mon,fri 22:00-07:00": what
// the apply bar compares and the panel formats in its own language.
func (s InternetSchedule) Words() string {
	return strings.Join(s.Days, ",") + " " + s.From + "-" + s.To
}

// CleanSchedule checks a requested schedule and returns it in its one
// spelling: days in weekday order, once each; times as HH:MM.
func CleanSchedule(s InternetSchedule) (InternetSchedule, error) {
	want := map[string]bool{}
	for _, d := range s.Days {
		d = strings.ToLower(strings.TrimSpace(d))
		if dayIndex(d) < 0 {
			return InternetSchedule{}, Refuse("days", fmt.Errorf("%q is not a day of the week (mon … sun)", d))
		}
		want[d] = true
	}
	if len(want) == 0 {
		return InternetSchedule{}, Refuse("days", errors.New("choose at least one day"))
	}
	out := InternetSchedule{}
	for _, d := range Weekdays {
		if want[d] {
			out.Days = append(out.Days, d)
		}
	}
	f, ok := clockMinutes(s.From)
	if !ok {
		return InternetSchedule{}, Refuse("from", fmt.Errorf("%q is not a time of day (HH:MM)", s.From))
	}
	t, ok := clockMinutes(s.To)
	if !ok {
		return InternetSchedule{}, Refuse("to", fmt.Errorf("%q is not a time of day (HH:MM)", s.To))
	}
	if f == t {
		return InternetSchedule{}, Refuse("to", errors.New("the window ends when it starts; for a whole day off, turn the internet off instead"))
	}
	out.From, out.To = hhmm(f), hhmm(t)
	return out, nil
}

// OffAt reports whether the device is off the internet at a moment of the
// router's local week: day 0 is Monday, minute 0 is midnight.
func (s InternetSchedule) OffAt(day, minute int) bool {
	f, _ := clockMinutes(s.From)
	t, _ := clockMinutes(s.To)
	for _, d := range s.Days {
		i := dayIndex(d)
		switch {
		case t > f: // within one day
			if day == i && minute >= f && minute < t {
				return true
			}
		default: // up to midnight, then (unless it ends at midnight) the next morning
			if day == i && minute >= f {
				return true
			}
			if t != 0 && day == (i+1)%7 && minute < t {
				return true
			}
		}
	}
	return false
}

// NextChange is the local time (HH:MM) at which OffAt next changes, looking
// at most a week ahead; empty when it never does.
func (s InternetSchedule) NextChange(day, minute int) string {
	now := s.OffAt(day, minute)
	for step := 1; step <= 7*24*60; step++ {
		m := minute + step
		if s.OffAt((day+m/(24*60))%7, m%(24*60)) != now {
			return hhmm(m % (24 * 60))
		}
	}
	return ""
}

// ApplySchedule fills a device's schedule state from the router's clock. A
// block wins over a schedule: the device is off whatever the schedule says.
func ApplySchedule(d *Device, clock *RouterClock) {
	if d.Schedule == nil {
		return
	}
	if d.Internet != InternetBlocked {
		d.Internet = InternetScheduled
	}
	if clock == nil {
		return
	}
	day := dayIndex(clock.Weekday)
	minute, ok := clockMinutes(clock.Now)
	if day < 0 || !ok {
		return
	}
	d.OffBySchedule = d.Schedule.OffAt(day, minute)
	d.ScheduleChangeAt = d.Schedule.NextChange(day, minute)
}

// DayIndex is the position of a weekday (mon = 0), or -1.
func dayIndex(d string) int {
	for i, w := range Weekdays {
		if w == d {
			return i
		}
	}
	return -1
}

// clockMinutes reads "HH:MM" (or "HH:MM:SS", as the firewall stores it) as
// minutes after midnight.
func clockMinutes(v string) (int, bool) {
	var h, m, sec int
	v = strings.TrimSpace(v)
	switch strings.Count(v, ":") {
	case 1:
		if n, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil || n != 2 || len(v) != 5 {
			return 0, false
		}
	case 2:
		if n, err := fmt.Sscanf(v, "%d:%d:%d", &h, &m, &sec); err != nil || n != 3 || len(v) != 8 {
			return 0, false
		}
	default:
		return 0, false
	}
	if h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func hhmm(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// ClockMinutes is clockMinutes for adapters that read the firewall's times.
func ClockMinutes(v string) (int, bool) { return clockMinutes(v) }

// DayIndex is dayIndex for adapters.
func DayIndex(d string) int { return dayIndex(d) }
