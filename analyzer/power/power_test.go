// Copyright 2026 reedhoop. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package power

import (
	"os"
	"path/filepath"
	"testing"
)

// loadSample 读取 testdata 下的样本文件。
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
	raw := loadSample(t, "power_sample.txt")
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil Summary")
	}

	// 顶层字段
	if got.Wakefulness != "Awake" {
		t.Errorf("Wakefulness = %q, want Awake", got.Wakefulness)
	}
	if !got.IsPowered {
		t.Error("IsPowered = false, want true")
	}
	if got.PlugType != 1 {
		t.Errorf("PlugType = %d, want 1", got.PlugType)
	}
	if got.BatteryLevel != 13 {
		t.Errorf("BatteryLevel = %d, want 13", got.BatteryLevel)
	}
	if got.DeviceIdleMode {
		t.Error("DeviceIdleMode = true, want false")
	}
	if got.LightDeviceIdleMode {
		t.Error("LightDeviceIdleMode = true, want false")
	}
	if got.LastWakeTime != 4342927 {
		t.Errorf("LastWakeTime = %d, want 4342927", got.LastWakeTime)
	}
	if got.LastSleepTime != 3987051 {
		t.Errorf("LastSleepTime = %d, want 3987051", got.LastSleepTime)
	}
	if got.LastSleepReason != "timeout" {
		t.Errorf("LastSleepReason = %q, want timeout", got.LastSleepReason)
	}
	if got.WakeLockSummary != 0x1 {
		t.Errorf("WakeLockSummary = 0x%x, want 0x1", got.WakeLockSummary)
	}

	// WakeLocks
	if len(got.WakeLocks) != 2 {
		t.Fatalf("WakeLocks len = %d, want 2", len(got.WakeLocks))
	}
	wl0 := got.WakeLocks[0]
	if wl0.Level != "PARTIAL_WAKE_LOCK" || wl0.Name != "AudioIn" {
		t.Errorf("wl0 = %+v, want AudioIn PARTIAL_WAKE_LOCK", wl0)
	}
	if wl0.UID != 1041 || wl0.PID != 0 {
		t.Errorf("wl0 uid/pid = %d/%d, want 1041/0", wl0.UID, wl0.PID)
	}
	if wl0.AcquiredAgoMs != 15*60*1000+12*1000+377 {
		t.Errorf("wl0 AcquiredAgoMs = %d, want %d", wl0.AcquiredAgoMs, 15*60*1000+12*1000+377)
	}
	if !wl0.Long {
		t.Error("wl0 Long = false, want true")
	}

	wl1 := got.WakeLocks[1]
	if wl1.Name != "*gms_scheduler*/com.google.android.gms/.clearcut.uploader.QosUploaderService" {
		t.Errorf("wl1 Name = %q", wl1.Name)
	}
	if wl1.UID != 10180 || wl1.PID != 3853 {
		t.Errorf("wl1 uid/pid = %d/%d, want 10180/3853", wl1.UID, wl1.PID)
	}
	if wl1.AcquiredAgoMs != 6*1000+304 {
		t.Errorf("wl1 AcquiredAgoMs = %d, want %d", wl1.AcquiredAgoMs, 6*1000+304)
	}
	if wl1.Long {
		t.Error("wl1 Long = true, want false")
	}

	// SuspendBlockers
	if len(got.SuspendBlockers) != 5 {
		t.Fatalf("SuspendBlockers len = %d, want 5", len(got.SuspendBlockers))
	}
	sb := got.SuspendBlockers[1]
	if sb.Name != "PowerManagerService.WakeLocks" {
		t.Errorf("sb1 Name = %q", sb.Name)
	}
	if sb.RefCount != 1 {
		t.Errorf("sb1 RefCount = %d, want 1", sb.RefCount)
	}
	if sb.AcquiredAt != "unknown: (07-13 22:02:39.565)" {
		t.Errorf("sb1 AcquiredAt = %q", sb.AcquiredAt)
	}

	// UIDStates：fixture 里有 18 条
	if len(got.UIDStates) != 18 {
		t.Fatalf("UIDStates len = %d, want 18", len(got.UIDStates))
	}
	// 抽查首条
	us0 := got.UIDStates[0]
	if us0.UID != "1000" || !us0.Active || us0.Count != 0 || us0.State != 0 {
		t.Errorf("us0 = %+v", us0)
	}
	// 抽查 u0a 前缀的 uid
	var u0a180 *UIDState
	for i := range got.UIDStates {
		if got.UIDStates[i].UID == "u0a180" {
			u0a180 = &got.UIDStates[i]
			break
		}
	}
	if u0a180 == nil {
		t.Fatal("u0a180 not found")
	}
	if !u0a180.Active || u0a180.Count != 1 || u0a180.State != 5 {
		t.Errorf("u0a180 = %+v", u0a180)
	}

	// BatterySaver
	if got.BatterySaver.CurrentlyOn {
		t.Error("BatterySaver.CurrentlyOn = true, want false")
	}
	if got.BatterySaver.TimesFullEnabled != 0 {
		t.Errorf("TimesFullEnabled = %d, want 0", got.BatterySaver.TimesFullEnabled)
	}
	// DrainStats：fixture 里有 6 行（NonDoze/Deep/Light × NonIntr/Intr）
	if len(got.BatterySaver.DrainStats) != 6 {
		t.Fatalf("DrainStats len = %d, want 6", len(got.BatterySaver.DrainStats))
	}
	// 找到 NonDoze Intr 这一行（43m / 855mAh / 15% / 1186,9mAh/h）
	var nonDozeIntr *DrainStat
	for i := range got.BatterySaver.DrainStats {
		ds := &got.BatterySaver.DrainStats[i]
		if ds.DozeMode == "NonDoze" && ds.Interruptible {
			nonDozeIntr = ds
			break
		}
	}
	if nonDozeIntr == nil {
		t.Fatal("NonDoze Intr drain stat not found")
	}
	if nonDozeIntr.DurationMin != 43 {
		t.Errorf("DurationMin = %d, want 43", nonDozeIntr.DurationMin)
	}
	if nonDozeIntr.MahUsed != 855 {
		t.Errorf("MahUsed = %v, want 855", nonDozeIntr.MahUsed)
	}
	if nonDozeIntr.PercentOfTotal != 15 {
		t.Errorf("PercentOfTotal = %v, want 15", nonDozeIntr.PercentOfTotal)
	}
	if nonDozeIntr.MahPerHour != 1186.9 {
		t.Errorf("MahPerHour = %v, want 1186.9", nonDozeIntr.MahPerHour)
	}
}

func TestParseDurationMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"-15m12s377ms", 15*60*1000 + 12*1000 + 377},
		{"-6s304ms", 6*1000 + 304},
		{"-1h2m3s", 3600*1000 + 2*60*1000 + 3*1000},
		{"+14s121ms", 14*1000 + 121},
		{"-47s832ms", 47*1000 + 832},
		{"0", 0},
	}
	for _, c := range cases {
		got := parseDurationMs(c.in)
		if got != c.want {
			t.Errorf("parseDurationMs(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
