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

// Package historianutils 中与 AOSP dumpsys 段分隔符对齐的正则回归测试。
//
// ServiceDumpRE 是最关键的正则：它必须与 AOSP dumpsys.cpp 的
// writeDumpHeader 对齐（含 CRITICAL/HIGH/NORMAL 优先级前缀）。该正则的
// 早期版本曾因把 \s+ 错放进可选组内部，导致 `DUMP OF SERVICE CRITICAL power:`
// 整行失配——本文件把这些边界全部锁成测试，防止回归。
package historianutils

import (
	"testing"
)

func matchService(t *testing.T, line string) (bool, string) {
	t.Helper()
	if m, result := SubexpNames(ServiceDumpRE, line); m {
		return true, result["service"]
	}
	return false, ""
}

func TestServiceDumpRE(t *testing.T) {
	cases := []struct {
		line  string
		want  string
		match bool
	}{
		{"DUMP OF SERVICE power:", "power", true},
		{"DUMP OF SERVICE CRITICAL power:", "power", true}, // 优先级前缀对齐（关键）
		{"DUMP OF SERVICE HIGH alarm:", "alarm", true},
		{"DUMP OF SERVICE NORMAL procstats:", "procstats", true},
		{"DUMP OF SERVICE activity:", "activity", true},
		{"DUMP OF SERVICE", "", false},        // 缺少冒号与服务名
		{"dump of service power:", "", false}, // 大小写敏感
		{"DUMP OF SERVICE power", "", false},  // 缺冒号
	}
	for _, c := range cases {
		got, val := matchService(t, c.line)
		if got != c.match {
			t.Errorf("ServiceDumpRE.MatchString(%q) = %v, want %v", c.line, got, c.match)
			continue
		}
		if c.match && val != c.want {
			t.Errorf("ServiceDumpRE service of %q = %q, want %q", c.line, val, c.want)
		}
	}
}

// TestSubexpNames 锁定 SubexpNames 的 TrimSpace 防御与未匹配返回。
func TestSubexpNames(t *testing.T) {
	// 前后空白应被忽略，仍能正确捕获 service。
	if m, result := SubexpNames(ServiceDumpRE, "   DUMP OF SERVICE power:   "); !m {
		t.Fatal("SubexpNames(trimmed) = false, want true")
	} else if result["service"] != "power" {
		t.Errorf("service = %q, want power", result["service"])
	}

	// 非匹配应返回 false 与 nil map。
	if m, result := SubexpNames(ServiceDumpRE, "unrelated line"); m {
		t.Errorf("SubexpNames(unrelated) = true, want false (result=%v)", result)
	}
}
