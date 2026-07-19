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

// Package procstats parses the "dumpsys procstats" section of an Android
// bugreport and exposes a structured summary for MCP consumption.
//
// 与 dumpsysactivity 的 RUNNING PROCESSES 不同，procstats 给的是一段时间
// 窗口内（如最近 24h 或自上次重启）每个进程的状态时长占比 + RSS 内存
// 三元组（min/avg/max）。OEM 功耗分析场景里，这正好回答"哪个进程
// 长期跑在前台/后台、占用了多少内存"——是排查功耗大户和内存压力的
// 关键数据源。
package procstats

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
	"github.com/reedhoop/ai-battery-historian/historianutils"
)

// Summary 是 dumpsys procstats 段的结构化镜像。
type Summary struct {
	CommittedFrom string        // "2026-07-13-15-35-12"（最近一次 commit 窗口）
	Processes     []ProcessStat // 按 Total.Percent 降序
}

// ProcessStat 对应一个进程块 "  * <pkg> / <uid> / <ver>:" 及其下属状态行。
type ProcessStat struct {
	Package string
	UID     string // "u0a106" / "1000"
	Version string // "v20251243"
	Total   StateEntry
	States  []StateEntry // 含 TOTAL 之外的所有状态（Persistent / Top / Bnd Fgs 等）
}

// StateEntry 是进程块里一行 "<Label>: <pct>% (cpu/cpu/rss over N)"。
type StateEntry struct {
	Label   string  // "TOTAL" / "Persistent" / "Bnd Fgs" 等
	Percent float64 // 99 / 2,9 等（已转为浮点）
	// RSS 三元组（min/avg/max），单位 KB。0 表示该状态行没有 RSS 数据
	// （例如纯百分比 "Bnd Fgs: 64%"）。
	RSSMinKB int64
	RSSAvgKB int64
	RSSMaxKB int64
	Samples  int // "over 2" → 2
}

// Parse 解析 bugreport 全文，返回 dumpsys procstats 段的结构化摘要。
// 段不存在时返回 (nil, nil)；段存在但解析失败返回 (nil, err)。
func Parse(raw string) (*Summary, error) {
	section := bugreportutils.ExtractServiceDump(raw, "procstats")
	if section == "" {
		return nil, nil
	}
	lines := strings.Split(section, "\n")

	s := &Summary{}
	var procs []ProcessStat
	var current *ProcessStat
	var lastCommitted string

	flush := func() {
		if current != nil {
			procs = append(procs, *current)
			current = nil
		}
	}

	for _, line := range lines {
		// COMMITTED STATS FROM <date>:
		if m, r := historianutils.SubexpNames(committedRE, line); m {
			flush()
			lastCommitted = r["from"]
			continue
		}
		// 进程块头："  * <pkg> / <uid> / <ver>:"
		if m, r := historianutils.SubexpNames(procHeadRE, line); m {
			flush()
			current = &ProcessStat{
				Package: r["pkg"],
				UID:     r["uid"],
				Version: r["ver"],
			}
			continue
		}
		if current == nil {
			continue
		}
		// 状态行："         TOTAL: 99% (0,00-0,00-0,00/0,00-0,00-0,00/474MB-446MB-474MB over 2)"
		// 或 "       Bnd Fgs: 64%"
		se, ok := parseStateLine(line)
		if !ok {
			continue
		}
		if se.Label == "TOTAL" {
			current.Total = se
		} else {
			current.States = append(current.States, se)
		}
	}
	flush()

	s.CommittedFrom = lastCommitted
	// 按 Total.Percent 降序排序（与设计文档 §6.5 topN 一致）
	sort.SliceStable(procs, func(i, j int) bool {
		return procs[i].Total.Percent > procs[j].Total.Percent
	})
	s.Processes = procs
	return s, nil
}

// TopN 返回按 Total.Percent 降序的前 N 个进程。N<=0 或 N>len 时返回全部。
func (s *Summary) TopN(n int) []ProcessStat {
	if s == nil || n <= 0 || n >= len(s.Processes) {
		return s.Processes
	}
	return s.Processes[:n]
}

