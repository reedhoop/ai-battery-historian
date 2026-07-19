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

package procstats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reedhoop/ai-battery-historian/bugreportutils"
)

// TestParse_ViaExtractServiceDump 守护生产路径：
//
//	core.go → procstats.Parse(完整 bugreport)
//
// procstats.Parse 内部会自行调用 ExtractServiceDump 完成段提取，故生产路径是直接
// 把完整 dumpsys 喂给 Parse。本测试既复刻该调用，又单独守卫 ExtractServiceDump
// 接缝。fixture 使用已入库的 testdata/procstats_sample.txt。
func TestParse_ViaExtractServiceDump(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "procstats_sample.txt"))
	if err != nil {
		t.Fatalf("read procstats_sample.txt: %v", err)
	}
	raw := string(b)

	// 守卫 ExtractServiceDump 接缝。
	body := bugreportutils.ExtractServiceDump(raw, "procstats")
	if body == "" {
		t.Fatal("ExtractServiceDump returned empty body")
	}
	if strings.Contains(body, "DUMP OF SERVICE") {
		t.Errorf("extracted body still contains service header")
	}

	// 生产路径：完整 dumpsys 直接喂 Parse。
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(raw): %v", err)
	}
	if got == nil {
		t.Fatal("Parse returned nil Summary")
	}
	if got.CommittedFrom != "2026-07-13-15-35-12" {
		t.Errorf("CommittedFrom = %q, want 2026-07-13-15-35-12", got.CommittedFrom)
	}
	if len(got.Processes) != 7 {
		t.Errorf("Processes len = %d, want 7", len(got.Processes))
	}
}
