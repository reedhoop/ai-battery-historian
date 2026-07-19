# 项目长期记忆：ai-battery-historian

## 项目性质
- 本仓库是 **Google Battery Historian v2 的 fork**（Go + Closure），路径 `D:\02_project_my\03_small_app\ai-battery-historian`。
- 代码归属判定规则：**`Copyright 20xx Google Inc.` = 上游代码，冻结；`Copyright 2026 reedhoop` = 本项目扩展（P3/P4/OEM 功能）**。
- 模块路径前缀：`github.com/reedhoop/ai-battery-historian/...`

## 不可逾越的边界（重构/清理前必读）
- **Google 上游的原始 battery 信息提取一律冻结不动**：`ExtractBatterystatsCheckin` + checkin/CSV 管线（`parseutils`/`checkinparse`/`checkindelta`）、`extractSensorInfo`(sensorservice)、`IsBugReport`、`activity/activity.go`(事件日志 CSV)、`dmesg/dmesg.go`(内核 dmesg CSV) 等上游分析器。
- 任何"统一/重构/优化"类改造，**只适用于本项目扩展功能**（dumpsys 类：power/alarm/procstats/activity/packageutils/wearable 等），不得触碰上游 battery 核心。
- 例外先与用户确认，不可自行扩大改动范围。

## 通用 Section 分级提取方案（设计已定，未实现）
- 报告：`通用段提取方案_洞察报告.md`。把 7 处 `inSection` 扫描循环收敛为配置驱动 + 单遍栈 + Section 树的 `sectionparser` 包。
- 仅纳入本项目 dumpsys 扩展功能；`bugreportutils.go` 是混合文件，迁移时只换本项目新增的 `ExtractServiceDump`，`extractSensorInfo`/`ExtractBatterystatsCheckin` 不动。

## 环境
- 研究 AOSP 源码走本地 WSL：`wsl -u wayne` 读 `/home/wayne/android/aosp/frameworks`（≈AOSP 17）。若 WSL 不可用，退路为 android.googlesource.com 网页 / 用户拷文件 / 用户贴片段。
- 本机可能同时跑多个 battery-historian.exe；验证时务必用确定空闲端口（如 10099），避开被旧版占用的 9999。

## 已移除功能（勿复活）
- 自定义"电池健康度评分"（P3-C）已于 commit `8a271c6` 移除：`analyzer/health`、`query_health`、`bugreport://{id}/health`、`battery_health_report`、`cmd/healthcheck`、结果页健康度卡片、`AnalysisResult.Health`、FR-19。保留原生 Bh/healthd/System Health tab、`query_histogram` 的「健康度直方图」(FR-05)、`metrics.js` HEALTH metric。
