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

// Package power parses the "dumpsys power" section of an Android bugreport and
// exposes a structured summary for MCP consumption.
//
// 与 aggregated.ActivityData（来自 batterystats 的聚合 wakelock 时长）不同，
// 这里给出的是 power 段 "Wake Locks:" 子段里的实时持有快照——当前还有
// 哪些 wakelock 没释放、持有了多久、是哪个 uid/pid 持有的。OEM 功耗测试
// 场景里，这正好补齐了 batterystats 无法回答的"现在卡住的是谁"。
package power

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
	"github.com/reedhoop/ai-battery-historian/historianutils"
)

// Summary 是 dumpsys power 段的结构化镜像。
type Summary struct {
	Wakefulness         string             // "Awake" / "Asleep" / "Dreaming"
	IsPowered           bool
	PlugType            int                // 0=unplugged, 1=AC, 2=USB, 4=Wireless
	BatteryLevel        int                // 0-100
	DeviceIdleMode      bool
	LightDeviceIdleMode bool
	LastWakeTime        int64              // 毫秒，相对 boot
	LastSleepTime       int64
	LastSleepReason     string             // "timeout" / "powerkey" / "proximity" ...
	WakeLockSummary     int                // bitmask
	SuspendBlockers     []SuspendBlocker
	WakeLocks           []WakeLock         // 当前持有的 wakelock 实时快照
	UIDStates           []UIDState
	BatterySaver        BatterySaverStats
}

// SuspendBlocker 对应 "Suspend Blockers:" 子段。
type SuspendBlocker struct {
	Name       string // "PowerManagerService.WakeLocks" 等
	RefCount   int
	AcquiredAt string // 方括号里的内容，如 "unknown: (07-13 22:02:39.565)"
}

// WakeLock 是 power 段 "Wake Locks:" 子段的实时快照。注意与
// aggregated.ActivityData（batterystats 聚合）区分：这里给的是当前
// 持有的 wakelock，含 ACQ 相对时间，不是聚合时长。
type WakeLock struct {
	Name          string // wakelock 名（含 tag，如 'AudioIn'）
	Level         string // "PARTIAL_WAKE_LOCK" / "FULL_WAKE_LOCK" ...
	UID           int32
	PID           int32 // 可选，未给出时为 0
	AcquiredAgoMs int64  // ACQ=-15m12s377ms → 912377
	Long          bool   // 是否标记 LONG
	WorkSource    string // 原始 WorkSource 字符串
	IsFrozen      bool   // Android 17+ 新增 isFrozen 字段
	PowerGroupId  int32  // Android 17+ 新增 powerGroupId 字段
}

// UIDState 对应 "UID states:" 子段。
type UIDState struct {
	UID    string // 原样保留，可能是 "1000" / "u0a73" / "u0i14" 等
	Active bool
	Count  int
	State  int
}

// BatterySaverStats 对应 "Battery saving stats:" 子段。
type BatterySaverStats struct {
	CurrentlyOn          bool
	TimesFullEnabled     int
	TimesAdaptiveEnabled int
	DrainStats           []DrainStat // 按 Doze 状态分组
}

// DrainStat 对应 "Drain stats:" 表格的一行。
type DrainStat struct {
	DozeMode       string // "NonDoze" / "Deep" / "Light"
	Interruptible  bool
	DurationMin    int
	MahUsed        float64
	PercentOfTotal float64
	MahPerHour     float64
}

