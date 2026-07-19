// Copyright 2026 reedho. All Rights Reserved.
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

// Package alarm parses the "dumpsys alarm" section of an Android bugreport and
// exposes a structured summary for MCP consumption.
//
// 与 batterystats 的聚合 alarm 统计不同，这里给出的是 alarm 段 "pending
// alarms" 队列的实时快照——下一次会触发的是谁、什么时候触发、是哪个包设
// 的、是否唤醒型。OEM 功耗测试场景里，这正是排查"为什么设备每 5 分钟
// 醒一次"最直接的入口。
package alarm

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
	"github.com/reedhoop/ai-battery-historian/historianutils"
)

// Summary 是 dumpsys alarm 段的结构化镜像。
type Summary struct {
	NowRTC              int64
	NowElapsed          int64
	RuntimeStartedISO   string // "2026-07-13 17:53:27.030"
	RuntimeUptimeMs     int64  // "Runtime uptime (elapsed): +4h38m12s216ms" → 毫秒
	PendingAlarms       int    // "69 pending alarms:"
	Alarms              []Alarm
	// TopAlarms 是 pending 队列按 repeatInterval 降序 + seq 升序取前 N（默认 20）。
	// 注意：这与 AOSP dumpsys alarm "Top Alarms:" 段语义不同——
	// AOSP 的 "Top Alarms:" 是已触发 alarm 按 aggregateTime（running 总时长）降序排名，
	// 见 AggregateTopAlarms 字段。本字段保留此命名是为向后兼容旧 MCP 客户端。
	TopAlarms              []Alarm
	AggregateTopAlarms     []AggregateTopAlarm // AOSP "Top Alarms:" 段，按 running 时长降序的已触发 alarm 排名
	NextWakeupAlarm        string              // 原始行
	NextNonWakeupAlarm     string
	LastWakeup             string
	ForceAllAppsStandby    bool
	ActiveUIDs             []int32
	ScheduleExactAlarmUIDs []int32
}

// AggregateTopAlarm 对应 AOSP dumpsys alarm "Top Alarms:" 子段的一条记录。
// 与 TopAlarms（pending 队列按 repeatInterval 排序）不同，这里是已触发
// alarm 按 aggregateTime（running 总时长）降序的排名，直接反映"哪个 alarm
// 最耗 CPU"。每条记录占 2 行：
//
//	+2s531ms running, 15 wakeups, 15 alarms: 1000:android
//	  *walarm*:com.tcl.server.power.action.APP_POWER_MONITOR_TICK
type AggregateTopAlarm struct {
	RunningRel string // "+2s531ms"
	RunningMs  int64  // 解析后的毫秒数
	Wakeups    int
	Alarms     int
	UIDPkg     string // "1000:android"（uid:package）
	Tag        string // "*walarm*:com.tcl.server.power.action.APP_POWER_MONITOR_TICK"
}

// Alarm 对应一条 "ELAPSED_WAKEUP #N: ..." 记录。
type Alarm struct {
	Seq              int    // 序号 #1, #2, ...
	Type             string // "ELAPSED_WAKEUP" / "ELAPSED" / "RTC_WAKEUP" / "RTC"
	Tag              string // tag=*alarm*:TIME_TICK 等
	PackageName      string // com.android.nfc 等
	WhenElapsedMs    int64  // 触发时间（elapsed 毫秒，绝对值，来自 Alarm{...} 行）
	WhenElapsedRel   string // "+20s754ms" 相对时间原文（保留可读性）
	RepeatIntervalMs int64  // 0=非重复，60000=每分钟
	Flags            int    // flags=0x8 等
	WindowMs         int64  // window=+7s500ms → 7500；window=0 → 0
	ExactAllowReason string // "allow-listed" / "listener" / ""
	Operation        string // PendingIntent 目标，如 "com.android.nfc broadcastIntent"
	Listener         string // listener 引用
}

