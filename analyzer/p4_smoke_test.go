// Copyright 2026 Google Inc. All Rights Reserved.
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

//go:build smoke

// P4 端到端冒烟测试：用真实 bugreport 验证 4 段解析器（power/alarm/
// dumpsysactivity/procstats）在 analysisResults() 末尾被正确触发，且
// AnalysisResult 对应字段被填充。默认不跑（build tag=smoke），手动触发：
//
//	go test -tags=smoke -run TestP4Smoke -v ./analyzer/ -timeout 30m
//
// 测试自动遍历 _samples/bugreport-*.txt；目录不存在或无样本时跳过。
// 当前已验证样本（均为 reportVersion=36，Android 16/17 真机）：
//   - bugreport-T807D_EEA-CP2A.260306.002-2026-06-12-03-42-34.txt (Android 17)
//   - bugreport-T952K_EEA-BP2A.250605.031.A3-2026-07-13-22-30-48.txt (Android 16)
//   - bugreport-9185W-AP3A.240905.015.A2-2026-07-14-15-22-51.txt
//   - bugreport-T705M-AP3A.240905.015.A2-2026-07-09-09-41-54.txt
//   - bugreport-vegas_g_sys-V1VES35.22-59-1-2-2026-07-09-09-41-38.txt
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP4Smoke(t *testing.T) {
	root := sampleRoot(t)
	samples := findBugreports(t, filepath.Join(root, "_samples"))
	if len(samples) == 0 {
		t.Skipf("no bugreport-*.txt samples under %s/_samples", root)
	}

	for _, path := range samples {
		path := path // capture
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("sample bugreport not readable: %v", err)
			}
			t.Logf("loaded bugreport: %d bytes", len(data))

			results, err := Analyze(string(data))
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if len(results) == 0 {
				t.Fatal("no results")
			}
			r := results[0]
			if r.CriticalError != "" {
				t.Fatalf("CriticalError: %s", r.CriticalError)
			}

			// power 段：bugreport 应含 dumpsys power 段
			if r.PowerSummary == nil {
				t.Error("PowerSummary is nil; expected dumpsys power section")
			} else {
				t.Logf("PowerSummary: wakefulness=%s batteryLevel=%d wakelocks=%d suspendBlockers=%d drainStats=%d",
					r.PowerSummary.Wakefulness, r.PowerSummary.BatteryLevel,
					len(r.PowerSummary.WakeLocks), len(r.PowerSummary.SuspendBlockers),
					len(r.PowerSummary.BatterySaver.DrainStats))
			}

			// alarm 段：bugreport 应含 dumpsys alarm 段
			if r.AlarmSummary == nil {
				t.Error("AlarmSummary is nil; expected dumpsys alarm section")
			} else {
				t.Logf("AlarmSummary: pending=%d alarms=%d topAlarms=%d aggregateTopAlarms=%d activeUIDs=%d exactAlarmUIDs=%d",
					r.AlarmSummary.PendingAlarms, len(r.AlarmSummary.Alarms),
					len(r.AlarmSummary.TopAlarms), len(r.AlarmSummary.AggregateTopAlarms),
					len(r.AlarmSummary.ActiveUIDs), len(r.AlarmSummary.ScheduleExactAlarmUIDs))
			}

			// activity 段：bugreport 应含 dumpsys activity 段
			if r.ActivityStats == nil {
				t.Error("ActivityStats is nil; expected dumpsys activity section")
			} else {
				t.Logf("ActivityStats: anr=%d lmk=%d exits=%d running=%d totalPersistent=%d",
					len(r.ActivityStats.LastANR), len(r.ActivityStats.LMKKills),
					len(r.ActivityStats.ProcessExits), len(r.ActivityStats.RunningProcesses),
					r.ActivityStats.TotalPersistent)
			}

			// procstats 段：bugreport 应含 dumpsys procstats 段
			if r.ProcStats == nil {
				t.Error("ProcStats is nil; expected dumpsys procstats section")
			} else {
				t.Logf("ProcStats: committedFrom=%s processes=%d", r.ProcStats.CommittedFrom, len(r.ProcStats.Processes))
				if len(r.ProcStats.Processes) > 0 {
					p0 := r.ProcStats.Processes[0]
					t.Logf("ProcStats top process: pkg=%s uid=%s total=%.2f%% states=%d",
						p0.Package, p0.UID, p0.Total.Percent, len(p0.States))
				}
			}
		})
	}
}

// findBugreports 返回 dir 下所有 bugreport-*.txt 的绝对路径，按文件名升序。
func findBugreports(t *testing.T, dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasPrefix(n, "bugreport-") || !strings.HasSuffix(n, ".txt") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// sampleRoot 回溯到项目根目录（含 _samples 子目录）。
func sampleRoot(t *testing.T) string {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if info, err := os.Stat(filepath.Join(cwd, "_samples")); err == nil && info.IsDir() {
			return cwd
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			break
		}
		cwd = parent
	}
	t.Fatalf("_samples directory not found from %s", cwd)
	return ""
}

// 兜底：避免 import 未使用告警（strings 在扩展断言时使用）。
var _ = strings.TrimSpace
