// Copyright 2016 The Android Open Source Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package analyzer

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// This file provides a self-contained, dependency-free way to render a
// battery-level timeline chart for modern (Format: 2 / absolute-timestamp)
// bug reports, which the legacy scripts/historian.py cannot parse.
//
// The generated output is inline SVG wrapped in a tiny HTML document, so it
// renders in any browser with no external JS/CSS assets (the rich
// historianV2 chart normally needs a Closure-compiled bundle that is not
// present in every checkout).

var (
	// v2HeaderRE matches the "Battery History [Format: N]" section header.
	v2HeaderRE = regexp.MustCompile(`Battery History \[Format:\s*(\d+)\]`)
	// v2ResetRE matches "RESET:TIME: YYYY-MM-DD-HH-MM-SS".
	v2ResetRE = regexp.MustCompile(`RESET:TIME:\s*(\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2})`)
	// v2LineRE matches a Format:2 data line:
	//   "MM-DD HH:MM:SS.mmm <level%> <cmdHex> [key=value...] [+/-events...]"
	v2LineRE = regexp.MustCompile(`^(\d{2})-(\d{2})\s+(\d{2}):(\d{2}):(\d{2})\.(\d{3})\s+(\d{1,3})\s+([0-9a-fA-F]+)\b`)
)

type v2Sample struct {
	t        time.Time
	level    int
	charging bool
	screen   bool
}

// parseV2History extracts the battery-level timeline from the
// "Battery History [Format: 2]" section of a bug report. Returns the samples
// and the year (taken from the RESET:TIME marker, defaulting to the current
// year when absent).
func parseV2History(contents string) ([]v2Sample, int) {
	var samples []v2Sample
	year := time.Now().Year()
	inSection := false
	curCharging, curScreen := false, false

	for _, raw := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(raw)
		if !inSection {
			if m := v2HeaderRE.FindStringSubmatch(line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil && n >= 2 {
					inSection = true
				}
			}
			continue
		}
		// A "Stats:" line marks the end of the history block.
		if strings.HasPrefix(line, "Stats:") {
			break
		}
		if m := v2ResetRE.FindStringSubmatch(line); m != nil {
			if t, err := time.Parse("2006-01-02-15-04-05", m[1]); err == nil {
				year = t.Year()
			}
			continue
		}
		m := v2LineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		month, _ := strconv.Atoi(m[1])
		day, _ := strconv.Atoi(m[2])
		hour, _ := strconv.Atoi(m[3])
		min, _ := strconv.Atoi(m[4])
		sec, _ := strconv.Atoi(m[5])
		ms, _ := strconv.Atoi(m[6])
		level, _ := strconv.Atoi(m[7])
		t := time.Date(year, time.Month(month), day, hour, min, sec, ms*1e6, time.UTC)

		// Update the running charger/screen state from events on this line.
		// Events only appear when the state changes, so we carry the last
		// known state forward between changes.
		for _, tok := range strings.Fields(line) {
			switch tok {
			case "+plugged", "+charging":
				curCharging = true
			case "-plugged", "-charging":
				curCharging = false
			case "+screen":
				curScreen = true
			case "-screen":
				curScreen = false
			}
		}
		samples = append(samples, v2Sample{t: t, level: level, charging: curCharging, screen: curScreen})
	}
	return samples, year
}

