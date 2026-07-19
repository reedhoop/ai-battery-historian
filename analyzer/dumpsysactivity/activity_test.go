// Copyright 2026 reedhoop. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package dumpsysactivity

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
	raw := loadSample(t, "activity_sample.txt")
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil Summary")
	}

	// LastANR
	if len(got.LastANR) != 1 {
		t.Fatalf("LastANR len = %d, want 1", len(got.LastANR))
	}
	anr := got.LastANR[0]
	if anr.Package != "com.example.app" {
		t.Errorf("ANR Package = %q", anr.Package)
	}
	if anr.Process != "com.example.app/.MainActivity" {
		t.Errorf("ANR Process = %q", anr.Process)
	}
	if anr.PID != 12345 {
		t.Errorf("ANR PID = %d, want 12345", anr.PID)
	}
	if anr.Reason != "Input dispatching timed out (com.example.app/com.example.app.MainActivity (Server) is not responding. Waited 5000ms for FocusEvent)" {
		t.Errorf("ANR Reason = %q", anr.Reason)
	}
	if anr.FullText == "" {
		t.Error("ANR FullText is empty")
	}

	// LMK
	if got.LMKTotalKills != 2 {
		t.Errorf("LMKTotalKills = %d, want 2", got.LMKTotalKills)
	}
	if len(got.LMKKills) != 12 {
		t.Fatalf("LMKKills len = %d, want 12", len(got.LMKKills))
	}
	// 第一条 oom_adj=999, count=2
	if got.LMKKills[0].OomAdj != 999 || got.LMKKills[0].Count != 2 {
		t.Errorf("LMKKills[0] = %+v, want {999, 2}", got.LMKKills[0])
	}
	// 第二条 oom_adj=900, count=0
	if got.LMKKills[1].OomAdj != 900 || got.LMKKills[1].Count != 0 {
		t.Errorf("LMKKills[1] = %+v, want {900, 0}", got.LMKKills[1])
	}

	// ProcessExits
	if len(got.ProcessExits) != 3 {
		t.Fatalf("ProcessExits len = %d, want 3", len(got.ProcessExits))
	}
	pe0 := got.ProcessExits[0]
	if pe0.Package != "com.google.android.apps.restore" {
		t.Errorf("pe0 Package = %q", pe0.Package)
	}
	if pe0.PID != 8952 || pe0.UID != 10173 {
		t.Errorf("pe0 PID/UID = %d/%d, want 8952/10173", pe0.PID, pe0.UID)
	}
	if pe0.Timestamp != "2026-07-13 22:15:35.343" {
		t.Errorf("pe0 Timestamp = %q", pe0.Timestamp)
	}
	if pe0.Reason != "OTHER KILLS BY SYSTEM" || pe0.Subreason != "TOO MANY EMPTY PROCS" {
		t.Errorf("pe0 Reason/Subreason = %q/%q", pe0.Reason, pe0.Subreason)
	}
	if pe0.ExitCode != 0 {
		t.Errorf("pe0 ExitCode = %d, want 0", pe0.ExitCode)
	}
	if pe0.RSSKB != 96*1024 {
		t.Errorf("pe0 RSSKB = %d, want %d", pe0.RSSKB, 96*1024)
	}
	// 第二条 reason=SIGNALED status=9
	pe1 := got.ProcessExits[1]
	if pe1.Reason != "SIGNALED" || pe1.ExitCode != 9 {
		t.Errorf("pe1 = %+v, want Reason=SIGNALED ExitCode=9", pe1)
	}
	if pe1.RSSKB != 93*1024 {
		t.Errorf("pe1 RSSKB = %d, want %d", pe1.RSSKB, 93*1024)
	}
	// 第三条跨 package
	pe2 := got.ProcessExits[2]
	if pe2.Package != "com.google.android.inputmethod.latin" {
		t.Errorf("pe2 Package = %q", pe2.Package)
	}
	if pe2.Reason != "PACKAGE UPDATED" {
		t.Errorf("pe2 Reason = %q", pe2.Reason)
	}
	if pe2.RSSKB != 254*1024 {
		t.Errorf("pe2 RSSKB = %d, want %d", pe2.RSSKB, 254*1024)
	}

	// RunningProcesses
	if len(got.RunningProcesses) != 4 {
		t.Fatalf("RunningProcesses len = %d, want 4", len(got.RunningProcesses))
	}
	rp0 := got.RunningProcesses[0]
	if rp0.PID != 4618 || rp0.UID != 1000 {
		t.Errorf("rp0 PID/UID = %d/%d, want 4618/1000", rp0.PID, rp0.UID)
	}
	if rp0.Package != "com.tct.iris" {
		t.Errorf("rp0 Package = %q", rp0.Package)
	}
	if !rp0.Persistent {
		t.Error("rp0 Persistent = false, want true")
	}
	if rp0.OomAdj != -800 {
		t.Errorf("rp0 OomAdj = %d, want -800", rp0.OomAdj)
	}
	if rp0.ProcState != 0 {
		t.Errorf("rp0 ProcState = %d, want 0", rp0.ProcState)
	}
	if rp0.RSSKB != 83*1024 {
		t.Errorf("rp0 RSSKB = %d, want %d", rp0.RSSKB, 83*1024)
	}
	// 第三条 *APP* 非持久
	rp2 := got.RunningProcesses[2]
	if rp2.Persistent {
		t.Error("rp2 Persistent = true, want false")
	}
	if rp2.Package != "com.tcl.ai.ability" {
		t.Errorf("rp2 Package = %q", rp2.Package)
	}
	if rp2.OomAdj != 100 {
		t.Errorf("rp2 OomAdj = %d, want 100", rp2.OomAdj)
	}
	if rp2.ProcState != 6 {
		t.Errorf("rp2 ProcState = %d, want 6", rp2.ProcState)
	}
	// 第四条 oom_adj=0 / ProcState=2 / RSS=234MB
	rp3 := got.RunningProcesses[3]
	if rp3.OomAdj != 0 {
		t.Errorf("rp3 OomAdj = %d, want 0", rp3.OomAdj)
	}
	if rp3.ProcState != 2 {
		t.Errorf("rp3 ProcState = %d, want 2", rp3.ProcState)
	}
	if rp3.RSSKB != 234*1024 {
		t.Errorf("rp3 RSSKB = %d, want %d", rp3.RSSKB, 234*1024)
	}

	// TotalPersistent
	if got.TotalPersistent != 10 {
		t.Errorf("TotalPersistent = %d, want 10", got.TotalPersistent)
	}
}
