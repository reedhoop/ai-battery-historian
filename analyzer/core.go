// Copyright 2016 Google Inc. All Rights Reserved.
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

package analyzer

import (
	"fmt"

	"github.com/reedhoop/ai-battery-historian/aggregated"
	"github.com/reedhoop/ai-battery-historian/analyzer/alarm"
	"github.com/reedhoop/ai-battery-historian/analyzer/dumpsysactivity"
	"github.com/reedhoop/ai-battery-historian/analyzer/health"
	"github.com/reedhoop/ai-battery-historian/analyzer/power"
	"github.com/reedhoop/ai-battery-historian/analyzer/procstats"
	"github.com/reedhoop/ai-battery-historian/parseutils"
	bspb "github.com/reedhoop/ai-battery-historian/pb/batterystats_proto"
	"github.com/reedhoop/ai-battery-historian/presenter"
)

// AnalysisResult is the structured, plot-free output of the Analysis Core.
// It bundles the same data products the HTTP endpoint exposes (the system
// checkin, per-app power stats and histogram stats) plus the raw parsed
// batterystats proto (RawCheckin) which callers can diff locally.
//
// It is intended for programmatic / MCP consumption, where the Historian
// HTML plot is irrelevant.
type AnalysisResult struct {
	Checkin        aggregated.Checkin
	AppStats       []presenter.AppStat
	HistogramStats presenter.HistogramStats
	RawCheckin     *bspb.BatteryStats

	IsDiff      bool
	SDKVersion  int
	DeviceModel string
	FileName    string

	CriticalError string
	Note          string
	Error         string
	Warning       string

	// PlotHTML is the Historian plot HTML (P3-B). Empty unless the report was
	// analyzed with chart generation enabled (AnalyzeWithChart / drawPlot).
	// When chart generation is disabled (Analyze / Compare) but the report is a
	// modern Format:2 bug report, this is populated by postProcess with a
	// self-contained inline-SVG battery-level chart.
	PlotHTML string

	// ReportHTML is a self-contained analysis-page HTML (P3-B report resource):
	// device metadata, health card, battery-level chart, and key aggregate
	// stats. Populated by postProcess for every successfully parsed report.
	ReportHTML string

	// Health is the battery health score (P3-C). Nil when the report could
	// not be scored, e.g. an unsupported bug-report version or a critical
	// parse error (CriticalError set).
	Health *health.Report

	// LevelSummaries is the per-segment (battery-level-drop) time-indexed
	// series, retained for windowed health re-scoring (P3-C time-range).
	// It carries StartTimeMs/EndTimeMs per segment plus per-segment stats
	// (screen on/off, wakelocks, syncs/wakeups, idle/doze). No JSON
	// tag → not serialized to the programmatic/MCP response (internal only).
	LevelSummaries []parseutils.ActivitySummary `json:"-"`

	// DeviceCapacityMah is the battery capacity in mAh, needed to convert
	// level-drop points into mAh for windowed discharge-rate scoring.
	DeviceCapacityMah float32 `json:"-"`

	// P4（OEM 功耗分析扩展）：其他 dumpsys 段解析结果。nil 表示该段在
	// bugreport 中不存在或解析失败（不阻塞主流程）。每个字段的解析独立，
	// 单个失败不影响其他字段。
	//
	// 包路径约定：为避免与现有顶级 activity 包（解析 activity manager
	// 事件、输出 CSV）冲突，dumpsys activity 段的解析器放在
	// analyzer/dumpsysactivity 子包，其余三个直接放 analyzer/<name>。
	PowerSummary  *power.Summary           // dumpsys power
	AlarmSummary  *alarm.Summary           // dumpsys alarm
	ActivityStats *dumpsysactivity.Summary // dumpsys activity（含 ANR / LMK / 进程退出 / 运行中进程）
	ProcStats     *procstats.Summary       // dumpsys procstats
}

// AnalysisResults is the set of per-report results from a single analysis or
// comparison. For one bugreport it contains exactly one entry. For a comparison
// it contains one entry (a delta) when the two reports are from the same device
// and overlapping period, or two independent entries otherwise.
type AnalysisResults []*AnalysisResult

// CompareResult holds the outcome of a two-report comparison.
type CompareResult struct {
	Reports         AnalysisResults
	Combined        presenter.CombinedCheckinSummary
	UsingComparison bool
}

// Analyze parses a single bug report and returns structured results without
// generating the Historian HTML plot (so no Python dependency is required).
//
// contents is the full text of an Android bugreport.
func Analyze(contents string) (AnalysisResults, error) {
	pd := &ParsedData{skipPlot: true}
	defer pd.Cleanup()
	if err := pd.parseBugReport("bugreport.txt", contents, "", ""); err != nil {
		return nil, fmt.Errorf("analyze: %v", err)
	}
	results := pd.analysisResults()
	pd.postProcess(results, contents, "")
	return results, nil
}

