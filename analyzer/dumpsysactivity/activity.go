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

// Package dumpsysactivity parses the "dumpsys activity" section of an Android
// bugreport and exposes a structured summary for MCP consumption.
//
// 包名用 dumpsysactivity 而非 activity，因为顶级 activity/ 包已被现有
// "activity manager 事件解析"（logcat 事件）占用，避免冲突。
//
// activity 段单段可达 5 万行，这里只抽取与功耗分析相关的 4 个子段：
//   - LAST ANR      → ANR 记录（卡住导致系统假死、CPU 占满）
//   - LMK KILLS     → LMK 杀进程直方图（内存压力场景）
//   - PROCESS EXIT INFO → 进程退出原因（被系统杀 / crash / ANR）
//   - RUNNING PROCESSES → 当前运行进程的 oom_adj + RSS（功耗大户定位）
package dumpsysactivity

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
	"github.com/reedhoop/ai-battery-historian/historianutils"
)

// Summary 是 dumpsys activity 段的精炼镜像。
type Summary struct {
	LastANR          []ANRRecord
	LMKTotalKills    int        // "Total number of kills: N"
	LMKKills         []LMKKill  // 按 oom_adj 阈值的累计直方图
	ProcessExits     []ProcessExit
	RunningProcesses []RunningProcess
	TotalPersistent  int        // "Total persistent processes: N"
}

// ANRRecord 对应 LAST ANR 子段的一条 ANR 记录。
// 注意：单个 bugreport 里通常只有一条 ANR（最近一次），但保留 slice
// 以兼容未来可能的多条场景。
type ANRRecord struct {
	Timestamp string // ANR trace 里 "ANR in <pkg>" 行之前的时间，若存在
	Process   string // "com.example.app (com.example.app/.MainActivity)"
	PID       int32
	UID       int32
	Package   string
	Reason    string // "Input dispatching timed out ..." 等
	FullText  string // 完整原文（截断到 4KB 防止过大）
}

// LMKKill 对应 LMK KILLS 子段的一行 "kills at or below oom_adj N: M"。
// 注意：bugreport 里 LMK 子段只有按 oom_adj 阈值的累计直方图，
// 不含单条 kill 记录（pid/uid/package 都没有），设计文档原 LMKKill
// 字段（PID/UID/Package/Reason/Timestamp/RSSKB）已相应调整为直方图字段。
type LMKKill struct {
	OomAdj int // 999, 900, 800, ..., 0
	Count  int // kills at or below this oom_adj
}

// ProcessExit 对应 PROCESS EXIT INFO 子段的一条 ApplicationExitInfo。
type ProcessExit struct {
	Timestamp string // "2026-07-13 22:15:35.343"
	PID       int32
	UID       int32
	Package   string
	Process   string // 进程名，可能含 :process 后缀
	Reason    string // "OTHER KILLS BY SYSTEM" / "SIGNALED" / "PACKAGE UPDATED" 等
	Subreason string // "TOO MANY EMPTY PROCS" / "UNKNOWN" 等
	ExitCode  int    // status 字段，0 表示被系统杀而非信号退出
	RSSKB     int64  // "rss=96MB" → 96*1024
}

// RunningProcess 对应 RUNNING PROCESSES 子段的一条 ProcessRecord。
type RunningProcess struct {
	PID         int32
	UID         int32
	Package     string
	Persistent  bool   // *PERS* 还是 *APP*
	OomAdj      int    // "oom adj: max=-800 curRaw=-800 ..." 里的 cur 字段
	ProcState   int    // "curProcState=0" 等（0=TOP, 6=CACHED 等）
	RSSKB       int64  // "lastRss=83MB" → 83*1024
}

