// Copyright 2026 reedhoop. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package procstats

import (
	"os"
	"path/filepath"
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
	raw := loadSample(t, "procstats_sample.txt")
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil Summary")
	}

	if got.CommittedFrom != "2026-07-13-15-35-12" {
		t.Errorf("CommittedFrom = %q", got.CommittedFrom)
	}

	// 7 个进程块
	if len(got.Processes) != 7 {
		t.Fatalf("Processes len = %d, want 7", len(got.Processes))
	}

	// 排序后第 1 个应是 Total=99% 的某个，第 6 个是 82% 的 gms.inputmethod
	// （99% 之间按 stable 排序保留原顺序，所以第 1 个是 systemui）
	p0 := got.Processes[0]
	if p0.Package != "com.android.systemui" {
		t.Errorf("p0 Package = %q, want com.android.systemui", p0.Package)
	}
	if p0.UID != "u0a106" || p0.Version != "v20251243" {
		t.Errorf("p0 UID/Version = %q/%q", p0.UID, p0.Version)
	}
	if p0.Total.Percent != 99 {
		t.Errorf("p0 Total.Percent = %v, want 99", p0.Total.Percent)
	}
	if p0.Total.RSSMinKB != 474*1024 || p0.Total.RSSAvgKB != 446*1024 || p0.Total.RSSMaxKB != 474*1024 {
		t.Errorf("p0 Total RSS = %d/%d/%d, want 474/446/474 *1024", p0.Total.RSSMinKB, p0.Total.RSSAvgKB, p0.Total.RSSMaxKB)
	}
	if p0.Total.Samples != 2 {
		t.Errorf("p0 Total.Samples = %d, want 2", p0.Total.Samples)
	}
	// systemui 有两条状态：Persistent(35%) + Bnd Fgs(64%)
	if len(p0.States) != 2 {
		t.Fatalf("p0 States len = %d, want 2", len(p0.States))
	}
	if p0.States[0].Label != "Persistent" || p0.States[0].Percent != 35 {
		t.Errorf("p0 States[0] = %+v, want Persistent 35%%", p0.States[0])
	}
	if p0.States[1].Label != "Bnd Fgs" || p0.States[1].Percent != 64 {
		t.Errorf("p0 States[1] = %+v, want Bnd Fgs 64%%", p0.States[1])
	}
	// Bnd Fgs 行没有 RSS 数据
	if p0.States[1].RSSMinKB != 0 || p0.States[1].Samples != 0 {
		t.Errorf("p0 States[1] RSS = %d, samples=%d, want 0/0", p0.States[1].RSSMinKB, p0.States[1].Samples)
	}

	// launcher 进程有 5 个状态行
	var launcher *ProcessStat
	for i := range got.Processes {
		if got.Processes[i].Package == "com.tcl.android.launcher" {
			launcher = &got.Processes[i]
			break
		}
	}
	if launcher == nil {
		t.Fatal("launcher not found")
	}
	if len(launcher.States) != 4 {
		t.Fatalf("launcher States len = %d, want 4 (Top/Bnd Top/Bnd Fgs/Imp Fg)", len(launcher.States))
	}
	// Imp Fg: 2,2% → 2.2
	var impFg *StateEntry
	for i := range launcher.States {
		if launcher.States[i].Label == "Imp Fg" {
			impFg = &launcher.States[i]
			break
		}
	}
	if impFg == nil {
		t.Fatal("Imp Fg state not found")
	}
	if impFg.Percent != 2.2 {
		t.Errorf("Imp Fg Percent = %v, want 2.2", impFg.Percent)
	}

	// inputmethod: TOTAL=82%，Imp Bg=80%
	var ime *ProcessStat
	for i := range got.Processes {
		if got.Processes[i].Package == "com.google.android.inputmethod.latin" {
			ime = &got.Processes[i]
			break
		}
	}
	if ime == nil {
		t.Fatal("inputmethod not found")
	}
	if ime.Total.Percent != 82 {
		t.Errorf("ime Total.Percent = %v, want 82", ime.Total.Percent)
	}
	if ime.Total.RSSMaxKB != 321*1024 {
		t.Errorf("ime Total.RSSMaxKB = %d, want %d", ime.Total.RSSMaxKB, 321*1024)
	}

	// 排序验证：所有 99% 在前，82% 在后
	if got.Processes[len(got.Processes)-1].Total.Percent != 82 {
		t.Errorf("last process Percent = %v, want 82 (lowest)", got.Processes[len(got.Processes)-1].Total.Percent)
	}

	// TopN
	top3 := got.TopN(3)
	if len(top3) != 3 {
		t.Errorf("TopN(3) len = %d, want 3", len(top3))
	}
	// TopN(0) 或负数 → 全部
	if all := got.TopN(0); len(all) != len(got.Processes) {
		t.Errorf("TopN(0) len = %d, want %d", len(all), len(got.Processes))
	}
}