// AnalyzeWithChart behaves like Analyze but also generates the Historian plot
// HTML (requires Python 3 + the migrated scripts/historian.py). The resulting
// AnalysisResult.PlotHTML can be served as the MCP chart resource (P3-B).
func AnalyzeWithChart(contents string) (AnalysisResults, error) {
	pd := &ParsedData{skipPlot: false, drawPlot: true}
	defer pd.Cleanup()
	if err := pd.parseBugReport("bugreport.txt", contents, "", ""); err != nil {
		return nil, fmt.Errorf("analyze: %v", err)
	}
	results := pd.analysisResults()
	pd.postProcess(results, contents, "")
	return results, nil
}

// Compare parses two bug reports. If they share the same Android ID and
// batterystats start clock time, the result is a delta (UsingComparison ==
// true, len(Reports) == 1 with IsDiff == true); otherwise they are analyzed as
// two independent reports (len(Reports) == 2).
func Compare(contentsA, contentsB string) (*CompareResult, error) {
	pd := &ParsedData{skipPlot: true}
	defer pd.Cleanup()
	if err := pd.parseBugReport("bugreport_a.txt", contentsA, "bugreport_b.txt", contentsB); err != nil {
		return nil, fmt.Errorf("compare: %v", err)
	}
	results := pd.analysisResults()
	pd.postProcess(results, contentsA, contentsB)
	// UsingComparison must reflect an actual delta path: exactly one report
	// AND that report carries IsDiff=true. A single non-diff result (e.g.
	// when one side failed to parse) must NOT be reported as a comparison.
	usingComparison := len(results) == 1 && results[0].IsDiff
	return &CompareResult{
		Reports:         results,
		Combined:        presenter.CombineCheckinData(pd.data),
		UsingComparison: usingComparison,
	}, nil
}

// analysisResults assembles the structured results from the internal parsed
// data. pd.data (presenter.HTMLData) and pd.responseArr (uploadResponse) are
// appended in lock-step inside parseBugReport, so they are parallel slices.
func (pd *ParsedData) analysisResults() AnalysisResults {
	out := make(AnalysisResults, 0, len(pd.data))
	for i := range pd.data {
		data := pd.data[i]
		r := &AnalysisResult{
			Checkin:     data.CheckinSummary,
			AppStats:    data.AppStats,
			DeviceModel: data.DeviceModel,
			Error:       data.Error,
			Warning:     data.Warning,
		}
		if i < len(pd.responseArr) {
			resp := pd.responseArr[i]
			r.RawCheckin = resp.BatteryStats
			r.HistogramStats = resp.HistogramStats
			r.IsDiff = resp.IsDiff
			r.SDKVersion = resp.SDKVersion
			r.FileName = resp.FileName
			r.CriticalError = resp.CriticalError
			r.Note = resp.Note
			// P3-C 时间窗：留存逐段序列 + 容量，供窗口化重算。
			r.LevelSummaries = resp.LevelSummaries
			r.DeviceCapacityMah = resp.DeviceCapacity
			// P3-B: capture the Historian plot HTML (empty unless chart generation was enabled).
			r.PlotHTML = string(data.Historian)
			if len(resp.AppStats) > 0 {
				r.AppStats = resp.AppStats
			}
		}
		// P3-C: score battery health when the report parsed cleanly.
		if r.CriticalError == "" {
			r.Health = health.Evaluate(r.Checkin, r.HistogramStats)
		}
		// P4: 解析其他 dumpsys 段（power/alarm/dumpsysactivity/procstats）。
		// 不阻塞主流程：单段失败只把对应字段留 nil，不影响其他段。
		// 与 postProcess 同样的索引规则：result[0] 用 contentsA，
		// result[i>0] 用 contentsB（若有）。
		raw := pd.bugReportContentsA
		if i > 0 && pd.bugReportContentsB != "" {
			raw = pd.bugReportContentsB
		}
		if raw != "" {
			r.PowerSummary, _ = power.Parse(raw)
			r.AlarmSummary, _ = alarm.Parse(raw)
			r.ActivityStats, _ = dumpsysactivity.Parse(raw)
			r.ProcStats, _ = procstats.Parse(raw)
		}
		out = append(out, r)
	}
	return out
}

// postProcess fills the plot/report HTML fields that are not produced by the
// legacy Python pipeline, so every parse (chart-enabled or not) yields the
// MCP-servable P3-B artifacts.
//
// contentsA / contentsB are the source bug-report texts for the corresponding
// result entries. For a single-report analysis contentsB is "". For a
// comparison, result[i] is sourced from contentsA when i == 0 and from
// contentsB otherwise.
func (pd *ParsedData) postProcess(results AnalysisResults, contentsA, contentsB string) {
	for i, r := range results {
		if r.CriticalError != "" {
			continue
		}
		src := contentsA
		if i > 0 && contentsB != "" {
			src = contentsB
		}
		// P3-B fallback: when the Python Historian plot is absent (e.g. modern
		// Format:2 reports the migrated historian.py cannot parse), render a
		// self-contained inline-SVG battery-level chart instead.
		if r.PlotHTML == "" {
			if svg := generateV2ChartSVG(src); svg != "" {
				r.PlotHTML = svg
			}
		}
		// P3-B report page: a full analysis HTML (health + chart + stats).
		// Pass the already-resolved PlotHTML so the report embeds the same
		// chart served by the chart resource (no second V2 parse, no
		// mismatch between /chart and /report).
		r.ReportHTML = generateReportHTML(r, r.PlotHTML)
	}
}
