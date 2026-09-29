package openwrt

import (
	"errors"
	"strings"
	"testing"

	"github.com/veilbridge-os/veilbridge/internal/core"
)

// #54. The shapes below are the ones fw4 rendered on the router (25.12.5,
// 29.09): `meta hour "22:00:00"-"23:59:59" meta day { "Friday", "Saturday" }`
// for the evening section and `"00:00:00"-"07:00:00"` on the next days for
// the morning one.

func sched(days []string, from, to string) *core.InternetSchedule {
	return &core.InternetSchedule{Days: days, From: from, To: to}
}

func TestAnOvernightScheduleIsTwoRulesAndADayOneIsOne(t *testing.T) {
	e, m := scheduleOptions(phoneMAC, "lan", *sched([]string{"fri", "sun"}, "22:00", "07:00"))
	if e["weekdays"] != "Fri Sun" || e["start_time"] != "22:00:00" || e["stop_time"] != "23:59:59" {
		t.Errorf("evening = %v", e)
	}
	if m == nil || m["weekdays"] != "Sat Mon" || m["start_time"] != "00:00:00" || m["stop_time"] != "07:00:00" {
		t.Errorf("morning = %v, want the next days (Sunday night runs into Monday)", m)
	}
	for _, opts := range []map[string]string{e, m} {
		if opts["src_mac"] != phoneMAC || opts["dest"] != "*" || opts["target"] != "REJECT" || opts["proto"] != "all" {
			t.Errorf("rule shape = %v, want the block's shape (#53)", opts)
		}
		if _, ok := opts["utc_time"]; ok {
			t.Error("utc_time set: the owner's times are local")
		}
		if _, ok := opts["family"]; ok {
			t.Error("family set: IPv4 or IPv6 would stay open")
		}
	}
	if e, m := scheduleOptions(phoneMAC, "lan", *sched([]string{"mon"}, "09:00", "17:00")); m != nil || e["stop_time"] != "17:00:00" {
		t.Errorf("a day window: evening %v, morning %v; want one rule to 17:00", e, m)
	}
	if e, m := scheduleOptions(phoneMAC, "lan", *sched([]string{"mon"}, "22:00", "00:00")); m != nil || e["stop_time"] != "23:59:59" {
		t.Errorf("ends at midnight: evening %v, morning %v; want one rule to the end of the day", e, m)
	}
}

func TestASchedulesRulesReadBackAsTheSameSchedule(t *testing.T) {
	for _, s := range []*core.InternetSchedule{
		sched([]string{"mon", "tue", "wed", "thu", "fri"}, "22:00", "07:00"),
		sched([]string{"sat"}, "09:30", "12:00"),
		sched([]string{"sun"}, "21:00", "00:00"),
	} {
		m, u := internetWriter(t, nil)
		if _, err := m.StageDeviceSchedule(phoneMAC, s); err != nil {
			t.Fatal(err)
		}
		got, ok := schedules(u.base + u.show())[phoneMAC]
		if !ok || got.Words() != s.Words() {
			t.Errorf("staged %q, read back %q (found %v)", s.Words(), got.Words(), ok)
		}
		if u.called("uci commit") {
			t.Fatal("committed instead of staged")
		}
	}
}

func TestChangingAScheduleReplacesBothRulesAndOneRowSaysSo(t *testing.T) {
	m, u := internetWriter(t, nil)
	if _, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"fri"}, "22:00", "07:00")); err != nil {
		t.Fatal(err)
	}
	// From overnight to a day window: the morning rule must go, or Saturday
	// morning stays off although nobody asked for that any more.
	cs, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"sat"}, "10:00", "12:00"))
	if err != nil {
		t.Fatal(err)
	}
	if _, left := u.values["firewall.vb_sched_02005e0053d6_m"]; left {
		t.Error("the morning rule of the old schedule was left behind")
	}
	if len(cs) != 1 || cs[0].LabelKey != "firewall.schedule.section" || cs[0].From != "fri 22:00-07:00" ||
		cs[0].To != "sat 10:00-12:00" || cs[0].Subject != phoneMAC || !cs[0].Dangerous {
		t.Errorf("rows = %+v, want one row from the old schedule to the new one", cs)
	}
	// The same again is nothing to do.
	if cs, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"SAT"}, "10:00", "12:00")); err != nil || len(cs) != 0 {
		t.Errorf("the same schedule again: rows %+v, err %v", cs, err)
	}
}

func TestRemovingAScheduleRemovesBothRules(t *testing.T) {
	m, u := internetWriter(t, nil)
	if _, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"fri"}, "22:00", "07:00")); err != nil {
		t.Fatal(err)
	}
	cs, err := m.StageDeviceSchedule(phoneMAC, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k := range u.values {
		if strings.Contains(k, "vb_sched_") {
			t.Errorf("left behind: %s", k)
		}
	}
	if len(cs) != 1 || cs[0].From != "fri 22:00-07:00" || cs[0].To != "" {
		t.Errorf("rows = %+v", cs)
	}
	if cs, err := m.StageDeviceSchedule(phoneMAC, nil); err != nil || len(cs) != 0 {
		t.Errorf("removing what is not there: rows %+v, err %v", cs, err)
	}
}