// Parse 解析 bugreport 全文，返回 dumpsys alarm 段的结构化摘要。
// 段不存在时返回 (nil, nil)；段存在但解析失败返回 (nil, err)。
func Parse(raw string) (*Summary, error) {
	section := bugreportutils.ExtractServiceDump(raw, "alarm")
	if section == "" {
		return nil, nil
	}
	s := &Summary{}
	lines := strings.Split(section, "\n")

	// 顶层单行字段
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "nowRTC="):
			s.NowRTC, s.NowElapsed = parseNowLine(trim)
		case strings.HasPrefix(trim, "RuntimeStarted="):
			s.RuntimeStartedISO = afterEq(trim)
		case strings.HasPrefix(trim, "Runtime uptime (elapsed):"):
			s.RuntimeUptimeMs = parseDurationMs(afterColon(trim))
		case strings.HasPrefix(trim, "Next wakeup alarm:"):
			s.NextWakeupAlarm = trim
		case strings.HasPrefix(trim, "Next non-wakeup alarm:"):
			s.NextNonWakeupAlarm = trim
		case strings.HasPrefix(trim, "Last wakeup:"):
			s.LastWakeup = trim
		case strings.HasPrefix(trim, "Force all apps standby:"):
			s.ForceAllAppsStandby = afterColon(trim) == "true"
		case strings.HasPrefix(trim, "Active uids:"):
			s.ActiveUIDs = parseUIDList(afterColon(trim))
		case strings.HasPrefix(trim, "App ids requesting SCHEDULE_EXACT_ALARM:"):
			s.ScheduleExactAlarmUIDs = parseUIDList(strings.TrimPrefix(afterColon(trim), " "))
		}
	}

	// pending alarms 列表
	s.PendingAlarms, s.Alarms = parsePendingAlarms(lines)
	s.TopAlarms = computeTopAlarms(s.Alarms, 20)
	s.AggregateTopAlarms = parseAggregateTopAlarms(lines)
	return s, nil
}

var (
	// nowRTC=1783974699246=2026-07-13 22:31:39.246 nowELAPSED=16727112
	nowLineRE = regexp.MustCompile(`nowRTC=(?P<rtc>\d+).*nowELAPSED=(?P<elapsed>\d+)`)
	// ELAPSED_WAKEUP #1: Alarm{12d12ec type 2 origWhen 16736921 whenElapsed 16736921 android}
	alarmHeadRE = regexp.MustCompile(`(?P<type>ELAPSED_WAKEUP|ELAPSED|RTC_WAKEUP|RTC)\s+#(?P<seq>\d+):\s+Alarm\{[^}]*\swhenElapsed\s(?P<when>\d+)\s(?P<pkg>\S+)\}`)
	// tag=*walarm*:WriteBufferAlarm
	tagRE = regexp.MustCompile(`^\s*tag=(?P<tag>\S+)`)
	// type=ELAPSED origWhen=+25s754ms window=+45s0ms repeatInterval=60000 count=0 flags=0x8
	// 或 type=RTC_WAKEUP origWhen=2026-07-13 23:00:00.000 window=0 exactAllowReason=policy_permission repeatInterval=0 count=0 flags=0x9
	// 注意：RTC 类型的 origWhen 是 "YYYY-MM-DD HH:MM:SS.mmm" 形式，含空格，
	// 因此用惰性匹配 (?P<ow>.+?) 配合 " window=" 边界，避免错位。
	alarmTypeRE = regexp.MustCompile(`^\s*type=(?P<type>\S+)\s+origWhen=(?P<ow>.+?)\s+window=(?P<win>\S+)(?:\s+exactAllowReason=(?P<ear>\S+))?\s+repeatInterval=(?P<ri>\d+)\s+count=(?P<c>\d+)\s+flags=(?P<flags>0x[0-9a-fA-F]+)`)
	// whenElapsed=+9s809ms maxWhenElapsed=+17s309ms
	whenElapsedRE = regexp.MustCompile(`^\s*whenElapsed=(?P<we>\S+)\s+maxWhenElapsed=(?P<mwe>\S+)`)
	// operation=PendingIntent{b24824d: PendingIntentRecord{2ecfa5b com.android.nfc broadcastIntent}}
	// 注意 op 字段排除 `}`，避免贪婪匹配吃掉 PendingIntent 闭合括号
	operationRE = regexp.MustCompile(`^\s*operation=PendingIntent\{[^}]*PendingIntentRecord\{[^}]*\s(?P<pkg>\S+)\s(?P<op>[^\s}]+)\}`)
	// listener=android.app.AlarmManager$ListenerWrapper@a7d26b5
	listenerRE = regexp.MustCompile(`^\s*listener=(?P<listener>\S+)`)
	// "(\d+) pending alarms:"
	pendingCountRE = regexp.MustCompile(`(?P<n>\d+)\s+pending alarms:`)
)

func parseNowLine(s string) (rtc, elapsed int64) {
	m, r := historianutils.SubexpNames(nowLineRE, s)
	if !m {
		return 0, 0
	}
	rtc, _ = strconv.ParseInt(r["rtc"], 10, 64)
	elapsed, _ = strconv.ParseInt(r["elapsed"], 10, 64)
	return
}