// Parse 解析 bugreport 全文，返回 dumpsys power 段的结构化摘要。
// 段不存在时返回 (nil, nil)；段存在但解析失败返回 (nil, err)。
func Parse(raw string) (*Summary, error) {
	section := bugreportutils.ExtractServiceDump(raw, "power")
	if section == "" {
		return nil, nil
	}
	s := &Summary{}
	lines := strings.Split(section, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "mWakefulness="):
			s.Wakefulness = afterEq(trim)
		case strings.HasPrefix(trim, "mIsPowered="):
			s.IsPowered = afterEq(trim) == "true"
		case strings.HasPrefix(trim, "mPlugType="):
			s.PlugType, _ = strconv.Atoi(afterEq(trim))
		case strings.HasPrefix(trim, "mBatteryLevel="):
			s.BatteryLevel, _ = strconv.Atoi(afterEq(trim))
		case strings.HasPrefix(trim, "mDeviceIdleMode="):
			s.DeviceIdleMode = afterEq(trim) == "true"
		case strings.HasPrefix(trim, "mLightDeviceIdleMode="):
			s.LightDeviceIdleMode = afterEq(trim) == "true"
		case strings.HasPrefix(trim, "mLastWakeTime="):
			s.LastWakeTime, _ = strconv.ParseInt(firstToken(afterEq(trim)), 10, 64)
		case strings.HasPrefix(trim, "mLastSleepTime="):
			s.LastSleepTime, _ = strconv.ParseInt(firstToken(afterEq(trim)), 10, 64)
		case strings.HasPrefix(trim, "mLastSleepReason="):
			s.LastSleepReason = afterEq(trim)
		case strings.HasPrefix(trim, "mWakeLockSummary="):
			s.WakeLockSummary = parseHex(afterEq(trim))
		case trim == "Wake Locks: size=0":
			// 空段，保持 WakeLocks=nil
		case strings.HasPrefix(trim, "Wake Locks: size="):
			// 进入 Wake Locks 子段，后续每行（到空行或下一个段为止）是一条 wakelock
			for i++; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "" {
					break
				}
				if wl, ok := parseWakeLock(l); ok {
					s.WakeLocks = append(s.WakeLocks, wl)
				}
			}
		case strings.HasPrefix(trim, "Suspend Blockers: size="):
			for i++; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "" {
					break
				}
				if sb, ok := parseSuspendBlocker(l); ok {
					s.SuspendBlockers = append(s.SuspendBlockers, sb)
				}
			}
		case strings.HasPrefix(trim, "Battery Saver is currently:"):
			s.BatterySaver.CurrentlyOn = strings.HasSuffix(afterColon(trim), "ON")
		case strings.HasPrefix(trim, "Times full enabled:"):
			s.BatterySaver.TimesFullEnabled, _ = strconv.Atoi(afterColon(trim))
		case strings.HasPrefix(trim, "Times adaptive enabled:"):
			s.BatterySaver.TimesAdaptiveEnabled, _ = strconv.Atoi(afterColon(trim))
		case strings.HasPrefix(trim, "Drain stats:"):
			// Drain stats 表格采用"完整行 + 续行"双行格式：
			//   "NonDoze NonIntr: 15m 0mAh(0%) 0,0mAh/h    0m 0mAh(0%) 0,0mAh/h"
			//   "        Intr:    43m 855mAh(15%) 1186,9mAh/h ..."
			// 续行没有 Doze 前缀，需要从上一行继承 currentDoze。
			var currentDoze string
			for i++; i < len(lines); i++ {
				l := strings.TrimSpace(lines[i])
				if l == "" || strings.HasPrefix(l, "Battery saver policy") {
					break
				}
				ds, doze, ok := parseDrainStat(l, currentDoze)
				if !ok {
					continue
				}
				currentDoze = doze
				s.BatterySaver.DrainStats = append(s.BatterySaver.DrainStats, ds)
			}
		}
	}
	// UID states 单独一轮扫，避免在主循环里嵌套太深
	s.UIDStates = parseUIDStates(lines)
	return s, nil
}

