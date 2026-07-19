// Copyright 2026 reedhoop. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package bugreportutils 的段提取基础设施回归测试。
//
// 这些正则与提取函数是整个 bugreport 解析管道最脆弱、最被多处复用的
// 接缝（power / alarm / procstats / activity 四个扩展分析器都通过
// ExtractServiceDump 取段），此前没有任何测试覆盖。本文件用内联合成
// 输入锁定其行为，不依赖被 .gitignore 忽略的 _aosp_review 样本，确保
// 在干净 clone 下也能运行。
package bugreportutils

import (
	"regexp"
	"strings"
	"testing"

	"github.com/reedhoop/ai-battery-historian/historianutils"
)

// matchGroup 用命名分组正则匹配单行，返回是否匹配与指定分组值。
// 复用 historianutils.SubexpNames（它会先 TrimSpace，与线上调用一致）。
func matchGroup(t *testing.T, re *regexp.Regexp, line, group string) (bool, string) {
	t.Helper()
	if m, result := historianutils.SubexpNames(re, line); m {
		return true, result[group]
	}
	return false, ""
}

// TestBugReportSectionRE 锁定章节分隔符 `------ X ------` 的匹配与捕获，
// 含闭合端 5/6 短横的容差（AOSP 输出 6 个，正则闭合只写 5 个）。
func TestBugReportSectionRE(t *testing.T) {
	cases := []struct {
		line  string
		want  string // 期望 section 分组；空且 match=false 表示期望不匹配
		match bool
	}{
		{"------ SYSTEM LOG (logcat) ------", "SYSTEM LOG (logcat)", true},
		{"------ DUMPSYS CRITICAL (/system/bin/dumpsys) ------", "DUMPSYS CRITICAL (/system/bin/dumpsys)", true},
		{"------ DUMP BLOCK STAT ------", "DUMP BLOCK STAT", true},   // 闭合 6 短横
		{"------ DUMP BLOCK STAT -----", "DUMP BLOCK STAT", true},    // 闭合 5 短横（容差）
		{"------  CHECKIN BATTERYSTATS  ------", "CHECKIN BATTERYSTATS", true}, // 标题内含多余空格
		{"SYSTEM LOG (logcat)", "", false},
	}
	for _, c := range cases {
		got, val := matchGroup(t, BugReportSectionRE, c.line, "section")
		if got != c.match {
			t.Errorf("BugReportSectionRE.MatchString(%q) = %v, want %v", c.line, got, c.match)
			continue
		}
		if c.match && val != c.want {
			t.Errorf("BugReportSectionRE section of %q = %q, want %q", c.line, val, c.want)
		}
	}
}

// TestDumpstateRE 锁定文件头时间戳 `== dumpstate: <ts>` 的匹配与捕获。
func TestDumpstateRE(t *testing.T) {
	cases := []struct {
		line  string
		want  string
		match bool
	}{
		{"== dumpstate: 2026-07-19 12:34:56", "2026-07-19 12:34:56", true},
		{"== dumpstate: 2026-07-19 12:34:56 (some extra)", "2026-07-19 12:34:56", true}, // 后接附加文本
		{"== dumpstate:2026-07-19 12:34:56", "", false}, // 冒号后缺空格
		{"dumpstate: 2026-07-19 12:34:56", "", false},    // 缺前导 ==
	}
	for _, c := range cases {
		got, val := matchGroup(t, DumpstateRE, c.line, "timestamp")
		if got != c.match {
			t.Errorf("DumpstateRE.MatchString(%q) = %v, want %v", c.line, got, c.match)
			continue
		}
		if c.match && val != c.want {
			t.Errorf("DumpstateRE timestamp of %q = %q, want %q", c.line, val, c.want)
		}
	}
}

// TestActivitySubsectionRE 锁定 activity 段内子段 `ACTIVITY MANAGER ... (dumpsys activity X)`。
func TestActivitySubsectionRE(t *testing.T) {
	cases := []struct {
		line  string
		want  string
		match bool
	}{
		{"ACTIVITY MANAGER LAST ANR (dumpsys activity lastanr)", "lastanr", true},
		{"ACTIVITY MANAGER SETTINGS (dumpsys activity settings)", "settings", true},
		{"ACTIVITY MANAGER BINDER PROXY STATE (dumpsys activity binder-proxies)", "binder-proxies", true},
		{"ACTIVITY MANAGER ACTIVITIES (dumpsys activity activities)", "activities", true},
		{"POWER MANAGER (dumpsys power)", "", false},
	}
	for _, c := range cases {
		got, val := matchGroup(t, ActivitySubsectionRE, c.line, "subsection")
		if got != c.match {
			t.Errorf("ActivitySubsectionRE.MatchString(%q) = %v, want %v", c.line, got, c.match)
			continue
		}
		if c.match && val != c.want {
			t.Errorf("ActivitySubsectionRE subsection of %q = %q, want %q", c.line, val, c.want)
		}
	}
}

