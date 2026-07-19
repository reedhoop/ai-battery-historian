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
	"math"
	"testing"
	"time"

	"github.com/reedhoop/ai-battery-historian/aggregated"
	"github.com/reedhoop/ai-battery-historian/parseutils"
	"github.com/reedhoop/ai-battery-historian/presenter"
)

// approx reports whether a and b are within tol of each other.
func approx(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

// dimScore returns the score of the dimension with the given key, and whether
// it was found.
func dimScore(r *Report, key string) (float64, bool) {
	for _, d := range r.Dimensions {
		if d.Key == key {
			return d.Score, true
		}
	}
	return 0, false
}

// makeFixtures builds one 2h discharge segment (100%→90%, 1h screen-on) plus
// a full-report *Report derived from an equivalent aggregated Checkin.
func makeFixtures() ([]parseutils.ActivitySummary, *Report) {
	seg := parseutils.ActivitySummary{
		StartTimeMs:         0,
		EndTimeMs:           7200000, // 2h
		InitialBatteryLevel: 100,
		FinalBatteryLevel:   90, // drop 10%
		ScreenOnSummary:     parseutils.Dist{TotalDuration: 3600000 * time.Millisecond}, // 1h on, 1h off
	}
	summaries := []parseutils.ActivitySummary{seg}

	// Full report mirrors the same 2h window via aggregated stats.
	full := Evaluate(aggregated.Checkin{
		Realtime:                      7200000 * time.Millisecond,
		ScreenOffRealtime:             3600000 * time.Millisecond,
		ScreenOffDischargeRatePerHr:   aggregated.MFloat32{V: 5}, // 5%/h
		PartialWakelockTimePercentage: 0,
		ModemDischargeRatePerHr:       aggregated.MFloat32{V: 1},
	}, presenter.HistogramStats{
		TotalAppWakeupsPerHr:                  0,
		DeviceIdleModeEnabledTimePercentage:   0,
		TotalAppANRCount:                      0,
		TotalAppCrashCount:                    0,
	})
	return summaries, full
}

func TestEvaluateWindow_FullSpanMatchesWindowableDims(t *testing.T) {
	summaries, full := makeFixtures()

	// Whole-span window should produce the same windowable dimension scores
	// as the full report (same underlying data), proving the windowed path
	// reproduces the whole-report result when asked.
	win := EvaluateWindow(summaries, 3000, 0, 7200000, full)

	if !win.IsWindow {
		t.Fatal("expected IsWindow=true for a windowed report")
	}
	for _, key := range []string{"standby_drain", "wakelock_burden", "wakeup_sync_freq", "doze_adoption"} {
		want, ok1 := dimScore(full, key)
		got, ok2 := dimScore(win, key)
		if !ok1 || !ok2 {
			t.Fatalf("dimension %s missing (full ok=%v win ok=%v)", key, ok1, ok2)
		}
		if !approx(want, got, 1e-6) {
			t.Errorf("windowable dim %s: full score %.4f != window score %.4f", key, want, got)
		}
	}
	// Standby should be ~5%/h → score between good(1.5) and poor(10).
	if s, _ := dimScore(win, "standby_drain"); !approx(s, 58.823, 0.5) {
		t.Errorf("standby score = %.3f, want ~58.823", s)
	}
	// Composite must renormalize the *valid* windowable weights to 1.0
	// internally. Recompute it from the report's own windowable (Weight>0)
	// dimensions and assert the reported score matches.
	var wsum, ssum float64
	for _, d := range win.Dimensions {
		if d.Weight > 0 && d.Valid { // windowable & valid (frozen dims carry Weight 0)
			wsum += d.Weight
			ssum += d.Score * d.Weight
		}
	}
	if wsum == 0 {
		t.Fatal("no valid windowable dimension contributed to the composite")
	}
	if !approx(win.Score, ssum/wsum, 1e-6) {
		t.Errorf("window composite = %.3f, want renormalized %.3f", win.Score, ssum/wsum)
	}
}

func TestEvaluateWindow_SubWindowChangesScore(t *testing.T) {
	summaries, full := makeFixtures()

	// First 1h only: the screen-off discharge is concentrated into a shorter
	// window, so the windowed standby score must differ from the whole-report
	// value (windowing must not simply reproduce the long-run average).
	win := EvaluateWindow(summaries, 3000, 0, 3600000, full)

	fullStandby, _ := dimScore(full, "standby_drain")
	subStandby, _ := dimScore(win, "standby_drain")
	if approx(subStandby, fullStandby, 1.0) {
		t.Errorf("sub-window standby (%.3f) ≈ full (%.3f): windowing had no effect", subStandby, fullStandby)
	}
	if subStandby < 0 || subStandby > 100 {
		t.Errorf("sub-window standby out of [0,100]: %.3f", subStandby)
	}
}

func TestEvaluateWindow_FreezesStabilityAndModem(t *testing.T) {
	summaries, full := makeFixtures()
	win := EvaluateWindow(summaries, 3000, 0, 3600000, full)

	var frozenCount int
	for _, d := range win.Dimensions {
		if d.Key == "app_stability" || d.Key == "modem_activity" {
			frozenCount++
			if d.Weight != 0 {
				t.Errorf("frozen dim %s must have Weight=0, got %.2f", d.Key, d.Weight)
			}
			if !contains(d.Detail, "（全程值）") {
				t.Errorf("frozen dim %s Detail should carry 全程值 marker, got %q", d.Key, d.Detail)
			}
		}
	}
	if frozenCount != 2 {
		t.Errorf("expected 2 frozen dims (stability+modem), got %d", frozenCount)
	}
	// The frozen (全程值) dims carry Weight=0 and must NOT enter the window
	// composite; the 4 windowable dims keep their original weights (sum 0.75),
	// which EvaluateWindow renormalizes internally to 1.0 for the composite.
	var wsum float64
	for _, d := range win.Dimensions {
		if d.Weight != 0 {
			wsum += d.Weight
		}
	}
	if !approx(wsum, 0.75, 1e-6) {
		t.Errorf("windowable weights should sum to 0.75 (un-renormalized stored weights), got %.4f", wsum)
	}
}

func TestEvaluateWindow_NoOverlapIsNA(t *testing.T) {
	summaries, full := makeFixtures()
	// Window entirely after the segment → no data → N/A, IsWindow still true.
	win := EvaluateWindow(summaries, 3000, 8000000, 9000000, full)
	if !win.IsWindow {
		t.Fatal("expected IsWindow=true")
	}
	if win.Grade != "N/A" {
		t.Errorf("expected N/A for non-overlapping window, got grade %q score %.1f", win.Grade, win.Score)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
