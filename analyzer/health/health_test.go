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

package health

import (
	"strings"
	"testing"
	"time"

	"github.com/reedhoop/ai-battery-historian/aggregated"
	"github.com/reedhoop/ai-battery-historian/presenter"
)

func checkin(offRate, partial, kernel, modem float32, offRealtime time.Duration) aggregated.Checkin {
	return aggregated.Checkin{
		Realtime:                      time.Hour,
		ScreenOffRealtime:             offRealtime,
		ScreenOffDischargeRatePerHr:   aggregated.MFloat32{V: offRate},
		PartialWakelockTimePercentage: partial,
		KernelOverheadTimePercentage:  kernel,
		ModemDischargeRatePerHr:       aggregated.MFloat32{V: modem},
	}
}

// checkinFull is like checkin but also sets FullWakelockTimePercentage so tests
// can exercise the corrected wakelock-burden metric.
func checkinFull(offRate, partial, kernel, full, modem float32, offRealtime time.Duration) aggregated.Checkin {
	return aggregated.Checkin{
		Realtime:                      time.Hour,
		ScreenOffRealtime:             offRealtime,
		ScreenOffDischargeRatePerHr:   aggregated.MFloat32{V: offRate},
		PartialWakelockTimePercentage: partial,
		KernelOverheadTimePercentage:  kernel,
		FullWakelockTimePercentage:    full,
		ModemDischargeRatePerHr:       aggregated.MFloat32{V: modem},
	}
}

func hist(wakeups, syncs float32, anr, crash int32, doze float32) presenter.HistogramStats {
	return presenter.HistogramStats{
		TotalAppWakeupsPerHr:                wakeups,
		TotalAppSyncsPerHr:                  syncs,
		TotalAppANRCount:                    anr,
		TotalAppCrashCount:                  crash,
		DeviceIdleModeEnabledTimePercentage: doze,
	}
}

func TestEvaluate_Healthy(t *testing.T) {
	c := checkin(1.0, 2, 1, 0.5, time.Hour)
	h := hist(5, 2, 0, 0, 55)
	r := Evaluate(c, h)

	if r.Score < 85 {
		t.Errorf("healthy score = %.1f, want >= 85", r.Score)
	}
	if r.Grade != "A" {
		t.Errorf("healthy grade = %q, want A", r.Grade)
	}
	if len(r.Alerts) != 0 {
		t.Errorf("healthy alerts = %d, want 0: %+v", len(r.Alerts), r.Alerts)
	}
	if !r.Dimensions[0].Valid {
		t.Errorf("standby dimension should be valid when ScreenOffRealtime>0")
	}
}

func TestEvaluate_Poor(t *testing.T) {
	c := checkin(15, 25, 10, 10, time.Hour)
	h := hist(200, 50, 10, 30, 0)
	r := Evaluate(c, h)

	if r.Score >= 40 {
		t.Errorf("poor score = %.1f, want < 40", r.Score)
	}
	if r.Grade != "F" {
		t.Errorf("poor grade = %q, want F", r.Grade)
	}
	if len(r.Alerts) < 4 {
		t.Errorf("poor alerts = %d, want >= 4: %+v", len(r.Alerts), r.Alerts)
	}
	hasCritical := false
	for _, a := range r.Alerts {
		if a.Level == "critical" {
			hasCritical = true
		}
	}
	if !hasCritical {
		t.Errorf("poor report should contain at least one critical alert")
	}
}

func TestEvaluate_Moderate(t *testing.T) {
	c := checkin(4, 12, 4, 3, time.Hour)
	h := hist(60, 20, 0, 0, 15)
	r := Evaluate(c, h)

	if r.Score < 40 || r.Score >= 85 {
		t.Errorf("moderate score = %.1f, want in [40,85)", r.Score)
	}
}

func TestEvaluate_StandbyInvalidExcluded(t *testing.T) {
	// No screen-off time => standby dimension is not scoreable and excluded.
	c := checkin(0, 2, 1, 0.5, 0)
	h := hist(5, 2, 0, 0, 55)
	r := Evaluate(c, h)

	if r.Dimensions[0].Key != "standby_drain" {
		t.Fatalf("dimension[0] key = %q, want standby_drain", r.Dimensions[0].Key)
	}
	if r.Dimensions[0].Valid {
		t.Errorf("standby dimension should be invalid when ScreenOffRealtime==0")
	}
	if r.Score < 0 || r.Score > 100 {
		t.Errorf("composite score = %.1f, want within [0,100]", r.Score)
	}
}