// TestIsBugReport 锁定合法 bugreport 必须同时具备三个标记：dumpstate 时间戳、
// build fingerprint、至少一个 `------ ... ------` 章节头。任缺其一即非 bugreport。
func TestIsBugReport(t *testing.T) {
	valid := strings.Join([]string{
		"== dumpstate: 2026-07-19 12:34:56",
		"Build fingerprint: 'google/foo/bar:15/AP1A.1/1234:user/release-keys'",
		"------ SYSTEM LOG (logcat) ------",
		"some log content",
	}, "\n")
	if !IsBugReport([]byte(valid)) {
		t.Error("IsBugReport(valid) = false, want true")
	}

	dropDumpstate := strings.Join([]string{
		"Build fingerprint: 'google/foo/bar:15/AP1A.1/1234:user/release-keys'",
		"------ SYSTEM LOG (logcat) ------",
		"some log content",
	}, "\n")
	if IsBugReport([]byte(dropDumpstate)) {
		t.Error("IsBugReport(missing dumpstate) = true, want false")
	}

	dropFingerprint := strings.Join([]string{
		"== dumpstate: 2026-07-19 12:34:56",
		"------ SYSTEM LOG (logcat) ------",
		"some log content",
	}, "\n")
	if IsBugReport([]byte(dropFingerprint)) {
		t.Error("IsBugReport(missing fingerprint) = true, want false")
	}

	dropSection := strings.Join([]string{
		"== dumpstate: 2026-07-19 12:34:56",
		"Build fingerprint: 'google/foo/bar:15/AP1A.1/1234:user/release-keys'",
		"some log content without section header",
	}, "\n")
	if IsBugReport([]byte(dropSection)) {
		t.Error("IsBugReport(missing section) = true, want false")
	}

	if IsBugReport([]byte("totally unrelated text")) {
		t.Error("IsBugReport(unrelated) = true, want false")
	}
}

// TestExtractServiceDump 锁定服务段提取的 golden 行为：
//   - CRITICAL 前缀的 power 段能被正确识别并提取正文
//   - 段在遇到下一个 `DUMP OF SERVICE` 头时结束（不泄漏后续段）
//   - 不存在的服务返回空
//   - 同名多段（无前缀 power 与 CRITICAL power）会被合并收集（见下方说明）
func TestExtractServiceDump(t *testing.T) {
	const input = `DUMP OF SERVICE CRITICAL power:
POWER MANAGER (dumpsys power)
  mWakefulness=Awake
  mBatteryLevel=43
DUMP OF SERVICE CRITICAL sensorservice:
Sensor List:
  Handle: 1
DUMP OF SERVICE alarm:
Current Alarm Manager state:
`

	// CRITICAL 前缀的 power 段
	got := ExtractServiceDump(input, "power")
	if !strings.Contains(got, "POWER MANAGER (dumpsys power)") {
		t.Errorf("power body missing POWER MANAGER header:\n%s", got)
	}
	if !strings.Contains(got, "mBatteryLevel=43") {
		t.Errorf("power body missing battery level:\n%s", got)
	}
	if strings.Contains(got, "DUMP OF SERVICE") {
		t.Errorf("power body must not contain service header:\n%s", got)
	}
	if strings.Contains(got, "Sensor List") {
		t.Errorf("power body leaked into sensorservice section:\n%s", got)
	}
	if strings.Contains(got, "Current Alarm Manager state") {
		t.Errorf("power body leaked into alarm section:\n%s", got)
	}

	// 无前缀 alarm 段
	alarm := ExtractServiceDump(input, "alarm")
	if !strings.Contains(alarm, "Current Alarm Manager state") {
		t.Errorf("alarm body missing header:\n%s", alarm)
	}
	if strings.Contains(alarm, "DUMP OF SERVICE") {
		t.Errorf("alarm body must not contain service header:\n%s", alarm)
	}

	// 不存在的服务 → 空
	if s := ExtractServiceDump(input, "procstats"); s != "" {
		t.Errorf("ExtractServiceDump(procstats) = %q, want empty", s)
	}

	// 同名多段合并行为：第一个无前缀 power 与后面的 CRITICAL power 都会被收集，
	// 直到出现不同名的服务头才停止。
	// 注意：函数文档注释声称“返回第一个匹配段”，但实现实际会合并同名段，
	// 此处锁定真实行为，避免后续被无意改坏；若需改为“仅首个”，应同步修正注释。
	const dupInput = `DUMP OF SERVICE power:
plain-body
DUMP OF SERVICE CRITICAL power:
critical-body
DUMP OF SERVICE alarm:
alarm-body
`
	dup := ExtractServiceDump(dupInput, "power")
	if !strings.Contains(dup, "plain-body") || !strings.Contains(dup, "critical-body") {
		t.Errorf("same-name merge: expected both plain and critical bodies, got:\n%s", dup)
	}
	if strings.Contains(dup, "alarm-body") {
		t.Errorf("same-name merge leaked alarm body:\n%s", dup)
	}
}

// TestExtractBatterystatsCheckin 锁定 CHECKIN BATTERYSTATS 段提取：仅收集
// 该段内的 checkin 行，遇到下一个 `------ ... ------` 章节头即停止（即使
// 后面还有同名段也不会触及）。
func TestExtractBatterystatsCheckin(t *testing.T) {
	const input = `some header line
------ CHECKIN BATTERYSTATS ------
9,0,i,vers,123
9,0,l,battery,
inside-checkin line
------ DUMPSTATE (other) ------
not included
------ CHECKIN BATTERYSTATS ------
9,0,i,vers,999
`
	got := ExtractBatterystatsCheckin(input)
	want := "9,0,i,vers,123\n9,0,l,battery,\ninside-checkin line"
	if got != want {
		t.Errorf("ExtractBatterystatsCheckin =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "not included") {
		t.Error("checkin extraction leaked outside section")
	}

	// 无对应段 → 空
	if s := ExtractBatterystatsCheckin("no checkin here\njust text"); s != "" {
		t.Errorf("ExtractBatterystatsCheckin(no section) = %q, want empty", s)
	}
}