// Parse 解析 bugreport 全文，返回 dumpsys activity 段的结构化摘要。
// 段不存在时返回 (nil, nil)；段存在但解析失败返回 (nil, err)。
func Parse(raw string) (*Summary, error) {
	section := bugreportutils.ExtractServiceDump(raw, "activity")
	if section == "" {
		return nil, nil
	}
	lines := strings.Split(section, "\n")

	// 切分子段，记录每个子段的 [startLine, endLine) 范围
	subsections := splitSubsections(lines)

	s := &Summary{}
	for _, sub := range subsections {
		body := lines[sub.start:sub.end]
		switch sub.name {
		case "lastanr":
			s.LastANR = parseLastANR(body)
		case "lmk":
			s.LMKTotalKills, s.LMKKills = parseLMK(body)
		case "exit-info":
			s.ProcessExits = parseProcessExits(body)
		case "processes":
			s.RunningProcesses = parseRunningProcesses(body)
		}
	}

	// Total persistent processes: N 单独扫一遍（位于 processes 子段末尾，
	// 但跨多行不好定位，干脆全文扫描）
	s.TotalPersistent = findTotalPersistent(lines)

	return s, nil
}

// subsection 表示一个 dumpsys activity 子段的范围。
type subsection struct {
	name       string
	start, end int // [start, end)
}

// splitSubsections 用 ActivitySubsectionRE 切分子段，返回每个子段
// 在 lines 里的 [start, end) 行号范围（start 是子段标题行 +1，end
// 是下一个子段标题行或文件末尾）。
func splitSubsections(lines []string) []subsection {
	var out []subsection
	type marker struct {
		line int
		name string
	}
	var markers []marker
	for i, line := range lines {
		if m, r := historianutils.SubexpNames(bugreportutils.ActivitySubsectionRE, line); m {
			markers = append(markers, marker{line: i, name: r["subsection"]})
		}
	}
	for i, mk := range markers {
		end := len(lines)
		if i+1 < len(markers) {
			end = markers[i+1].line
		}
		out = append(out, subsection{
			name:  mk.name,
			start: mk.line + 1, // 跳过标题行
			end:   end,
		})
	}
	return out
}

var (
	// "ANR in com.example.app (com.example.app/.MainActivity)"
	anrHeadRE = regexp.MustCompile(`^ANR in (?P<pkg>\S+) \((?P<proc>\S+)\)`)
	// "  PID: 12345"
	anrPidRE = regexp.MustCompile(`^\s*PID:\s*(?P<pid>\d+)`)
	// "  Reason: Input dispatching timed out ..."
	anrReasonRE = regexp.MustCompile(`^\s*Reason:\s*(?P<reason>.+)`)
	// LMK: "  Total number of kills: 2"
	lmkTotalRE = regexp.MustCompile(`Total number of kills:\s*(?P<n>\d+)`)
	// LMK: "    kills at or below oom_adj 999: 2"
	lmkHistRE = regexp.MustCompile(`kills at or below oom_adj\s+(?P<adj>\d+):\s*(?P<n>\d+)`)
	// Exit: "  package: com.google.android.apps.restore"
	exitPkgRE = regexp.MustCompile(`^\s*package:\s*(?P<pkg>\S+)`)
	// Exit: "    Historical Process Exit for uid=10173"
	exitUidRE = regexp.MustCompile(`Historical Process Exit for uid=(?P<uid>\d+)`)
	// Exit: "          timestamp=2026-07-13 22:15:35.343 pid=8952 realUid=10173 packageUid=10173 definingUid=10173 user=0"
	exitTsRE = regexp.MustCompile(`timestamp=(?P<ts>\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+)\s+pid=(?P<pid>\d+)\s+realUid=(?P<uid>\d+)`)
	// Exit: "          process=com.google.android.apps.restore reason=13 (OTHER KILLS BY SYSTEM) subreason=3 (TOO MANY EMPTY PROCS) status=0"
	// 或 "          process=X reason=11 (APP CRASH(EXCEPTION)) subreason=0 (UNKNOWN) status=0"  (Android 17+ 含嵌套括号)
	// reason/subreason 用 .*? 非贪婪，配合后续 "\)\s+subreason=" / "\)\s+status=" 锚定，
	// 正确处理 reason 名称含嵌套括号的情况（如 "APP CRASH(EXCEPTION)"）。
	exitReasonRE = regexp.MustCompile(`process=(?P<proc>\S+)\s+reason=\d+\s+\((?P<reason>.*?)\)\s+subreason=\d+\s+\((?P<subreason>.*?)\)\s+status=(?P<status>-?\d+)`)
	// Exit: "          importance=400 pss=0.00 rss=96MB description=too many empty state=empty trace=null"
	// 或 "          importance=400 pss=0.00 rss=0.00 description=..."  (< 1MB 无单位)
	// 或 "          importance=400 pss=0.00 rss=1.5GB description=..."  (≥ 1GB)
	// 单位可选：无单位（<1MB，实际为 0）/ KB / MB / GB。
	exitRssRE = regexp.MustCompile(`rss=(?P<rss>[\d.,]+)(?P<unit>MB|GB|KB)?`)
	// Running: "  *PERS* UID 1000 ProcessRecord{961ff16 4618:com.tct.iris/1000}"
	// 或 "  *APP* UID 10078 ProcessRecord{2ec3311 23392:com.tcl.ai.ability/u0a78}"
	procHeadRE = regexp.MustCompile(`^\s*\*(?P<kind>PERS|APP)\*\s+UID\s+(?P<uid>\d+)\s+ProcessRecord\{[^}]*\s(?P<pid>\d+):(?P<pkg>[^/]+)/(?P<pu>\S+)\}`)
	// Running: "    oom adj: max=-800 curRaw=-800 setRaw=-800 cur=-800 set=-800"
	procOomRE = regexp.MustCompile(`oom adj:.*cur=(?P<cur>-?\d+)`)
	// Running: "    curProcState=0 mRepProcState=0 ..."
	procStateRE = regexp.MustCompile(`curProcState=(?P<state>-?\d+)`)
	// Running: "    lastRss=83MB lastCachedRss=0.00"
	// 单位可选：无单位（<1MB）/ KB / MB / GB。
	procRssRE = regexp.MustCompile(`lastRss=(?P<rss>[\d.,]+)(?P<unit>MB|GB|KB)?`)
	// "  Total persistent processes: 10"
	totalPersRE = regexp.MustCompile(`Total persistent processes:\s*(?P<n>\d+)`)
)