// parseUIDList 解析 "[u0a73 u0a78 ... u0i15]" 或 "{1000, 2000, 10087}" 形式的 uid 列表。
// 支持 u0aNN / u0iNN / uNN / 纯数字 四种 uid 格式。
func parseUIDList(s string) []int32 {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	var out []int32
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
		if v, ok := parseUIDToken(tok); ok {
			out = append(out, v)
		}
	}
	return out
}

// parseUIDToken 把 "u0a73" / "u0i14" / "u0" / "1000" 形式的 uid 转成 int32。
//   - u0aNN → 10000 + NN  (app uid)
//   - u0iNN → 99000 + NN  (isolated uid)
//   - uNN   → NN          (plain, 较少见)
//   - 数字  → 直接解析
func parseUIDToken(tok string) (int32, bool) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return 0, false
	}
	// 纯数字
	if n, err := strconv.Atoi(tok); err == nil {
		return int32(n), true
	}
	// u<user>a<app> 或 u<user>i<iso> 形式
	if len(tok) >= 3 && tok[0] == 'u' {
		// 找到 a 或 i 的位置
		idxA := strings.IndexByte(tok, 'a')
		idxI := strings.IndexByte(tok, 'i')
		sep := -1
		isApp := false
		switch {
		case idxA > 0 && (idxI < 0 || idxA < idxI):
			sep = idxA
			isApp = true
		case idxI > 0:
			sep = idxI
			isApp = false
		}
		if sep < 0 {
			return 0, false
		}
		_, err1 := strconv.Atoi(tok[1:sep])
		suffix, err2 := strconv.Atoi(tok[sep+1:])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		if isApp {
			return int32(10000 + suffix), true
		}
		return int32(99000 + suffix), true
	}
	return 0, false
}

func parsePendingAlarms(lines []string) (int, []Alarm) {
	count := 0
	var alarms []Alarm
	inList := false
	var current *Alarm
	flush := func() {
		if current != nil {
			alarms = append(alarms, *current)
			current = nil
		}
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if m, r := historianutils.SubexpNames(pendingCountRE, trim); m {
			count, _ = strconv.Atoi(r["n"])
			inList = true
			continue
		}
		if !inList {
			continue
		}
		// 检测是否进入新 alarm 记录
		if m, r := historianutils.SubexpNames(alarmHeadRE, line); m {
			flush()
			seq, _ := strconv.Atoi(r["seq"])
			whenMs, _ := strconv.ParseInt(r["when"], 10, 64)
			current = &Alarm{
				Seq:           seq,
				Type:          r["type"],
				PackageName:   r["pkg"],
				WhenElapsedMs: whenMs,
			}
			continue
		}
		if current == nil {
			// 在 alarm 列表区域里但还没开始第一条，或者在两条记录之间的空行
			// 检测列表结束（空行后跟非缩进行）
			if trim == "" {
				continue
			}
			// 列表区域里出现非缩进、非空行（如 "Top 10 Alarms:"），结束
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inList = false
				continue
			}
			continue
		}
		// 给当前 alarm 填字段
		if m, r := historianutils.SubexpNames(tagRE, line); m {
			current.Tag = r["tag"]
			continue
		}
		if m, r := historianutils.SubexpNames(alarmTypeRE, line); m {
			current.Type = r["type"]
			current.WhenElapsedRel = r["ow"]
			current.WindowMs = parseWindowMs(r["win"])
			current.ExactAllowReason = r["ear"]
			current.RepeatIntervalMs, _ = strconv.ParseInt(r["ri"], 10, 64)
			flags, _ := strconv.ParseInt(r["flags"], 0, 64)
			current.Flags = int(flags)
			continue
		}
		if m, r := historianutils.SubexpNames(whenElapsedRE, line); m {
			// 优先用这一行的 whenElapsed 相对值覆盖 WhenElapsedRel
			// （更直观，因为这是 policy 调整后的实际触发时间）
			current.WhenElapsedRel = r["we"]
			continue
		}
		if m, r := historianutils.SubexpNames(operationRE, line); m {
			current.Operation = r["pkg"] + " " + r["op"]
			continue
		}
		if m, r := historianutils.SubexpNames(listenerRE, line); m {
			current.Listener = r["listener"]
			continue
		}
	}
	flush()
	return count, alarms
}

// parseWindowMs 把 window=+7s500ms / window=+45s0ms / window=0 转成毫秒。
// 形如 "+7s500ms" 含 +/- 前缀；"0" 是无 window 精确 alarm。
func parseWindowMs(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "0" || s == "" {
		return 0
	}
	return parseDurationMs(s)
}

