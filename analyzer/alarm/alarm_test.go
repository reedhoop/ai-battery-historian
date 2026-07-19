// Copyright 2026 reedhoop. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package alarm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func loadSample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestParse_NoSection(t *testing.T) {
	got, err := Parse("no dumpsys here")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestParse_Sample(t *testing.T) {
	raw := loadSample(t, "alarm_sample.txt")
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil Summary")
	}

	// 顶层字段
	if got.NowRTC != 1783974699246 {
		t.Errorf("NowRTC = %d, want 1783974699246", got.NowRTC)
	}
	if got.NowElapsed != 16727112 {
		t.Errorf("NowElapsed = %d, want 16727112", got.NowElapsed)
	}
	if got.RuntimeStartedISO != "2026-07-13 17:53:27.030" {
		t.Errorf("RuntimeStartedISO = %q", got.RuntimeStartedISO)
	}
	// 4h38m12s216ms → 4*3600*1000 + 38*60*1000 + 12*1000 + 216 = 16695216
	if got.RuntimeUptimeMs != 4*3600*1000+38*60*1000+12*1000+216 {
		t.Errorf("RuntimeUptimeMs = %d, want %d", got.RuntimeUptimeMs, 4*3600*1000+38*60*1000+12*1000+216)
	}
	if got.PendingAlarms != 69 {
		t.Errorf("PendingAlarms = %d, want 69", got.PendingAlarms)
	}
	if got.ForceAllAppsStandby {
		t.Error("ForceAllAppsStandby = true, want false")
	}

	// NextWakeupAlarm / NextNonWakeupAlarm / LastWakeup
	if got.NextWakeupAlarm == "" {
		t.Error("NextWakeupAlarm is empty")
	}
	if !strings.Contains(got.NextWakeupAlarm, "+17s309ms") {
		t.Errorf("NextWakeupAlarm = %q, want contains +17s309ms", got.NextWakeupAlarm)
	}
	if !strings.Contains(got.NextNonWakeupAlarm, "+20s754ms") {
		t.Errorf("NextNonWakeupAlarm = %q", got.NextNonWakeupAlarm)
	}
	if !strings.Contains(got.LastWakeup, "-11s191ms") {
		t.Errorf("LastWakeup = %q", got.LastWakeup)
	}

	// ActiveUIDs：fixture 里 22 个
	wantActive := []int32{10073, 10078, 10089, 10094, 10106, 10111, 10166, 10179, 10180, 10181, 10182, 10194, 10197, 10218, 10223, 10242, 10243, 10250, 10259, 10269, 99014, 99015}
	if !reflect.DeepEqual(got.ActiveUIDs, wantActive) {
		t.Errorf("ActiveUIDs = %v, want %v", got.ActiveUIDs, wantActive)
	}

	// ScheduleExactAlarmUIDs
	if len(got.ScheduleExactAlarmUIDs) != 15 {
		t.Fatalf("ScheduleExactAlarmUIDs len = %d, want 15", len(got.ScheduleExactAlarmUIDs))
	}
	if got.ScheduleExactAlarmUIDs[0] != 1000 || got.ScheduleExactAlarmUIDs[2] != 10087 {
		t.Errorf("ScheduleExactAlarmUIDs[0,2] = %d,%d, want 1000,10087", got.ScheduleExactAlarmUIDs[0], got.ScheduleExactAlarmUIDs[2])
	}

	// Alarms：fixture 里 7 条
	if len(got.Alarms) != 7 {
		t.Fatalf("Alarms len = %d, want 7", len(got.Alarms))
	}
	a0 := got.Alarms[0]
	if a0.Seq != 1 || a0.Type != "ELAPSED_WAKEUP" || a0.Tag != "*walarm*:WriteBufferAlarm" {
		t.Errorf("a0 = %+v", a0)
	}
	if a0.PackageName != "android" {
		t.Errorf("a0 PackageName = %q, want android", a0.PackageName)
	}
	if a0.WhenElapsedMs != 16736921 {
		t.Errorf("a0 WhenElapsedMs = %d, want 16736921", a0.WhenElapsedMs)
	}
	if a0.RepeatIntervalMs != 0 {
		t.Errorf("a0 RepeatIntervalMs = %d, want 0", a0.RepeatIntervalMs)
	}
	if a0.Flags != 0x8 {
		t.Errorf("a0 Flags = 0x%x, want 0x8", a0.Flags)
	}
	if a0.WindowMs != 7500 {
		t.Errorf("a0 WindowMs = %d, want 7500", a0.WindowMs)
	}
	if a0.ExactAllowReason != "" {
		t.Errorf("a0 ExactAllowReason = %q, want empty", a0.ExactAllowReason)
	}
	if a0.Listener != "android.app.AlarmManager$ListenerWrapper@a7d26b5" {
		t.Errorf("a0 Listener = %q", a0.Listener)
	}
	if a0.Operation != "" {
		t.Errorf("a0 Operation = %q, want empty", a0.Operation)
	}

	// a1: ELAPSED with exactAllowReason=allow-listed
	a1 := got.Alarms[1]
	if a1.Type != "ELAPSED" || a1.ExactAllowReason != "allow-listed" {
		t.Errorf("a1 = %+v", a1)
	}
	if a1.WindowMs != 0 {
		t.Errorf("a1 WindowMs = %d, want 0", a1.WindowMs)
	}

	// a2: ELAPSED with operation + repeatInterval=60000
	a2 := got.Alarms[2]
	if a2.RepeatIntervalMs != 60000 {
		t.Errorf("a2 RepeatIntervalMs = %d, want 60000", a2.RepeatIntervalMs)
	}
	if a2.Operation != "com.android.nfc broadcastIntent" {
		t.Errorf("a2 Operation = %q", a2.Operation)
	}
	if a2.PackageName != "com.android.nfc" {
		t.Errorf("a2 PackageName = %q", a2.PackageName)
	}
	// window=+45s0ms → 45000
	if a2.WindowMs != 45000 {
		t.Errorf("a2 WindowMs = %d, want 45000", a2.WindowMs)
	}

	// a3: ELAPSED with gms scheduler operation（包名含 /）
	a3 := got.Alarms[3]
	if a3.Operation != "com.google.android.gms/com.google.android.gms.scheduler broadcastIntent" {
		t.Errorf("a3 Operation = %q", a3.Operation)
	}

	// a5: RTC_WAKEUP
	a5 := got.Alarms[5]
	if a5.Type != "RTC_WAKEUP" || a5.ExactAllowReason != "policy_permission" {
		t.Errorf("a5 = %+v", a5)
	}
	if a5.WhenElapsedMs != 18427866 {
		t.Errorf("a5 WhenElapsedMs = %d, want 18427866", a5.WhenElapsedMs)
	}

	// a6: ELAPSED_WAKEUP + repeatInterval=3600000
	a6 := got.Alarms[6]
	if a6.RepeatIntervalMs != 3600000 {
		t.Errorf("a6 RepeatIntervalMs = %d, want 3600000", a6.RepeatIntervalMs)
	}

	// TopAlarms：repeat > 0 优先，按 repeatInterval 降序 + seq 升序
	if len(got.TopAlarms) == 0 {
		t.Fatal("TopAlarms empty")
	}
	// 第一条应是 repeatInterval=3600000 的（a6），其次 60000 的（a2）
	if got.TopAlarms[0].RepeatIntervalMs != 3600000 {
		t.Errorf("TopAlarms[0].RepeatIntervalMs = %d, want 3600000", got.TopAlarms[0].RepeatIntervalMs)
	}
	if got.TopAlarms[1].RepeatIntervalMs != 60000 {
		t.Errorf("TopAlarms[1].RepeatIntervalMs = %d, want 60000", got.TopAlarms[1].RepeatIntervalMs)
	}
	// 之后是 repeat=0 的，按 seq 升序
	if got.TopAlarms[2].Seq != 1 {
		t.Errorf("TopAlarms[2].Seq = %d, want 1", got.TopAlarms[2].Seq)
	}
}

func TestParseUIDToken(t *testing.T) {
	cases := []struct {
		in   string
		want int32
		ok   bool
	}{
		{"1000", 1000, true},
		{"u0a73", 10073, true},
		{"u0i14", 99014, true},
		{"u10a5", 10005, true},
		{"", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseUIDToken(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseUIDToken(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseDurationMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"+4h38m12s216ms", 4*3600*1000 + 38*60*1000 + 12*1000 + 216},
		{"+9s809ms", 9*1000 + 809},
		{"+45s0ms", 45*1000},
		{"+1m10s754ms", 60*1000 + 10*1000 + 754},
		{"0", 0},
		{"-15m12s377ms", 15*60*1000 + 12*1000 + 377},
	}
	for _, c := range cases {
		got := parseDurationMs(c.in)
		if got != c.want {
			t.Errorf("parseDurationMs(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