func TestEvaluate_Deterministic(t *testing.T) {
	c := checkin(3, 8, 3, 2, time.Hour)
	h := hist(40, 15, 1, 2, 25)
	a := Evaluate(c, h)
	b := Evaluate(c, h)
	if a.Score != b.Score {
		t.Errorf("Evaluate not deterministic: %.3f vs %.3f", a.Score, b.Score)
	}
}

// TestWakelock_IgnoresKernelOverhead locks in the P0 fix: the wakelock-burden
// metric must score partial+full wakelock time, NOT screen-off uptime. Here the
// device is awake only 5% of the time (partial=5, full=0); the leftover 85%
// screen-off time is captured by KernelOverheadTimePercentage and must NOT be
// summed in. The old code produced v≈90 → "critical" on every overnight report.
func TestWakelock_IgnoresKernelOverhead(t *testing.T) {
	c := checkinFull(2, 5, 85, 0, 0.5, time.Hour)
	h := hist(5, 2, 0, 0, 55)
	r := Evaluate(c, h)
	d := r.Dimensions[1] // wakelock_burden
	if d.Key != "wakelock_burden" {
		t.Fatalf("dimension[1] key = %q, want wakelock_burden", d.Key)
	}
	if !d.Valid {
		t.Fatalf("wakelock dimension should be valid")
	}
	if d.MetricValue != 5 {
		t.Errorf("wakelock metricValue = %.1f, want 5 (partial+full, kernel excluded)", d.MetricValue)
	}
	if d.Status == "poor" {
		t.Errorf("wakelock scored poor (%.1f) — kernel overhead leaked in; detail=%q", d.Score, d.Detail)
	}
}

// TestWakeupSync_NotCountedAsWakeups locks in the P1 fix: TotalAppSyncsPerHr is
// a sync *duration* rate (seconds/hour), a different unit from wakeup counts, so
// it must not be summed into the scored metric. 10 wakeups/h is healthy; 500
// sync-seconds/h must surface as context only, not inflate the score to "次/h".
func TestWakeupSync_NotCountedAsWakeups(t *testing.T) {
	c := checkin(1.0, 2, 1, 0.5, time.Hour)
	h := hist(10, 500, 0, 0, 55)
	r := Evaluate(c, h)
	d := r.Dimensions[2] // wakeup_sync_freq
	if d.MetricValue != 10 {
		t.Errorf("wakeup metricValue = %.1f, want 10 (sync seconds must not be added)", d.MetricValue)
	}
	if d.Status == "poor" {
		t.Errorf("wakeup scored poor (%.1f) with only 10 wakeups/h; detail=%q", d.Score, d.Detail)
	}
	if !strings.Contains(d.Detail, "秒/h") {
		t.Errorf("wakeup detail should surface sync duration; got %q", d.Detail)
	}
}

// TestStability_AlwaysValid locks in the P2 fix: the old guard
// `ANRCount<0 && CrashCount<0` was dead code (counts are non-negative). A report
// with 0 ANR / 0 crash must still be scored as healthy (valid, score 100).
func TestStability_AlwaysValid(t *testing.T) {
	c := checkin(1.0, 2, 1, 0.5, time.Hour)
	r := Evaluate(c, hist(0, 0, 0, 0, 55))
	d := r.Dimensions[3] // app_stability
	if !d.Valid {
		t.Errorf("stability should be valid with 0 ANR/0 crash")
	}
	if d.Score != 100 || d.Status != "good" {
		t.Errorf("stability = %.1f/%q, want 100/good (0 crashes = healthy)", d.Score, d.Status)
	}
}

// TestEvaluatedCount_Tracking locks in the P2 display-precision fix: the report
// must report how many of the 6 dimensions were actually scored so the composite
// is not mistaken for a full evaluation.
func TestEvaluatedCount_Tracking(t *testing.T) {
	// All 6 dimensions scoreable.
	rAll := Evaluate(checkin(1.0, 2, 1, 0.5, time.Hour), hist(5, 2, 0, 0, 55))
	if rAll.Total != 6 {
		t.Errorf("total = %d, want 6", rAll.Total)
	}
	if rAll.Evaluated != 6 {
		t.Errorf("evaluated = %d, want 6 (all dims valid)", rAll.Evaluated)
	}

	// No screen-off time → standby dimension invalid → 5/6.
	rPartial := Evaluate(checkin(0, 2, 1, 0.5, 0), hist(5, 2, 0, 0, 55))
	if rPartial.Total != 6 {
		t.Errorf("total = %d, want 6", rPartial.Total)
	}
	if rPartial.Evaluated != 5 {
		t.Errorf("evaluated = %d, want 5 (standby invalid)", rPartial.Evaluated)
	}
}
