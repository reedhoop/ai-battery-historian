package analyzer

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"github.com/reedhoop/ai-battery-historian/presenter"
)

// TestVerifyHealthCardRender is a throwaway verification for WebUI ③:
// it confirms (A) the whole result-page template set still parses after the
// health-card injection, and (B) the health_card partial renders correctly
// (else-if chains, printf, status->label mapping, N/A for unscored dims).
func TestVerifyHealthCardRender(t *testing.T) {
	// (A) Parsing the entire resultTempl must succeed.
	InitTemplates("../templates")
	if resultTempl == nil {
		t.Fatal("resultTempl not initialized after InitTemplates")
	}

	// (B) Isolated render of the health_card partial.
	hr := &presenter.HealthReport{
		Score:   72,
		Grade:   "B",
		Summary: "电池健康度 B（72/100）：主要扣分项 待机放电率。",
		Dimensions: []presenter.HealthDimension{
			{Label: "待机放电率", Score: 60, Status: "fair", Detail: "灭屏放电 6.0%/h"},
			{Label: "Modem 活动", Score: 0, Status: "n/a", Detail: "无 Modem 活动数据"},
		},
		Alerts: []presenter.HealthAlert{
			{Level: "warning", Category: "standby_drain", Message: "灭屏待机掉电过快", Value: "6.0%/h"},
		},
	}
	tpl, err := template.ParseFiles("../templates/health_card.html")
	if err != nil {
		t.Fatalf("parse health_card.html: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "health_card", hr); err != nil {
		t.Fatalf("execute health_card: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"电池健康度", "72", "B", "alert-warning", "待机放电率", "N/A", "无 Modem 活动数据"} {
		if !strings.Contains(out, want) {
			t.Errorf("health_card output missing %q\n--- output ---\n%s", want, out)
		}
	}
}