// generateV2ChartSVG returns a self-contained HTML document containing an SVG
// chart of battery level (%) over time, with charging periods shaded. It
// returns "" when no Format:2 history is present.
func generateV2ChartSVG(contents string) string {
	samples, _ := parseV2History(contents)
	if len(samples) < 2 {
		return ""
	}
	// Downsample to keep the SVG reasonably sized.
	const maxPoints = 2000
	if len(samples) > maxPoints {
		step := float64(len(samples)) / float64(maxPoints)
		kept := make([]v2Sample, 0, maxPoints)
		for i := 0; i < maxPoints; i++ {
			kept = append(kept, samples[int(float64(i)*step)])
		}
		samples = kept
	}

	start := samples[0].t
	totalMs := samples[len(samples)-1].t.Sub(start).Milliseconds()
	if totalMs <= 0 {
		totalMs = 1
	}

	const (
		W, H         = 920.0, 400.0
		padL, padR    = 55.0, 20.0
		padT, padB    = 42.0, 48.0
	)
	plotW := W - padL - padR
	plotH := H - padT - padB

	x := func(t time.Time) float64 {
		return padL + (float64(t.Sub(start).Milliseconds())/float64(totalMs))*plotW
	}
	y := func(level int) float64 {
		return padT + (1.0-float64(level)/100.0)*plotH
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8">`)
	b.WriteString(`<title>Battery level timeline</title>`)
	b.WriteString(`<style>body{font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;margin:0;background:#fff}`)
	b.WriteString(`.chart-wrap{display:flex;justify-content:center;padding:16px}`)
	b.WriteString(`text{font-family:inherit}</style></head><body>`)
	b.WriteString(`<div class="chart-wrap"><svg xmlns="http://www.w3.org/2000/svg" width="`)
	b.WriteString(strconv.FormatFloat(W, 'f', 0, 64))
	b.WriteString(`" height="`)
	b.WriteString(strconv.FormatFloat(H, 'f', 0, 64))
	b.WriteString(`" viewBox="0 0 `)
	b.WriteString(strconv.FormatFloat(W, 'f', 0, 64))
	b.WriteString(` `)
	b.WriteString(strconv.FormatFloat(H, 'f', 0, 64))
	b.WriteString(`" role="img" aria-label="Battery level over time">`)

	// Plot background.
	b.WriteString(`<rect x="`)
	b.WriteString(strconv.FormatFloat(padL, 'f', 1, 64))
	b.WriteString(`" y="`)
	b.WriteString(strconv.FormatFloat(padT, 'f', 1, 64))
	b.WriteString(`" width="`)
	b.WriteString(strconv.FormatFloat(plotW, 'f', 1, 64))
	b.WriteString(`" height="`)
	b.WriteString(strconv.FormatFloat(plotH, 'f', 1, 64))
	b.WriteString(`" fill="#fafafa" stroke="#e0e0e0"/>`)

	// Y gridlines + labels (0,20,40,60,80,100).
	for lv := 0; lv <= 100; lv += 20 {
		gy := y(lv)
		b.WriteString(`<line x1="`)
		b.WriteString(strconv.FormatFloat(padL, 'f', 1, 64))
		b.WriteString(`" y1="`)
		b.WriteString(strconv.FormatFloat(gy, 'f', 1, 64))
		b.WriteString(`" x2="`)
		b.WriteString(strconv.FormatFloat(padL+plotW, 'f', 1, 64))
		b.WriteString(`" y2="`)
		b.WriteString(strconv.FormatFloat(gy, 'f', 1, 64))
		b.WriteString(`" stroke="#ececec"/>`)
		b.WriteString(`<text x="`)
		b.WriteString(strconv.FormatFloat(padL-8, 'f', 1, 64))
		b.WriteString(`" y="`)
		b.WriteString(strconv.FormatFloat(gy+4, 'f', 1, 64))
		b.WriteString(`" font-size="11" fill="#666" text-anchor="end">`)
		b.WriteString(strconv.Itoa(lv))
		b.WriteString(`</text>`)
	}

	// X gridlines + labels (5 evenly spaced ticks).
	for i := 0; i <= 4; i++ {
		frac := float64(i) / 4.0
		tx := padL + frac*plotW
		b.WriteString(`<line x1="`)
		b.WriteString(strconv.FormatFloat(tx, 'f', 1, 64))
		b.WriteString(`" y1="`)
		b.WriteString(strconv.FormatFloat(padT, 'f', 1, 64))
		b.WriteString(`" x2="`)
		b.WriteString(strconv.FormatFloat(tx, 'f', 1, 64))
		b.WriteString(`" y2="`)
		b.WriteString(strconv.FormatFloat(padT+plotH, 'f', 1, 64))
		b.WriteString(`" stroke="#ececec"/>`)
		elapsedMs := int64(frac * float64(totalMs))
		b.WriteString(`<text x="`)
		b.WriteString(strconv.FormatFloat(tx, 'f', 1, 64))
		b.WriteString(`" y="`)
		b.WriteString(strconv.FormatFloat(padT+plotH+18, 'f', 1, 64))
		b.WriteString(`" font-size="11" fill="#666" text-anchor="middle">`)
		b.WriteString(html.EscapeString(fmtDuration(elapsedMs)))
		b.WriteString(`</text>`)
	}

	// Charging bands.
	for i := 0; i < len(samples)-1; i++ {
		if samples[i].charging {
			x1 := x(samples[i].t)
			x2 := x(samples[i+1].t)
			if x2 < x1 {
				x2 = x1
			}
			b.WriteString(`<rect x="`)
			b.WriteString(strconv.FormatFloat(x1, 'f', 1, 64))
			b.WriteString(`" y="`)
			b.WriteString(strconv.FormatFloat(padT, 'f', 1, 64))
			b.WriteString(`" width="`)
			b.WriteString(strconv.FormatFloat(x2-x1, 'f', 1, 64))
			b.WriteString(`" height="`)
			b.WriteString(strconv.FormatFloat(plotH, 'f', 1, 64))
			b.WriteString(`" fill="#e8f5e9"/>`)
		}
	}

	// Battery level polyline.
	b.WriteString(`<polyline fill="none" stroke="#1565c0" stroke-width="2" points="`)
	for _, s := range samples {
		b.WriteString(strconv.FormatFloat(x(s.t), 'f', 1, 64))
		b.WriteString(`,`)
		b.WriteString(strconv.FormatFloat(y(s.level), 'f', 1, 64))
		b.WriteString(` `)
	}
	b.WriteString(`"/>`)

	// Axis titles + title.
	b.WriteString(`<text x="`)
	b.WriteString(strconv.FormatFloat(padL+plotW/2, 'f', 1, 64))
	b.WriteString(`" y="`)
	b.WriteString(strconv.FormatFloat(H-8, 'f', 1, 64))
	b.WriteString(`" font-size="12" fill="#444" text-anchor="middle">Elapsed time (green = charging)</text>`)
	b.WriteString(`<text x="14" y="`)
	b.WriteString(strconv.FormatFloat(padT+plotH/2, 'f', 1, 64))
	b.WriteString(`" font-size="12" fill="#444" text-anchor="middle" transform="rotate(-90 14 `)
	b.WriteString(strconv.FormatFloat(padT+plotH/2, 'f', 1, 64))
	b.WriteString(`)">Battery level (%)</text>`)
	b.WriteString(`<text x="`)
	b.WriteString(strconv.FormatFloat(W/2, 'f', 1, 64))
	b.WriteString(`" y="22" font-size="14" fill="#222" text-anchor="middle">Battery level timeline</text>`)

	b.WriteString(`</svg></div></body></html>`)
	return b.String()
}

// fmtDuration renders a millisecond span compactly, e.g. "3h12m" or "1d4h".
func fmtDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	d := ms / 86400000
	ms -= d * 86400000
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	if d > 0 {
		return fmt.Sprintf("%dd%dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", ms/1000)
}

// generateReportHTML builds a self-contained HTML report page for one analysis
// result: device metadata, the battery-level chart, and key aggregate stats.
// No external assets are referenced.
//
// plotHTML is the already-resolved chart HTML (either the Python Historian
// plot from AnalyzeWithChart, or the inline-SVG fallback from
// generateV2ChartSVG filled by postProcess). It is embedded verbatim so the
// report page shows the exact same chart served by the chart resource.
func generateReportHTML(r *AnalysisResult, plotHTML string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Battery Historian report</title>`)
	b.WriteString(`<style>
body{font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;margin:0;color:#222;background:#f5f6f8}
.wrap{max-width:1000px;margin:0 auto;padding:24px}
h1{font-size:20px;margin:0 0 4px}
.meta{color:#666;font-size:13px;margin-bottom:20px}
.card{background:#fff;border:1px solid #e3e6ea;border-radius:10px;padding:18px;margin-bottom:20px;box-shadow:0 1px 2px rgba(0,0,0,.04)}
table{border-collapse:collapse;width:100%;font-size:13px}
td,th{text-align:left;padding:6px 10px;border-bottom:1px solid #eee}
th{color:#666;font-weight:600}
.k{color:#666}
</style></head><body><div class="wrap">`)

	b.WriteString(`<h1>`)
	b.WriteString(html.EscapeString(r.FileName))
	b.WriteString(`</h1><div class="meta">`)
	meta := []string{}
	if r.DeviceModel != "" {
		meta = append(meta, "Device: "+html.EscapeString(r.DeviceModel))
	}
	if r.SDKVersion != 0 {
		meta = append(meta, fmt.Sprintf("SDK: %d", r.SDKVersion))
	}
	if r.CriticalError != "" {
		meta = append(meta, "Parse error: "+html.EscapeString(r.CriticalError))
	}
	b.WriteString(strings.Join(meta, " &middot; "))
	b.WriteString(`</div>`)

	// Chart: embed the already-resolved PlotHTML so /report and /chart agree.
	if plotHTML != "" {
		b.WriteString(`<div class="card">`)
		b.WriteString(plotHTML)
		b.WriteString(`</div>`)
	}

	// Key aggregate stats.
	c := r.Checkin
	b.WriteString(`<div class="card"><h3 style="margin-top:0">Key aggregate stats</h3><table>`)
	row := func(k string, v string) {
		b.WriteString(`<tr><td class="k">`)
		b.WriteString(html.EscapeString(k))
		b.WriteString(`</td><td>`)
		b.WriteString(html.EscapeString(v))
		b.WriteString(`</td></tr>`)
	}
	if c.ScreenOffDischargeRatePerHr.V != 0 {
		row("Screen-off discharge", fmt.Sprintf("%.2f %%/h", c.ScreenOffDischargeRatePerHr.V))
	}
	row("Partial wakelock time", fmt.Sprintf("%.1f %%", c.PartialWakelockTimePercentage))
	row("Kernel overhead time", fmt.Sprintf("%.1f %%", c.KernelOverheadTimePercentage))
	if c.ModemDischargeRatePerHr.V != 0 {
		row("Modem discharge", fmt.Sprintf("%.2f %%/h", c.ModemDischargeRatePerHr.V))
	}
	row("Doze / device-idle time", fmt.Sprintf("%.1f %%", c.DeviceIdleModeEnabledTimePercentage))
	row("Total app wakeups", fmt.Sprintf("%.1f /h", c.TotalAppWakeupsPerHr))
	row("App ANR count", fmt.Sprintf("%d", c.TotalAppANRCount))
	row("App crash count", fmt.Sprintf("%d", c.TotalAppCrashCount))
	b.WriteString(`</table></div>`)

	b.WriteString(`</div></body></html>`)
	return b.String()
}