var (
	// COMMITTED STATS FROM 2026-07-13-15-35-12:
	committedRE = regexp.MustCompile(`COMMITTED STATS FROM (?P<from>[\d-]+):`)
	// "  * com.android.systemui / u0a106 / v20251243:"
	procHeadRE = regexp.MustCompile(`^\s*\*\s+(?P<pkg>\S+)\s*/\s*(?P<uid>\S+)\s*/\s*(?P<ver>\S+):`)
	// "         TOTAL: 99% (0.00-0.00-0.00/0.00-0.00-0.00/474MB-446MB-474MB over 2)"
	// 或 "       Bnd Fgs: 64%"
	// 或 "        Imp Fg: 2.2%"  (en_US locale)
	// 或 "       Bnd Fgs: 10.0%"  (Android 17 实际样本)
	// Label 可含字母 + 空格（如 "Bnd Fgs" / "Service Rs"）。
	// pct 兼容 en_US locale（"."）和欧式 locale（","），含小数点。
	// 可选 * 前缀（ProcessStats.java:2750-2752 溢出标记）。
	// 注意：historianutils.SubexpNames 内部对输入做了 TrimSpace，因此
	// 正则不能依赖行首缩进区分；pct 后强制要求 "%" 已足以过滤非状态行。
	stateLineRE = regexp.MustCompile(`^(?:\*)?(?P<label>[A-Za-z(][A-Za-z (]*?):\s+(?P<pct>[\d.,]+)%(?:\s+\((?P<rest>[^)]+)\))?`)
	// 解析 rest 里的 RSS 三元组（最后一组）：
	//   "0.00-86MB-219MB/0.00-76MB-203MB/421MB-412MB-421MB over 7"  (Android 17 实际样本)
	// 三组以 / 分隔，前两组是 PSS/USS，第三组是 RSS。
	// 每个值可能是：纯数字（<1MB，如 0.00）、N.NNMB（1-10MB）、NMB（≥10MB）、N.NNGB（≥1GB）。
	// 必须用命名捕获组，否则 historianutils.SubexpNames 无法取到值。
	rssRE = regexp.MustCompile(`/(?P<min>[\d.,]+)(?P<minu>MB|GB|KB)?-(?P<avg>[\d.,]+)(?P<avgu>MB|GB|KB)?-(?P<max>[\d.,]+)(?P<maxu>MB|GB|KB)?\s+over\s+(?P<samples>\d+)`)
)

func parseStateLine(line string) (StateEntry, bool) {
	m, r := historianutils.SubexpNames(stateLineRE, line)
	if !m {
		return StateEntry{}, false
	}
	se := StateEntry{
		Label:   strings.TrimSpace(r["label"]),
		Percent: parseFloat(r["pct"]),
	}
	if rest := r["rest"]; rest != "" {
		if m2, r2 := historianutils.SubexpNames(rssRE, rest); m2 {
			se.RSSMinKB = parseRSSKB(r2["min"], r2["minu"])
			se.RSSAvgKB = parseRSSKB(r2["avg"], r2["avgu"])
			se.RSSMaxKB = parseRSSKB(r2["max"], r2["maxu"])
			samples, _ := strconv.Atoi(r2["samples"])
			se.Samples = samples
		}
	}
	return se, true
}

// parseRSSKB 把 "0.00" / "5.5MB" / "86MB" / "1.5GB" 形式的 RSS 值转成 KB。
//   - 无单位：< 1MB 的小值（实际为 0），按 bytes 处理除以 1024（结果通常为 0）
//   - KB：直接取值
//   - MB：×1024
//   - GB：×1024*1024
func parseRSSKB(v, unit string) int64 {
	f := parseFloat(v)
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

// parseFloat 解析欧式逗号小数（"2,9" → 2.9）。
func parseFloat(s string) float64 {
	s = strings.ReplaceAll(s, ",", ".")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
