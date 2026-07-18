// Copyright 2016 The Android Open Source Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied,
// See the License for the specific language governing permissions and
// limitations under the License.

package analyzer

import (
	"os"
	"strings"
	"testing"
)

// v2SamplePath points at a real Android 16 / Format:2 bug report used to
// verify the self-contained V2 chart + report generation. The test skips when
// the sample is absent so it stays portable.
const v2SamplePath = "../_samples/bugreport-T952K_EEA-BP2A.250605.031.A3-2026-07-13-22-30-48.txt"

// TestAnalyzeV2Sample verifies that a modern Format:2 bug report gets a
// self-contained V2 SVG chart in PlotHTML (the postProcess fallback) and a
// full ReportHTML page, without requiring the Python Historian pipeline.
func TestAnalyzeV2Sample(t *testing.T) {
	data, err := os.ReadFile(v2SamplePath)
	if err != nil {
		t.Skipf("V2 sample not present, skipping: %v", err)
	}
	results, err := Analyze(string(data))
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no analysis results produced")
	}
	r := results[0]
	if r.CriticalError != "" {
		t.Fatalf("unexpected critical error: %s", r.CriticalError)
	}
	if !strings.Contains(r.PlotHTML, "<svg") || !strings.Contains(r.PlotHTML, "Battery level timeline") {
		t.Errorf("PlotHTML missing the V2 SVG chart (len=%d)", len(r.PlotHTML))
	}
	if !strings.Contains(r.ReportHTML, "Battery Historian report") {
		t.Errorf("ReportHTML missing the report page (len=%d)", len(r.ReportHTML))
	}
	if !strings.Contains(r.ReportHTML, "<svg") {
		t.Errorf("ReportHTML does not embed the battery-level chart")
	}
	if r.Health == nil {
		t.Errorf("Health score was not computed for the V2 sample")
	}
}

// TestParseV2History confirms the Format:2 timeline is actually extracted
// from the sample (battery level lives in token 3 of each history line).
func TestParseV2History(t *testing.T) {
	data, err := os.ReadFile(v2SamplePath)
	if err != nil {
		t.Skipf("V2 sample not present, skipping: %v", err)
	}
	samples, _ := parseV2History(string(data))
	if len(samples) < 2 {
		t.Fatalf("expected multiple V2 samples, got %d", len(samples))
	}
	for _, s := range samples {
		if s.level < 0 || s.level > 100 {
			t.Fatalf("battery level out of range [0,100]: %d", s.level)
		}
	}
}

// TestGenerateV2ChartSVGNegative confirms non-V2 input yields no chart (so the
// MCP chart resource can return a clear "not available" error).
func TestGenerateV2ChartSVGNegative(t *testing.T) {
	if svg := generateV2ChartSVG("this is not a bug report"); svg != "" {
		t.Errorf("expected empty SVG for non-V2 input, got len=%d", len(svg))
	}
}