// parseLastANR 解析 LAST ANR 子段。空段（"<no ANR has occurred since boot>"）返回 nil。
// 非空段把整个 trace 文本作为单条 ANRRecord 返回，提取 PID/Reason/Package。
// FullText 截断到 4KB 防止单条 ANR trace 过大撑爆 token。
func parseLastANR(body []string) []ANRRecord {
	// 找到 "ANR in ..." 行作为起点
	startIdx := -1
	for i, line := range body {
		if anrHeadRE.MatchString(line) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		// 没找到 "ANR in"，可能是 "<no ANR has occurred since boot>" 等空段标记
		return nil
	}
	fullText := strings.Join(body[startIdx:], "\n")
	rec := ANRRecord{FullText: truncateText(fullText, 4096)}
	if m, r := historianutils.SubexpNames(anrHeadRE, body[startIdx]); m {
		rec.Package = r["pkg"]
		rec.Process = r["proc"]
	}
	// 在 trace 里扫 PID / Reason
	for _, line := range body[startIdx:] {
		if m, r := historianutils.SubexpNames(anrPidRE, line); m {
			pid, _ := strconv.Atoi(r["pid"])
			rec.PID = int32(pid)
		}
		if m, r := historianutils.SubexpNames(anrReasonRE, line); m {
			rec.Reason = strings.TrimSpace(r["reason"])
		}
	}
	return []ANRRecord{rec}
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n...[truncated]"
}

// parseLMK 解析 LMK KILLS 子段，返回 (totalKills, histogram)。
// 直方图按 oom_adj 阈值降序（999 在前，0 在后）。
func parseLMK(body []string) (int, []LMKKill) {
	total := 0
	var hist []LMKKill
	for _, line := range body {
		if m, r := historianutils.SubexpNames(lmkTotalRE, line); m {
			total, _ = strconv.Atoi(r["n"])
			continue
		}
		if m, r := historianutils.SubexpNames(lmkHistRE, line); m {
			adj, _ := strconv.Atoi(r["adj"])
			n, _ := strconv.Atoi(r["n"])
			hist = append(hist, LMKKill{OomAdj: adj, Count: n})
		}
	}
	return total, hist
}

