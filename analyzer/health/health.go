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

// Package health computes a battery "health score" for an analyzed bug
// report, together with graded alerts that point at the worst-offending
// behaviors. It is intentionally dependency-light: it only consumes the
// already-aggregated data products (aggregated.Checkin and
// presenter.HistogramStats), so it can run anywhere the Analysis Core runs
// (including the MCP / programmatic path that skips the Python plot).
package health

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/reedhoop/ai-battery-historian/aggregated"
	"github.com/reedhoop/ai-battery-historian/parseutils"
	"github.com/reedhoop/ai-battery-historian/presenter"
)

// Dimension is one scored aspect of battery behavior. Score is 0-100
// (higher = healthier). Status is one of "good" / "fair" / "poor" / "n/a".
// A dimension with Valid == false was not scoreable from the available data
// and is excluded from the composite.
type Dimension struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Score       float64 `json:"score"`
	Weight      float64 `json:"weight"`
	Valid       bool    `json:"valid"`
	Status      string  `json:"status"`
	Detail      string  `json:"detail"`
	MetricValue float64 `json:"metricValue"`
}

// Alert is a single actionable finding. Level is "info" / "warning" /
// "critical".
type Alert struct {
	Level    string `json:"level"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Metric   string `json:"metric"`
	Value    string `json:"value"`
}

// Report is the full health evaluation for one bug report.
type Report struct {
	Score       float64     `json:"score"`
	Grade       string      `json:"grade"`
	Summary     string      `json:"summary"`
	Dimensions  []Dimension `json:"dimensions"`
	Alerts      []Alert     `json:"alerts"`
	GeneratedAt time.Time   `json:"generatedAt"`
	// Evaluated is the number of dimensions that could actually be scored
	// (had valid data); Total is the fixed number of dimensions. They let
	// consumers show e.g. "已评估 3/6 项" so a composite built from a few
	// dimensions is not mistaken for a full 100-point evaluation.
	Evaluated int `json:"evaluated"`
	Total     int `json:"total"`

	// Window metadata. IsWindow is true when this Report was produced by
	// EvaluateWindow over a sub-span (not the whole report). WindowStartMs/
	// WindowEndMs are the selected range (unix ms). WindowableKeys lists
	// the dimensions that were actually re-scored over the window; the rest
	// (stability / modem) carry their WHOLE-REPORT value and are flagged as
	// frozen in their Detail so the card can show them as "全程值".
	IsWindow      bool   `json:"isWindow"`
	WindowStartMs int64  `json:"windowStartMs"`
	WindowEndMs   int64  `json:"windowEndMs"`
	WindowableKeys []string `json:"windowableKeys"`
}

// Weights for the 6 dimensions. They sum to 1.0.
const (
	wStandby   = 0.30 // screen-off (standby) discharge rate
	wWakelock  = 0.20 // partial + kernel wakelock time burden
	wWakeup    = 0.15 // app wakeups + syncs per hour
	wStability = 0.15 // app ANR + crash counts
	wDoze      = 0.10 // device idle / doze adoption
	wModem     = 0.10 // modem discharge rate
)

// isFinite reports whether v is neither NaN nor +/-Inf. The math package does
// not export IsFinite, so we compose it from IsNaN and IsInf.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// Evaluate scores a single report's battery health from its aggregated
// checkin and histogram stats. It is a pure function: same inputs always
// yield the same Report (except for GeneratedAt which records the moment).
//
// When no dimension can be scored (all Valid=false) the returned Report has
// Grade="N/A", Score=0, and a Summary explaining the data was insufficient —
// this lets callers distinguish "unhealthy" from "unscoreable".
func Evaluate(c aggregated.Checkin, h presenter.HistogramStats) *Report {
	dims := []Dimension{
		standbyDimension(c),
		wakelockDimension(c),
		wakeupDimension(h),
		stabilityDimension(h),
		dozeDimension(h),
		modemDimension(c),
	}

	var weightSum, scoreSum float64
	evaluated, total := 0, len(dims)
	for _, d := range dims {
		if d.Valid && isFinite(d.Score) {
			weightSum += d.Weight
			scoreSum += d.Score * d.Weight
			evaluated++
		}
	}

	// No scoreable dimension → return an explicit N/A report instead of a
	// misleading Score=0 / Grade="F".
	if weightSum == 0 {
		return &Report{
			Score:       0,
			Grade:       "N/A",
			Summary:     "无可用数据：所有健康度维度均无法评分（缺少亮/灭屏时长、wakelock、wakeup 或 histogram 统计）。",
			Dimensions:  dims,
			Alerts:      nil,
			GeneratedAt: time.Now(),
			Evaluated:   0,
			Total:       total,
		}
	}

	composite := scoreSum / weightSum
	// Defensive: guard against NaN/Inf slipping through from upstream parsing.
	if !isFinite(composite) {
		composite = 0
	}

	alerts := buildAlerts(dims)
	grade := gradeOf(composite)
	return &Report{
		Score:       composite,
		Grade:       grade,
		Summary:     summarize(composite, grade, dims),
		Dimensions:  dims,
		Alerts:      alerts,
		GeneratedAt: time.Now(),
		Evaluated:   evaluated,
		Total:       total,
	}
}

// EvaluateWindow re-scores battery health over a user-selected time span
// [fromMs, toMs] (unix ms) instead of the whole report. It re-aggregates
// the per-segment (battery-level-drop) series (parseutils.ActivitySummary)
// that the analyzer already retains, so a brief anomaly — a 10-minute
// wakelock storm, a short deep-discharge burst — is no longer diluted
// into the long whole-report average.
//
// Only 4 of the 6 dimensions can be scored precisely from the
// time-indexed series:
//   - standby discharge rate  (level drop during screen-off)
//   - wakelock burden         (held wakelock time)
//   - wakeup/sync frequency    (TotalSyncSummary count)
//   - doze adoption           (IdleModeSummary duration)
// The other 2 — app stability (ANR/crash) and modem discharge rate —
// only exist as whole-report totals (no per-timestamp breakdown in the
// series), so under the strict policy they are carried FROZEN from the
// full-report Report and shown as "全程值", excluded from the window
// composite. This avoids fabricating numbers for metrics we cannot window.
//
// capacityMah is accepted for API symmetry (a future mAh-based standby
// score) but the current %/h computation does not require it.
func EvaluateWindow(summaries []parseutils.ActivitySummary, capacityMah float32, fromMs, toMs int64, full *Report) *Report {
	if fromMs > toMs {
		fromMs, toMs = toMs, fromMs
	}

		var realtimeMs, screenOffMs, screenOffDischargePct, longWakelockMs, idleMs, totalSyncNum int64
	// nsToMs converts a duration expressed in nanoseconds (time.Duration, as
	// stored in parseutils.Dist) into milliseconds, so it can be combined with
	// the millisecond-based segment boundaries (StartTimeMs/EndTimeMs).
	const nsToMs = int64(time.Millisecond)
	for i := range summaries {
		s := summaries[i]
		segStart, segEnd := s.StartTimeMs, s.EndTimeMs
		// Segment does not overlap the selected window.
		if segEnd < fromMs || segStart > toMs {
			continue
		}
		clampStart := segStart
		if clampStart < fromMs {
			clampStart = fromMs
		}
		clampEnd := segEnd
		if clampEnd > toMs {
			clampEnd = toMs
		}
		segDur := segEnd - segStart
		clampDur := clampEnd - clampStart
		if segDur <= 0 || clampDur <= 0 {
			continue
		}
		// Skip charging segments (plugged-in): discharge-based health does
		// not apply while the battery is charging.
		pluggedMs := distDurationInWindow(s.PluggedInSummary, segStart, segEnd, clampStart, clampEnd) / nsToMs
		if pluggedMs*2 >= clampDur {
			continue
		}
		realtimeMs += clampDur

		// Screen-off time within the clamped segment (ms).
		screenOnMs := distDurationInWindow(s.ScreenOnSummary, segStart, segEnd, clampStart, clampEnd) / nsToMs
		screenOffMs += clampDur - screenOnMs

		// Discharge attributable to screen-off time (the standby rate is
		// specifically the screen-OFF discharge). offFrac scales the
		// segment's level drop to its screen-off portion.
		drop := int64(s.InitialBatteryLevel - s.FinalBatteryLevel)
		if drop > 0 {
			offFrac := float64(clampDur-screenOnMs) / float64(segDur)
			screenOffDischargePct += int64(float64(drop)*offFrac + 0.5)
		}
		// Wakelock burden (held wakelock time) — proxy via Long Wakelocks.
		longWakelockMs += sumDistMapDurationInWindow(s.LongWakelockSummary, segStart, segEnd, clampStart, clampEnd) / nsToMs
		// Doze / idle adoption.
		idleMs += sumDistMapDurationInWindow(s.IdleModeSummary, segStart, segEnd, clampStart, clampEnd) / nsToMs
		// Wakeup/sync frequency.
		totalSyncNum += distNumInWindow(s.TotalSyncSummary, segStart, segEnd, clampStart, clampEnd)
	}

	// Build a synthetic aggregated.Checkin carrying ONLY the fields the 4
	// windowable dimension functions read.
	var synth aggregated.Checkin
	synth.Realtime = time.Duration(realtimeMs) * time.Millisecond
	synth.ScreenOffRealtime = time.Duration(screenOffMs) * time.Millisecond
	if screenOffMs > 0 {
		// Discharge rate in %/h = (discharge %) / (screen-off hours).
		// Computed directly (not via the batterystats 3600*1000*pts quirk)
		// so the units match the dimension thresholds exactly.
		hours := float64(screenOffMs) / 3600000.0
		synth.ScreenOffDischargeRatePerHr = aggregated.MFloat32{
			V: float32(float64(screenOffDischargePct) / hours),
		}
	}
	if realtimeMs > 0 {
		// Wakelock burden (% of realtime held awake). The series only
		// exposes held wakelock time (Long Wakelocks), so we use it as
		// the partial-wakelock proxy; full-wakelock is left 0.
		synth.PartialWakelockTimePercentage = float32(float64(longWakelockMs) / float64(realtimeMs) * 100.0)
	}

	var synthHist presenter.HistogramStats
	if realtimeMs > 0 {
		hours := float64(realtimeMs) / 3600000.0
		if hours > 0 {
			synthHist.TotalAppWakeupsPerHr = float32(float64(totalSyncNum) / hours)
		}
		synthHist.DeviceIdleModeEnabledTimePercentage = float32(float64(idleMs) / float64(realtimeMs) * 100.0)
	}

	dims := []Dimension{
		standbyDimension(synth),
		wakelockDimension(synth),
		wakeupDimension(synthHist),
		dozeDimension(synthHist),
	}

	// Windowable dimension keys (the 4 re-scored over the window).
	windowable := map[string]bool{
		"standby_drain":      true,
		"wakelock_burden":  true,
		"wakeup_sync_freq":  true,
		"doze_adoption":     true,
	}

	// Composite over the windowable, valid dimensions only; their
	// weights (0.30+0.20+0.15+0.10 = 0.75) renormalize to 1.0.
	var weightSum, scoreSum float64
	evaluated, total := 0, len(dims)

	// No time (and thus no data) falls inside the selected window: report
	// N/A rather than a misleading all-zero "F".
	if realtimeMs == 0 {
		return &Report{
			Score:        0,
			Grade:        "N/A",
			Summary:      "所选时间段内无运行数据，无法评估健康度。",
			Dimensions:   dims,
			Alerts:       nil,
			GeneratedAt:  time.Now(),
			Evaluated:    0,
			Total:        total,
			IsWindow:     true,
			WindowStartMs: fromMs,
			WindowEndMs:   toMs,
			WindowableKeys: nil,
		}
	}

	var windowableKeys []string
	for _, d := range dims {
		if windowable[d.Key] {
			if d.Valid && isFinite(d.Score) {
				weightSum += d.Weight
				scoreSum += d.Score * d.Weight
				evaluated++
				windowableKeys = append(windowableKeys, d.Key)
			}
		}
	}

	// Carry stability + modem FROZEN from the full-report (they have no
	// per-timestamp series). Marked "全程值" so they are shown for
	// reference but excluded from the window composite above.
	if full != nil {
		for _, fd := range full.Dimensions {
			if fd.Key == "app_stability" || fd.Key == "modem_activity" {
				frozen := fd
				frozen.Detail = fd.Detail + "（全程值）"
				frozen.Weight = 0
				dims = append(dims, frozen)
			}
		}
		total = 6
	}

	if weightSum == 0 {
		return &Report{
			Score:        0,
			Grade:        "N/A",
			Summary:      "所选时间段内无可评分的窗口化维度（无灭屏放电、wakelock、doze 或 wakeup 数据）。",
			Dimensions:   dims,
			Alerts:       nil,
			GeneratedAt:  time.Now(),
			Evaluated:    evaluated,
			Total:        total,
			IsWindow:     true,
			WindowStartMs: fromMs,
			WindowEndMs:   toMs,
			WindowableKeys: windowableKeys,
		}
	}

	composite := scoreSum / weightSum
	if !isFinite(composite) {
		composite = 0
	}
	alerts := buildAlerts(dims)
	grade := gradeOf(composite)
	return &Report{
		Score:        composite,
		Grade:        grade,
		Summary:      summarize(composite, grade, dims),
		Dimensions:   dims,
		Alerts:       alerts,
		GeneratedAt:   time.Now(),
		Evaluated:    evaluated,
		Total:        total,
		IsWindow:     true,
		WindowStartMs: fromMs,
		WindowEndMs:   toMs,
		WindowableKeys: windowableKeys,
	}
}

// distDurationInWindow scales a Dist's duration to the portion of its
// segment [segStart,segEnd] that falls inside [winStart,winEnd], assuming
// the event time was uniformly spread across the segment.
func distDurationInWindow(d parseutils.Dist, segStart, segEnd, winStart, winEnd int64) int64 {
	segDur := segEnd - segStart
	if segDur <= 0 {
		return 0
	}
	clampDur := winEnd - winStart
	if clampDur <= 0 {
		return 0
	}
	return int64(float64(d.TotalDuration) * float64(clampDur) / float64(segDur))
}

// distNumInWindow scales a Dist's count the same way.
func distNumInWindow(d parseutils.Dist, segStart, segEnd, winStart, winEnd int64) int64 {
	segDur := segEnd - segStart
	if segDur <= 0 {
		return 0
	}
	clampDur := winEnd - winStart
	if clampDur <= 0 {
		return 0
	}
	return int64(float64(d.Num) * float64(clampDur) / float64(segDur))
}

// sumDistMapDurationInWindow sums the clamped durations of every entry in a
// Dist map (e.g. LongWakelockSummary keyed by wakelock name).
func sumDistMapDurationInWindow(m map[string]parseutils.Dist, segStart, segEnd, winStart, winEnd int64) int64 {
	var total int64
	for _, d := range m {
		total += distDurationInWindow(d, segStart, segEnd, winStart, winEnd)
	}
	return total
}

// statusOf maps a 0-100 score to a qualitative status, using the same
// thresholds as gradeOf (A/B = good, C/D = fair, F = poor) so the alert
// severity and the letter grade always agree.
func statusOf(score float64) string {
	switch {
	case score >= 70: // A or B
		return "good"
	case score >= 40: // C or D
		return "fair"
	default: // F
		return "poor"
	}
}

// lerpDown scores a "lower is better" metric. value <= good returns 100,
// value >= poor returns 0, linear between. Returns valid=false if the
// thresholds are mis-ordered or the input is non-finite (NaN/Inf).
func lerpDown(value, good, poor float64) (score float64, valid bool) {
	if poor <= good || !isFinite(value) {
		return 0, false
	}
	switch {
	case value <= good:
		return 100, true
	case value >= poor:
		return 0, true
	}
	return 100 * (poor - value) / (poor - good), true
}

// lerpUp scores a "higher is better" metric. value >= good returns 100,
// value <= poor returns 0, linear between. Returns valid=false when the
// thresholds are mis-ordered or the input is non-finite.
func lerpUp(value, good, poor float64) (score float64, valid bool) {
	if good <= poor || !isFinite(value) {
		return 0, false
	}
	switch {
	case value >= good:
		return 100, true
	case value <= poor:
		return 0, true
	}
	return 100 * (value - poor) / (good - poor), true
}

// standbyDimension scores screen-off (standby) discharge rate in %/h.
// Only valid when the report actually contains screen-off time.
func standbyDimension(c aggregated.Checkin) Dimension {
	const good, poor = 1.5, 10.0
	v := float64(c.ScreenOffDischargeRatePerHr.V)
	if c.ScreenOffRealtime <= 0 {
		return Dimension{
			Key:         "standby_drain",
			Label:       "待机放电率",
			Weight:      wStandby,
			Valid:       false,
			Status:      "n/a",
			Detail:      "报告期无灭屏时段，无法评估待机放电",
			MetricValue: 0,
		}
	}
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "standby_drain",
		Label:       "待机放电率",
		Score:       s,
		Weight:      wStandby,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("灭屏放电 %.1f%%/h（健康≤%.1f，严重≥%.0f）", v, good, poor),
		MetricValue: v,
	}
}

// wakelockDimension scores the fraction of time spent in partial + full
// wakelocks (background work preventing sleep). Invalid when the report has
// no total battery realtime (the percentage denominator is zero).
//
// NOTE: we must NOT add KernelOverheadTimePercentage. That field is defined
// in aggregated_stats.go as ScreenOffUptimeMsec - PartialWakelockTimeMsec, so
// Partial% + KernelOverhead% cancels out to ScreenOffUptime% — i.e. it would
// measure how long the screen was off, not how long the device was held awake
// by wakelocks. On any normal overnight report ScreenOffUptime is 60-90%, which
// made this dimension report "critical" almost always. We score the real
// wakelock hold time instead (partial + full).
func wakelockDimension(c aggregated.Checkin) Dimension {
	const good, poor = 5.0, 30.0
	if c.Realtime <= 0 {
		return Dimension{
			Key: "wakelock_burden", Label: "Wakelock 负担", Weight: wWakelock,
			Valid: false, Status: "n/a",
			Detail: "报告期无电池运行时长，无法评估 wakelock 负担",
		}
	}
	v := float64(c.PartialWakelockTimePercentage + c.FullWakelockTimePercentage)
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "wakelock_burden",
		Label:       "Wakelock 负担",
		Score:       s,
		Weight:      wWakelock,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("partial + full wakelock 占 %.1f%% 时间（健康≤%.0f%%，严重≥%.0f%%）", v, good, poor),
		MetricValue: v,
	}
}

// wakeupDimension scores app wakeup frequency (events per hour). Invalid when
// the report carries no wakeup/sync data at all.
//
// NOTE on units: we score on TotalAppWakeupsPerHr (a *count* per hour). The
// companion field TotalAppSyncsPerHr is a sync *duration* rate (seconds/hour,
// accumulated from SyncManager task durations) — a different unit that must
// not be summed into the same number, or seconds would be silently treated as
// "次数" and inflate the metric. We surface the sync duration as context in
// the detail string instead of folding it into the score.
func wakeupDimension(h presenter.HistogramStats) Dimension {
	const good, poor = 20.0, 150.0
	wakeupsPerHr := float64(h.TotalAppWakeupsPerHr)
	syncSecPerHr := float64(h.TotalAppSyncsPerHr)
	if wakeupsPerHr <= 0 && syncSecPerHr <= 0 {
		return Dimension{
			Key: "wakeup_sync_freq", Label: "唤醒/同步频率", Weight: wWakeup,
			Valid: false, Status: "n/a",
			Detail: "无 wakeup / sync 直方图数据",
		}
	}
	s, ok := lerpDown(wakeupsPerHr, good, poor)
	detail := fmt.Sprintf("App 唤醒 %.0f 次/h（健康≤%.0f，严重≥%.0f）", wakeupsPerHr, good, poor)
	if syncSecPerHr > 0 {
		detail += fmt.Sprintf("；Sync 任务时长 %.0f 秒/h", syncSecPerHr)
	}
	return Dimension{
		Key:         "wakeup_sync_freq",
		Label:       "唤醒/同步频率",
		Score:       s,
		Weight:      wWakeup,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      detail,
		MetricValue: wakeupsPerHr,
	}
}

// stabilityDimension scores app stability from ANR + crash counts. The
// dimension is always scoreable when the histogram is present: ANR/Crash
// counts are non-negative ints, so there is no "missing data" sentinel to
// detect here, and a zero count legitimately means "no crashes" (healthy).
// (The old guard `count < 0` could never fire and was dead code.)
func stabilityDimension(h presenter.HistogramStats) Dimension {
	const good, poor = 0.0, 30.0
	v := float64(h.TotalAppANRCount + h.TotalAppCrashCount)
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "app_stability",
		Label:       "App 稳定性",
		Score:       s,
		Weight:      wStability,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("ANR %d + Crash %d（健康=0，严重≥%.0f）", h.TotalAppANRCount, h.TotalAppCrashCount, poor),
		MetricValue: v,
	}
}

// dozeDimension scores device idle / doze mode adoption (higher is better).
// Invalid when the histogram has no device-idle percentage (e.g. pre-Doze
// Android versions or a report that never went through Doze).
func dozeDimension(h presenter.HistogramStats) Dimension {
	const good, poor = 30.0, 2.0
	v := float64(h.DeviceIdleModeEnabledTimePercentage)
	// A negative or NaN marker means the field was never populated.
	if v < 0 || !isFinite(v) {
		return Dimension{
			Key: "doze_adoption", Label: "Doze 采用率", Weight: wDoze,
			Valid: false, Status: "n/a",
			Detail: "无 Doze / device-idle 数据",
		}
	}
	s, ok := lerpUp(v, good, poor)
	return Dimension{
		Key:         "doze_adoption",
		Label:       "Doze 采用率",
		Score:       s,
		Weight:      wDoze,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("Doze 模式占 %.1f%% 时间（健康≥%.0f%%，严重≤%.0f%%）", v, good, poor),
		MetricValue: v,
	}
}

// modemDimension scores modem-related discharge rate in %/h. Invalid when the
// report has no modem activity (mobile active time missing and no discharge
// recorded).
func modemDimension(c aggregated.Checkin) Dimension {
	const good, poor = 1.0, 8.0
	if c.ModemDischargeRatePerHr.V <= 0 && c.MobileActiveTime.V <= 0 {
		return Dimension{
			Key: "modem_activity", Label: "Modem 活动", Weight: wModem,
			Valid: false, Status: "n/a",
			Detail: "无 Modem 活动数据",
		}
	}
	v := float64(c.ModemDischargeRatePerHr.V)
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "modem_activity",
		Label:       "Modem 活动",
		Score:       s,
		Weight:      wModem,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("Modem 放电 %.1f%%/h（健康≤%.1f，严重≥%.0f）", v, good, poor),
		MetricValue: v,
	}
}

// alertLevel returns the severity for a dimension status.
func alertLevel(status string) string {
	switch status {
	case "poor":
		return "critical"
	case "fair":
		return "warning"
	default:
		return ""
	}
}

// buildAlerts emits a critical/warning alert for each poorly-scored dimension.
// Alerts are sorted by severity (critical before warning), then by score
// ascending within the same severity so the worst offender appears first.
func buildAlerts(dims []Dimension) []Alert {
	type scoredAlert struct {
		Alert
		score float64
	}
	var tmp []scoredAlert
	for _, d := range dims {
		level := alertLevel(d.Status)
		if level == "" || !d.Valid {
			continue
		}
		tmp = append(tmp, scoredAlert{
			Alert: Alert{
				Level:    level,
				Category: d.Key,
				Message:  alertMessage(d),
				Metric:   d.Label,
				Value:    alertValue(d),
			},
			score: d.Score,
		})
	}
	sort.SliceStable(tmp, func(i, j int) bool {
		if tmp[i].Level != tmp[j].Level {
			return tmp[i].Level == "critical"
		}
		// Same severity → lower score (worse) first.
		return tmp[i].score < tmp[j].score
	})
	alerts := make([]Alert, len(tmp))
	for i, a := range tmp {
		alerts[i] = a.Alert
	}
	return alerts
}

// alertValue formats the dimension's metric value with the dimension-specific
// unit so consumers can render "5.2%/h" rather than a unit-less "5.2".
func alertValue(d Dimension) string {
	switch d.Key {
	case "standby_drain", "modem_activity":
		return fmt.Sprintf("%.1f%%/h", d.MetricValue)
	case "wakelock_burden":
		return fmt.Sprintf("%.1f%%", d.MetricValue)
	case "wakeup_sync_freq":
		return fmt.Sprintf("%.0f 次/h", d.MetricValue)
	case "app_stability":
		return fmt.Sprintf("%.0f 次", d.MetricValue)
	case "doze_adoption":
		return fmt.Sprintf("%.1f%%", d.MetricValue)
	default:
		return fmt.Sprintf("%.1f", d.MetricValue)
	}
}

// alertMessage produces a human-readable remediation hint per dimension.
func alertMessage(d Dimension) string {
	switch d.Key {
	case "standby_drain":
		return "灭屏待机掉电过快，优先排查后台持锁与高频唤醒（wakelock / alarm / jobScheduler）。"
	case "wakelock_burden":
		return "设备大量时间处于 wakelock，无法深度休眠；定位持有 partial/kernel wakelock 时间最长的前几个应用。"
	case "wakeup_sync_freq":
		return "应用唤醒与同步过于频繁，打断系统休眠；收敛不必要的 alarm 与周期性 sync。"
	case "app_stability":
		return "存在较多 ANR/CRASH，影响体验且可能伴随异常后台行为；先修复稳定性再评估耗电。"
	case "doze_adoption":
		return "Doze 模式占比过低，灭屏后系统未充分进入低功耗；检查是否滥用豁免（allowlist / foreground service）。"
	case "modem_activity":
		return "Modem 放电占比高，射频/数据传输偏重；优化网络请求批处理、降低移动网络活跃时长。"
	default:
		return d.Detail
	}
}

// gradeOf maps a 0-100 composite to a letter grade.
func gradeOf(score float64) string {
	switch {
	case score >= 85:
		return "A"
	case score >= 70:
		return "B"
	case score >= 55:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// summarize builds a one-line natural-language summary pointing at the worst
// scored dimensions, ordered by score ascending (worst first).
func summarize(score float64, grade string, dims []Dimension) string {
	var worst []Dimension
	for _, d := range dims {
		if d.Valid && (d.Status == "poor" || d.Status == "fair") {
			worst = append(worst, d)
		}
	}
	base := fmt.Sprintf("电池健康度 %s（%.0f/100）", grade, score)
	if len(worst) == 0 {
		return base + "：各项指标良好。"
	}
	sort.SliceStable(worst, func(i, j int) bool { return worst[i].Score < worst[j].Score })
	labels := make([]string, len(worst))
	for i, d := range worst {
		labels[i] = d.Label
	}
	return fmt.Sprintf("%s：主要扣分项 %s。", base, strings.Join(labels, "、"))
}