// Refused by the router's firewall: only this device's two sections go back,
// somebody's staged port forward stays in the draft.
func TestARefusedScheduleTakesBackOnlyItself(t *testing.T) {
	skipped := "[!] Section vb_sched_02005e0053d6 skipped due to invalid options\n"
	m, u := internetWriter(t, map[string]string{"firewall.other": "redirect"}, "Ruleset passes nftables check.\n", skipped)
	_, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"fri"}, "22:00", "07:00"))
	if err == nil || !strings.Contains(err.Error(), "would skip") {
		t.Fatalf("err = %v", err)
	}
	if u.values["firewall.other"] != "redirect" {
		t.Error("somebody else's draft was thrown away")
	}
	for k := range u.values {
		if strings.Contains(k, "vb_sched_") {
			t.Errorf("the refused schedule was left: %s", k)
		}
	}
}

func TestABadScheduleIsRefusedBeforeTheRouterIsTouched(t *testing.T) {
	m, u := internetWriter(t, nil)
	_, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"mon"}, "22:00", "22:00"))
	var fe *core.FieldError
	if !errors.As(err, &fe) || fe.Field != "to" {
		t.Fatalf("err = %v, want a refusal of to", err)
	}
	if u.called("uci set") || u.called(fw4Program) {
		t.Error("touched the router for a schedule that was refused")
	}
}

func TestAScheduleSwitchedOffByHandIsNotRewrittenQuietly(t *testing.T) {
	m, u := internetWriter(t, map[string]string{
		"firewall.vb_sched_02005e0053d6": "rule", "firewall.vb_sched_02005e0053d6.enabled": "0",
	})
	_, err := m.StageDeviceSchedule(phoneMAC, sched([]string{"fri"}, "22:00", "07:00"))
	var fe *core.FieldError
	if !errors.As(err, &fe) || fe.Field != "mac" {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if u.called("uci set") {
		t.Error("rewrote a schedule somebody paused")
	}
}

// The apply bar reads the draft back from `uci changes`: a schedule is one
// row however many sections it took, in both directions.
func TestAScheduleReadsBackAsOneRow(t *testing.T) {
	e, mo := scheduleOptions(phoneMAC, "lan", *sched([]string{"fri"}, "22:00", "07:00"))
	vals := func() map[string]string {
		out := map[string]string{}
		for sec, opts := range map[string]map[string]string{"firewall.vb_sched_02005e0053d6": e, "firewall.vb_sched_02005e0053d6_m": mo} {
			out[sec] = "rule"
			for k, v := range opts {
				out[sec+"."+k] = v
			}
		}
		return out
	}
	var added []string
	for k, v := range vals() {
		added = append(added, k+"='"+v+"'")
	}
	for _, tc := range []struct {
		name              string
		values, committed map[string]string
		lines             []string
		from, to          string
	}{
		{"added", vals(), map[string]string{}, added, "", "fri 22:00-07:00"},
		{"removed", map[string]string{}, vals(), []string{"-firewall.vb_sched_02005e0053d6", "-firewall.vb_sched_02005e0053d6_m"}, "fri 22:00-07:00", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingRunner{values: tc.values, committed: tc.committed, sectionType: "rule", stagedLines: tc.lines}
			cs, err := (networkManager{run: r.run, configDir: t.TempDir()}).StagedChanges()
			if err != nil {
				t.Fatal(err)
			}
			if len(cs) != 1 || cs[0].LabelKey != "firewall.schedule.section" || cs[0].From != tc.from ||
				cs[0].To != tc.to || cs[0].Subject != phoneMAC || !cs[0].Dangerous {
				t.Fatalf("rows = %+v, want one row %q → %q", cs, tc.from, tc.to)
			}
			assertKnownKeys(t, tc.name, cs)
		})
	}
}

// The list carries the schedule, "scheduled", and the router's clock as the
// router tells it: local time from `system info`, synced by the dnsmasq
// hook's file (#50), the zone as the owner set it.
func TestTheListCarriesTheScheduleAndTheRoutersClock(t *testing.T) {
	e, mo := scheduleOptions("02:00:5e:00:53:d6", "lan", *sched([]string{"tue"}, "22:00", "07:00"))
	var show strings.Builder
	for sec, opts := range map[string]map[string]string{"vb_sched_02005e0053d6": e, "vb_sched_02005e0053d6_m": mo} {
		show.WriteString("firewall." + sec + "=rule\n")
		for k, v := range opts {
			show.WriteString("firewall." + sec + "." + k + "='" + v + "'\n")
		}
	}
	for _, synced := range []bool{true, false} {
		sys := cudy()
		if synced {
			sys.files[timeValidFile] = []byte("ntpd says time is valid\n")
		}
		fx := newDevFixture(t, sys, "")
		fx.runner.out["uci -q show firewall"] = []byte(show.String())
		// Tuesday 29.09.2026 23:10 local, as `system info` gives it.
		fx.runner.out["/bin/ubus call system info"] = []byte(`{"localtime": 1790723400, "uptime": 100}`)
		fx.runner.out["uci -q get system.@system[0].zonename"] = []byte("Europe/Moscow\n")
		l, err := fx.m.ListDevices()
		if err != nil {
			t.Fatal(err)
		}
		c := l.Clock
		if c == nil || c.Synced != synced || c.Now != "23:10" || c.Weekday != "tue" || c.Timezone != "Europe/Moscow" {
			t.Fatalf("clock = %+v, want synced=%v, Tue 23:10, Europe/Moscow", c, synced)
		}
		var d core.Device
		for _, x := range l.Devices {
			if x.MAC == "02:00:5e:00:53:d6" {
				d = x
			}
		}
		if d.Internet != core.InternetScheduled || d.Schedule == nil || d.Schedule.Words() != "tue 22:00-07:00" ||
			!d.OffBySchedule || d.ScheduleChangeAt != "07:00" {
			t.Errorf("device = %+v", d)
		}
	}
}
