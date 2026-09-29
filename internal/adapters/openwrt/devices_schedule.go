package openwrt

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// An internet schedule per device (#54, D-89, D-98).
//
// Like a block (devices_internet.go), a schedule is the router's own firewall
// configuration: named rule sections the panel finds again by name, rendered
// by fw4 as `meta hour … meta day { … }` (measured on 25.12.5, 29.09). The
// firewall checks the day when a packet passes (#50), so a window across
// midnight is two sections: `vb_sched_<mac>` for the evening, on the days the
// window starts, and `vb_sched_<mac>_m` for the morning after, on the next
// days. Times are the router's local time: `utc_time` is never set.

const (
	schedulePrefix = "vb_sched_"
	morningSuffix  = "_m"
	// eveningEnd is how the firewall is told "until midnight": its hour match
	// is inclusive and counts seconds.
	eveningEnd = "23:59:59"
)

// fwDays are the firewall's names of the days, in core.Weekdays order.
var fwDays = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

func scheduleSection(mac string) string {
	return schedulePrefix + strings.ReplaceAll(mac, ":", "")
}

func isScheduleSection(section string) bool { return strings.HasPrefix(section, schedulePrefix) }

// scheduleMAC is the device a schedule section belongs to.
func scheduleMAC(section string) (string, bool) {
	hex := strings.TrimSuffix(strings.TrimPrefix(section, schedulePrefix), morningSuffix)
	if len(hex) != 12 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	mac, err := core.NormalizeMAC(b.String())
	return mac, err == nil
}

// scheduleOptions is what the firewall holds for one schedule: the evening
// section and, for a window across midnight, the morning one (nil otherwise).
func scheduleOptions(mac, local string, s core.InternetSchedule) (evening, morning map[string]string) {
	base := func(name string) map[string]string {
		return map[string]string{
			"name": name, "src": local, "dest": "*", "src_mac": mac, "proto": "all", "target": "REJECT",
		}
	}
	var days, next []string
	for _, d := range s.Days {
		i := core.DayIndex(d)
		days = append(days, fwDays[i])
		next = append(next, fwDays[(i+1)%7])
	}
	evening = base("VeilBridge: internet schedule for " + mac)
	evening["weekdays"] = strings.Join(days, " ")
	evening["start_time"] = s.From + ":00"
	f, _ := core.ClockMinutes(s.From)
	t, _ := core.ClockMinutes(s.To)
	if t > f {
		evening["stop_time"] = s.To + ":00"
		return evening, nil
	}
	evening["stop_time"] = eveningEnd
	if t == 0 {
		return evening, nil // ends at midnight: nothing the next morning
	}
	morning = base("VeilBridge: internet schedule for " + mac + " (after midnight)")
	morning["weekdays"] = strings.Join(next, " ")
	morning["start_time"] = "00:00:00"
	morning["stop_time"] = s.To + ":00"
	return evening, morning
}

// schedules reads every device's schedule back from `uci show firewall`
// output (draft included). A section switched off by hand is no schedule.
func schedules(show string) map[string]core.InternetSchedule {
	evenings := map[string]map[string]string{}
	mornings := map[string]map[string]string{}
	for _, s := range parseUCISections(show) {
		if s.kind != "rule" || !isScheduleSection(s.id) || s.options["enabled"] == "0" {
			continue
		}
		mac, ok := scheduleMAC(s.id)
		if !ok {
			continue
		}
		if strings.HasSuffix(s.id, morningSuffix) {
			mornings[mac] = s.options
		} else {
			evenings[mac] = s.options
		}
	}
	out := map[string]core.InternetSchedule{}
	for mac, e := range evenings {
		if s, ok := scheduleFrom(e, mornings[mac]); ok {
			out[mac] = s
		}
	}
	return out
}

// scheduleFrom turns the firewall's options back into a schedule. Anything
// that does not read as one the panel wrote is not shown as a schedule.
func scheduleFrom(evening, morning map[string]string) (core.InternetSchedule, bool) {
	var days []string
	for _, w := range strings.Fields(evening["weekdays"]) {
		for i, d := range fwDays {
			if strings.EqualFold(w, d) {
				days = append(days, core.Weekdays[i])
			}
		}
	}
	from := strings.TrimSuffix(evening["start_time"], ":00")
	to := strings.TrimSuffix(evening["stop_time"], ":00")
	if evening["stop_time"] == eveningEnd {
		to = "00:00"
		if morning != nil {
			to = strings.TrimSuffix(morning["stop_time"], ":00")
		}
	}
	s, err := core.CleanSchedule(core.InternetSchedule{Days: days, From: from, To: to})
	return s, err == nil
}

func (m networkManager) schedulesNow(ctx context.Context) map[string]core.InternetSchedule {
	if m.run == nil {
		return map[string]core.InternetSchedule{}
	}
	out, err := m.run(ctx, "uci", "-q", "show", "firewall")
	if err != nil {
		return map[string]core.InternetSchedule{}
	}
	return schedules(string(out))
}

