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

package power

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
)

// TestParse_ViaExtractServiceDump 守护生产路径：
//
//	core.go → power.Parse(完整 bugreport)
//
// 注意 power.Parse 内部（power.go:103）会自行调用 ExtractServiceDump 完成
// 段提取，因此生产路径是直接把完整 dumpsys 喂给 Parse，而不是预先剥头。
// 本测试一方面直接把完整样本喂给 Parse（复刻生产调用），另一方面单独调一次
// ExtractServiceDump 直接守卫该提取接缝——任一处被改坏，本测试都会失败。
// fixture 使用已入库的 testdata/power_sample.txt，不依赖被 .gitignore 忽略的
// _aosp_review 样本。
func TestParse_ViaExtractServiceDump(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "power_sample.txt"))
	if err != nil {
		t.Fatalf("read power_sample.txt: %v", err)
	}
	raw := string(b)

	// 守卫 ExtractServiceDump 接缝：从完整 dumpsys 抽到非空正文且剥掉了服务头。
	body := bugreportutils.ExtractServiceDump(raw, "power")
	if body == "" {
		t.Fatal("ExtractServiceDump returned empty body")
	}
	if strings.Contains(body, "DUMP OF SERVICE") {
		t.Errorf("extracted body still contains service header:\n%s", body)
	}
	if !strings.Contains(body, "POWER MANAGER (dumpsys power)") {
		t.Errorf("extracted body missing POWER MANAGER header:\n%s", body)
	}

	// 生产路径：完整 dumpsys 直接喂 Parse（Parse 内部再调用 ExtractServiceDump）。
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(raw): %v", err)
	}
	if got == nil {
		t.Fatal("Parse returned nil Summary")
	}
	if got.Wakefulness != "Awake" {
		t.Errorf("Wakefulness = %q, want Awake", got.Wakefulness)
	}
	if !got.IsPowered {
		t.Error("IsPowered = false, want true")
	}
	if got.BatteryLevel != 13 {
		t.Errorf("BatteryLevel = %d, want 13", got.BatteryLevel)
	}
}
