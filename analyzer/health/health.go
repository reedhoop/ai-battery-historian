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
	for _, d := range dims {
		if d.Valid && isFinite(d.Score) {
			weightSum += d.Weight
			scoreSum += d.Score * d.Weight
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
	}
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

// wakelockDimension scores the fraction of time spent in partial + kernel
// wakelocks (background work preventing sleep). Invalid when the report has
// no total battery realtime (the percentage denominator is zero).
func wakelockDimension(c aggregated.Checkin) Dimension {
	const good, poor = 5.0, 30.0
	if c.Realtime <= 0 {
		return Dimension{
			Key: "wakelock_burden", Label: "Wakelock 负担", Weight: wWakelock,
			Valid: false, Status: "n/a",
			Detail: "报告期无电池运行时长，无法评估 wakelock 负担",
		}
	}
	v := float64(c.PartialWakelockTimePercentage + c.KernelOverheadTimePercentage)
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "wakelock_burden",
		Label:       "Wakelock 负担",
		Score:       s,
		Weight:      wWakelock,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("partial+ kernel wakelock 占 %.1f%% 时间（健康≤%.0f%%，严重≥%.0f%%）", v, good, poor),
		MetricValue: v,
	}
}

// wakeupDimension scores app wakeup + sync frequency (events per hour).
// Invalid when the report carries no wakeup/sync data (both counters zero
// is treated as "no data" rather than "perfect" to avoid rewarding silence).
func wakeupDimension(h presenter.HistogramStats) Dimension {
	const good, poor = 20.0, 150.0
	v := float64(h.TotalAppWakeupsPerHr) + float64(h.TotalAppSyncsPerHr)
	if v <= 0 && h.TotalAppWakeupsPerHr == 0 && h.TotalAppSyncsPerHr == 0 {
		return Dimension{
			Key: "wakeup_sync_freq", Label: "唤醒/同步频率", Weight: wWakeup,
			Valid: false, Status: "n/a",
			Detail: "无 wakeup / sync 直方图数据",
		}
	}
	s, ok := lerpDown(v, good, poor)
	return Dimension{
		Key:         "wakeup_sync_freq",
		Label:       "唤醒/同步频率",
		Score:       s,
		Weight:      wWakeup,
		Valid:       ok,
		Status:      statusOf(s),
		Detail:      fmt.Sprintf("App 唤醒+同步 %.0f 次/h（健康≤%.0f，严重≥%.0f）", v, good, poor),
		MetricValue: v,
	}
}

// stabilityDimension scores app stability from ANR + crash counts. Invalid
// when the histogram carries no stability counters at all.
func stabilityDimension(h presenter.HistogramStats) Dimension {
	const good, poor = 0.0, 30.0
	if h.TotalAppANRCount < 0 && h.TotalAppCrashCount < 0 {
		return Dimension{
			Key: "app_stability", Label: "App 稳定性", Weight: wStability,
			Valid: false, Status: "n/a",
			Detail: "无 ANR / Crash 统计",
		}
	}
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