// StageDeviceSchedule stages a device's schedule; nil removes it. Asking for
// what is already so changes nothing and returns no rows.
func (m *deviceManager) StageDeviceSchedule(mac string, want *core.InternetSchedule) ([]core.ConfigChange, error) {
	if m.net.run == nil {
		return nil, core.ErrNotImplemented
	}
	mac, err := core.NormalizeMAC(mac)
	if err != nil {
		return nil, core.Refuse("mac", err)
	}
	if want != nil {
		clean, err := core.CleanSchedule(*want)
		if err != nil {
			return nil, err
		}
		want = &clean
	}
	ctx, cancel := context.WithTimeout(context.Background(), stageTimeout)
	defer cancel()

	evening := "firewall." + scheduleSection(mac)
	morning := evening + morningSuffix
	for _, key := range []string{evening, morning} {
		if m.net.uciGet(ctx, key) != "" && m.net.uciGet(ctx, key+".enabled") == "0" {
			return nil, core.Refuse("mac", fmt.Errorf(
				"openwrt: this device's schedule is switched off in the firewall settings; switch it on there, or remove it"))
		}
	}
	have, had := m.net.schedulesNow(ctx)[mac]
	switch {
	case want == nil && !had:
		return nil, nil
	case want != nil && had && have.Words() == want.Words():
		return nil, nil
	}

	local := ""
	if want != nil {
		fw, err := m.net.FirewallInfo()
		if err != nil {
			return nil, err
		}
		for _, z := range fw.Zones {
			if z.Role == core.ZoneLocal {
				local = z.Name
			}
		}
		if local == "" {
			return nil, fmt.Errorf("openwrt: the local network is in no firewall zone, so there is nowhere to put the schedule")
		}
	}
	baseline, err := m.net.fw4Warnings(ctx)
	if err != nil {
		return nil, err
	}
	// Only this device's two sections are ever taken back: the rest of the
	// firewall draft may be somebody's reviewed change.
	undo := func() {
		_, _ = m.net.run(ctx, "uci", "revert", evening)
		_, _ = m.net.run(ctx, "uci", "revert", morning)
	}
	for _, key := range []string{evening, morning} {
		if m.net.uciGet(ctx, key) != "" {
			if err := m.net.uciDelete(ctx, key); err != nil {
				undo()
				return nil, err
			}
		}
	}
	if want != nil {
		e, mo := scheduleOptions(mac, local, *want)
		// Evening first, then the morning after: the same draft every time,
		// and the order a person reads the rules in LuCI.
		for _, part := range []struct {
			key  string
			opts map[string]string
		}{{evening, e}, {morning, mo}} {
			key, opts := part.key, part.opts
			if opts == nil {
				continue
			}
			if err := m.net.uciSet(ctx, key, "rule"); err != nil {
				undo()
				return nil, err
			}
			names := make([]string, 0, len(opts))
			for o := range opts {
				names = append(names, o)
			}
			sort.Strings(names)
			for _, o := range names {
				if err := m.net.uciSet(ctx, key+"."+o, opts[o]); err != nil {
					undo()
					return nil, err
				}
			}
		}
	}
	if err := m.net.fw4Accepts(ctx, baseline); err != nil {
		undo()
		return nil, err
	}
	row := scheduleRow(mac, "", "")
	if had {
		row.From = have.Words()
	}
	if want != nil {
		row.To = want.Words()
	}
	return []core.ConfigChange{row}, nil
}

// scheduleRow is the one row the apply bar shows for a device's schedule,
// however many sections it takes on the router.
func scheduleRow(mac, from, to string) core.ConfigChange {
	said := describe("firewall", roleSchedule, "")
	return core.ConfigChange{
		Label: said.words, LabelKey: said.key, From: from, To: to, Subject: mac,
		Dangerous: dangerousConfig("firewall"), Detail: "firewall." + scheduleSection(mac),
	}
}

// timeValidFile is written by the dnsmasq package's ntp hotplug hook the
// first time ntpd reports a stratum after boot: "the clock was checked".
// Measured on both branches in #50; it lives in tmpfs, so a reboot clears it.
const timeValidFile = "/var/state/dnsmasqsec"

// clock is the router's own idea of the time: `system info` gives local time
// as the router computes it (its libc and time zone, not Go's), and the time
// zone is named the way the owner set it.
func (m *deviceManager) clock(ctx context.Context) *core.RouterClock {
	if m.bus == nil {
		return nil
	}
	si, err := m.bus.SystemInfo(ctx)
	if err != nil || si.Localtime == 0 {
		return nil
	}
	local := time.Unix(si.Localtime, 0).UTC() // already local: read it as is
	zone := m.net.uciGet(ctx, "system.@system[0].zonename")
	if zone == "" {
		zone = m.net.uciGet(ctx, "system.@system[0].timezone")
	}
	return &core.RouterClock{
		Synced:   m.sys.Exists(timeValidFile),
		Now:      local.Format("15:04"),
		Weekday:  core.Weekdays[(int(local.Weekday())+6)%7],
		Timezone: zone,
	}
}