// parseProcessExits 解析 PROCESS EXIT INFO 子段。
// 格式：
//   package: <pkg>
//     Historical Process Exit for uid=<uid>
//       ApplicationExitInfo #0:
//         timestamp=... pid=... realUid=...
//         process=... reason=N (NAME) subreason=N (NAME) status=N
//         importance=N pss=... rss=NMB description=... state=... trace=...
//       ApplicationExitInfo #1:
//         ...
func parseProcessExits(body []string) []ProcessExit {
	var out []ProcessExit
	var current *ProcessExit
	var currentPkg string
	var currentUID int32
	for _, line := range body {
		if m, r := historianutils.SubexpNames(exitPkgRE, line); m {
			currentPkg = r["pkg"]
			continue
		}
		if m, r := historianutils.SubexpNames(exitUidRE, line); m {
			uid, _ := strconv.Atoi(r["uid"])
			currentUID = int32(uid)
			continue
		}
		// 新一条 exit 记录开始
		if m, r := historianutils.SubexpNames(exitTsRE, line); m {
			if current != nil {
				out = append(out, *current)
			}
			pid, _ := strconv.Atoi(r["pid"])
			uid, _ := strconv.Atoi(r["uid"])
			current = &ProcessExit{
				Timestamp: r["ts"],
				PID:       int32(pid),
				UID:       int32(uid),
				Package:   currentPkg,
			}
			// 若 exitUidRE 的 uid 与 realUid 不一致，优先用 realUid
			if currentUID != 0 && current.UID == 0 {
				current.UID = currentUID
			}
			continue
		}
		if current == nil {
			continue
		}
		if m, r := historianutils.SubexpNames(exitReasonRE, line); m {
			current.Process = r["proc"]
			current.Reason = r["reason"]
			current.Subreason = r["subreason"]
			status, _ := strconv.Atoi(r["status"])
			current.ExitCode = status
			continue
		}
		if m, r := historianutils.SubexpNames(exitRssRE, line); m {
			current.RSSKB = parseSizeToKB(r["rss"], r["unit"])
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

// parseRunningProcesses 解析 RUNNING PROCESSES 子段。
// 每条记录以 "*PERS* / *APP* UID N ProcessRecord{...}" 开头，后续若干行
// 含 oom adj / curProcState / lastRss 等。
func parseRunningProcesses(body []string) []RunningProcess {
	var out []RunningProcess
	var current *RunningProcess
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for _, line := range body {
		if m, r := historianutils.SubexpNames(procHeadRE, line); m {
			flush()
			uid, _ := strconv.Atoi(r["uid"])
			pid, _ := strconv.Atoi(r["pid"])
			current = &RunningProcess{
				PID:        int32(pid),
				UID:        int32(uid),
				Package:    r["pkg"],
				Persistent: r["kind"] == "PERS",
			}
			continue
		}
		if current == nil {
			continue
		}
		if m, r := historianutils.SubexpNames(procOomRE, line); m {
			cur, _ := strconv.Atoi(r["cur"])
			current.OomAdj = cur
		}
		if m, r := historianutils.SubexpNames(procStateRE, line); m {
			st, _ := strconv.Atoi(r["state"])
			current.ProcState = st
		}
		if m, r := historianutils.SubexpNames(procRssRE, line); m {
			current.RSSKB = parseSizeToKB(r["rss"], r["unit"])
		}
	}
	flush()
	return out
}

// findTotalPersistent 在全文里搜 "Total persistent processes: N"。
func findTotalPersistent(lines []string) int {
	for _, line := range lines {
		if m, r := historianutils.SubexpNames(totalPersRE, line); m {
			n, _ := strconv.Atoi(r["n"])
			return n
		}
	}
	return 0
}

// parseSizeToKB 把 "0.00" / "5.5MB" / "96MB" / "1.5GB" 形式的尺寸值转成 KB。
//   - 无单位：< 1MB 的小值（实际为 0），按 bytes 处理除以 1024
//   - KB：直接取值
//   - MB：×1024
//   - GB：×1024*1024
// 兼容 en_US locale（"."）和欧式 locale（","）。
func parseSizeToKB(v, unit string) int64 {
	s := strings.ReplaceAll(v, ",", ".")
	f, _ := strconv.ParseFloat(s, 64)
	switch unit {
	case "GB":
		return int64(f * 1024 * 1024)
	case "MB":
		return int64(f * 1024)
	case "KB":
		return int64(f)
	default:
		// 无单位 = bytes（< 1MB 的小值），转 KB
		return int64(f / 1024)
	}
}