var (
	// 匹配 Android 17+ 的 wakelock 行：
	//   "PARTIAL_WAKE_LOCK 'name' [flags] ACQ=-1m9s621ms LONG (uid=1000 isFrozen=false isAttributedUidCached=false pid=1820 ws=WorkSource{...} powerGroupId=0)"
	// 兼容旧版 Android（缺 isFrozen/isAttributedUidCached/powerGroupId）。
	// flags 可选 0 或多个：ACQUIRE_CAUSES_WAKEUP / ON_AFTER_RELEASE / SYSTEM_WAKELOCK / DISABLED
	// ACQ 相对时间支持 d/h/m/s/ms 组合（Android 17 长持有 wakelock 可能跨天）。
	// ws 字段含嵌套 {} 和 ()，用非贪婪 .+? 配合行尾锚定避免错位。
	wakeLockRE = regexp.MustCompile(
		`(?P<level>\w+_WAKE_LOCK)\s+'(?P<name>[^']+)'` +
			`(?:\s+(?:ACQUIRE_CAUSES_WAKEUP|ON_AFTER_RELEASE|SYSTEM_WAKELOCK|DISABLED))*` +
			`\s+ACQ=(?P<acq>-?[\dhmsd]+)` +
			`(?P<long>\s+LONG)?` +
			`\s+\(uid=(?P<uid>\d+)` +
			`(?:\s+isFrozen=(?P<frozen>\w+))?` +
			`(?:\s+isAttributedUidCached=\w+)?` +
			`(?:\s+pid=(?P<pid>\d+))?` +
			`(?:\s+ws=(?P<ws>.+?))?` +
			`(?:\s+powerGroupId=(?P<pgid>\d+))?` +
			`\s*\)\s*$`)
	// 匹配 "  PowerManagerService.WakeLocks: ref count=1 [unknown: (07-13 22:02:39.565)]"
	suspendBlockerRE = regexp.MustCompile(`(?P<name>\S+):\s+ref count=(?P<count>\d+)\s+\[(?P<note>.*)\]`)
	// 匹配 "  UID 1041: INACTIVE  count=1 state=20" / "  UID u0a73:   ACTIVE  count=0 state=15"
	uidStateRE = regexp.MustCompile(`UID\s+(?P<uid>\S+):\s+(?P<active>ACTIVE|INACTIVE)\s+count=(?P<count>\d+)\s+state=(?P<state>\d+)`)
	// 匹配 Drain stats 表格的两种行：
	//   完整行: "NonDoze NonIntr:     15m      0mAh(  0%)      0.0mAh/h ..."
	//   续行:   "        Intr:     43m    855mAh( 15%)   1186.9mAh/h ..."
	// 续行不带 Doze 前缀，需要调用方传入 currentDoze 来补齐 DozeMode。
	// mah/pct/mahh 兼容 en_US locale（"."）和欧式 locale（","）。
	drainStatFullRE = regexp.MustCompile(`(?P<doze>NonDoze|Deep|Light)\s+(?P<intr>NonIntr|Intr):\s+(?P<dur>\d+)m\s+(?P<mah>[\d.,]+)mAh\(\s*(?P<pct>[\d.,]+)%\)\s+(?P<mahh>[\d.,]+)mAh/h`)
	drainStatContRE  = regexp.MustCompile(`(?P<intr>NonIntr|Intr):\s+(?P<dur>\d+)m\s+(?P<mah>[\d.,]+)mAh\(\s*(?P<pct>[\d.,]+)%\)\s+(?P<mahh>[\d.,]+)mAh/h`)
)

func parseWakeLock(line string) (WakeLock, bool) {
	m, r := historianutils.SubexpNames(wakeLockRE, line)
	if !m {
		return WakeLock{}, false
	}
	wl := WakeLock{
		Level:      r["level"],
		Name:       r["name"],
		Long:       strings.Contains(r["long"], "LONG"),
		WorkSource: r["ws"],
		IsFrozen:   r["frozen"] == "true",
	}
	uid, _ := strconv.Atoi(r["uid"])
	wl.UID = int32(uid)
	if r["pid"] != "" {
		pid, _ := strconv.Atoi(r["pid"])
		wl.PID = int32(pid)
	}
	if r["pgid"] != "" {
		pgid, _ := strconv.Atoi(r["pgid"])
		wl.PowerGroupId = int32(pgid)
	}
	wl.AcquiredAgoMs = parseDurationMs(r["acq"])
	return wl, true
}