// computeTopAlarms 按 repeatInterval > 0 优先 + repeatInterval 降序 + seq 升序取前 N。
// 设计文档原意：把"重复 alarm（定时唤醒源）"排前面，因为这正是功耗分析的重点。
func computeTopAlarms(alarms []Alarm, topN int) []Alarm {
	if topN <= 0 || len(alarms) == 0 {
		return nil
	}
	out := make([]Alarm, len(alarms))
	copy(out, alarms)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].RepeatIntervalMs, out[j].RepeatIntervalMs
		switch {
		case ri > 0 && rj <= 0:
			return true
		case ri <= 0 && rj > 0:
			return false
		case ri > 0 && rj > 0 && ri != rj:
			return ri > rj
		default:
			return out[i].Seq < out[j].Seq
		}
	})
	if len(out) > topN {
		out = out[:topN]
	}
	return out
}

// durationRE 匹配 "+4h38m12s216ms" / "-15m12s377ms" / "+20s754ms" / "0" / "+1d2h" 形式。
// Android 17+ 长运行 alarm 可能跨天，含 d 字段。
var durationRE = regexp.MustCompile(`(?P<sign>[-+]?)(?:(?P<d>\d+)d)?(?:(?P<h>\d+)h)?(?:(?P<m>\d+)m)?(?:(?P<s>\d+)s)?(?:(?P<ms>\d+)ms)?`)

// aggregateTopAlarmRE 匹配 AOSP "Top Alarms:" 段的第一行：
//   "+2s531ms running, 15 wakeups, 15 alarms: 1000:android"
// running 时长可能含 d/h/m/s/ms 组合。
var aggregateTopAlarmRE = regexp.MustCompile(`^\s*\+(?P<run>[\dhmsd]+)\s+running,\s+(?P<wake>\d+)\s+wakeups,\s+(?P<alarms>\d+)\s+alarms:\s+(?P<uidpkg>\S+)`)

// parseAggregateTopAlarms 扫描 "Top Alarms:" 段，返回按出现顺序（AOSP 已按 running 降序排好）的列表。
// 每条记录占 2 行：第 1 行是统计摘要，第 2 行是 tag。
func parseAggregateTopAlarms(lines []string) []AggregateTopAlarm {
	var out []AggregateTopAlarm
	inSection := false
	var current *AggregateTopAlarm
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "Top Alarms:" {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		// 段结束：空行或下一个段标题（"Alarm Stats:" 等）
		if trim == "" || (!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t")) {
			flush()
			inSection = false
			continue
		}
		// 第 1 行：统计摘要
		if m, r := historianutils.SubexpNames(aggregateTopAlarmRE, line); m {
			flush()
			wakeups, _ := strconv.Atoi(r["wake"])
			alarms, _ := strconv.Atoi(r["alarms"])
			current = &AggregateTopAlarm{
				RunningRel: "+" + r["run"],
				RunningMs:  parseDurationMs("+" + r["run"]),
				Wakeups:    wakeups,
				Alarms:     alarms,
				UIDPkg:     r["uidpkg"],
			}
			continue
		}
		// 第 2 行：tag（如 "  *walarm*:com.tcl.server.power.action.APP_POWER_MONITOR_TICK"）
		if current != nil && strings.HasPrefix(trim, "*walarm*:") {
			current.Tag = trim
			flush()
			continue
		}
		if current != nil && strings.HasPrefix(trim, "*alarm*:") {
			current.Tag = trim
			flush()
			continue
		}
	}
	flush()
	return out
}

// parseDurationMs 把 "+4h38m12s216ms" / "-15m12s377ms" / "+1d2h" 形式的相对时间转成毫秒。
// 返回绝对值，因为"多久"恒为正数。
func parseDurationMs(s string) int64 {
	m, r := historianutils.SubexpNames(durationRE, s)
	if !m {
		return 0
	}
	var total int64
	if r["d"] != "" {
		n, _ := strconv.ParseInt(r["d"], 10, 64)
		total += n * 24 * 3600 * 1000
	}
	if r["h"] != "" {
		n, _ := strconv.ParseInt(r["h"], 10, 64)
		total += n * 3600 * 1000
	}
	if r["m"] != "" {
		n, _ := strconv.ParseInt(r["m"], 10, 64)
		total += n * 60 * 1000
	}
	if r["s"] != "" {
		n, _ := strconv.ParseInt(r["s"], 10, 64)
		total += n * 1000
	}
	if r["ms"] != "" {
		n, _ := strconv.ParseInt(r["ms"], 10, 64)
		total += n
	}
	return total
}

// afterEq 返回 "key=value" 的 value 部分。
func afterEq(s string) string {
	if i := strings.Index(s, "="); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return ""
}

// afterColon 返回 "key: value" 的 value 部分。
func afterColon(s string) string {
	if i := strings.Index(s, ":"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return ""
}
