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