func parseSuspendBlocker(line string) (SuspendBlocker, bool) {
	m, r := historianutils.SubexpNames(suspendBlockerRE, line)
	if !m {
		return SuspendBlocker{}, false
	}
	sb := SuspendBlocker{
		Name:       r["name"],
		AcquiredAt: r["note"],
	}
	sb.RefCount, _ = strconv.Atoi(r["count"])
	return sb, true
}

func parseUIDStates(lines []string) []UIDState {
	var out []UIDState
	for _, line := range lines {
		m, r := historianutils.SubexpNames(uidStateRE, line)
		if !m {
			continue
		}
		us := UIDState{
			UID:    r["uid"],
			Active: r["active"] == "ACTIVE",
		}
		us.Count, _ = strconv.Atoi(r["count"])
		us.State, _ = strconv.Atoi(r["state"])
		out = append(out, us)
	}
	return out
}

func parseDrainStat(line, currentDoze string) (DrainStat, string, bool) {
	if m, r := historianutils.SubexpNames(drainStatFullRE, line); m {
		ds := DrainStat{
			DozeMode:      r["doze"],
			Interruptible: r["intr"] == "Intr",
		}
		ds.DurationMin, _ = strconv.Atoi(r["dur"])
		ds.MahUsed = parseFloat(r["mah"])
		ds.PercentOfTotal = parseFloat(r["pct"])
		ds.MahPerHour = parseFloat(r["mahh"])
		return ds, r["doze"], true
	}
	if currentDoze == "" {
		return DrainStat{}, "", false
	}
	if m, r := historianutils.SubexpNames(drainStatContRE, line); m {
		ds := DrainStat{
			DozeMode:      currentDoze,
			Interruptible: r["intr"] == "Intr",
		}
		ds.DurationMin, _ = strconv.Atoi(r["dur"])
		ds.MahUsed = parseFloat(r["mah"])
		ds.PercentOfTotal = parseFloat(r["pct"])
		ds.MahPerHour = parseFloat(r["mahh"])
		return ds, currentDoze, true
	}
	return DrainStat{}, "", false
}

// durationRE 匹配 "-15m12s377ms" / "-6s304ms" / "-1h2m3s" / "+14s121ms" / "-1d2h"
// 形式的相对时间。各组可选，按 d / h / m / s / ms 顺序匹配。
// Android 17+ 长持有 wakelock 可能跨天，含 d 字段。
var durationRE = regexp.MustCompile(`(?P<sign>[-+]?)(?:(?P<d>\d+)d)?(?:(?P<h>\d+)h)?(?:(?P<m>\d+)m)?(?:(?P<s>\d+)s)?(?:(?P<ms>\d+)ms)?`)

// parseDurationMs 把 "-15m12s377ms" / "-6s304ms" / "-1h2m3s" / "-1d2h" 形式的相对
// 时间转成毫秒（绝对值）。返回的是"多久"而非"何时"，所以恒为正数。
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

// firstToken 返回字符串第一个空白分隔的 token（用于 "4342927 (1696863 ms ago)" 取 4342927）。
func firstToken(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// parseHex 解析 0x 前缀的十六进制或普通十进制。
func parseHex(s string) int {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, _ := strconv.ParseInt(s[2:], 16, 64)
		return int(n)
	}
	n, _ := strconv.Atoi(s)
	return n
}

// parseFloat 解析带逗号小数点的浮点数（欧式格式 "1186,9" → 1186.9）。
func parseFloat(s string) float64 {
	s = strings.ReplaceAll(s, ",", ".")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ensure fmt is used for potential debug output in future extensions
var _ = fmt.Sprintf
