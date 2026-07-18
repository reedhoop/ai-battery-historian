# Battery Historian · AI/MCP 能力需求矩阵

> 配套文档：`MCP可行性评估.md`
> 目标：让 AI 助手（Claude / WorkBuddy / Cursor 等）通过 MCP 协议直接解析 Android bugreport、做耗电根因分析与 A/B 对比。
> 编号规则：FR = 功能需求，NFR = 非功能/工程需求。阶段：P1=独立包裹 Demo（Form A），P2=原生嵌入（Form B），P3=增强。

> **实施状态（2026-07-19 校准）**：FR-01..FR-16 + NFR-01..NFR-06 均已交付（除 P3-A Python 3 迁移未做）。FR-17/18/19 为 P3-B/C 新增需求，亦已交付。下表"实施状态"列反映 Form A（`mcp-server/`，v0.1.0）/ Form B（`cmd/battery-historian --mcp`，v0.2.0）的实际落地情况。**import 路径 bug 已修复**：6 个误用 `github.com/google/battery-historian/...` 旧路径的文件已批量替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` / `go vet ./analyzer/... ./cmd/battery-historian/...` / `go test ./analyzer/...` 全部通过。同步完成 code review 发现的 P0/P1 安全加固 + P2/P3 健康度优化（详见 §六.3）。

---

## 一、需求优先级说明

| 优先级 | 含义 | 说明 |
|---|---|---|
| P0 | 必须有 | 缺它 MCP 无法交付核心价值 |
| P1 | 应该有 | 首版即建议提供，决定可用性 |
| P2 | 可以有 | 增强体验，可后续迭代 |

---

## 二、主需求矩阵（功能 + 工程）

| ID | 需求 | 类型 | 对应 MCP 能力 | 底层复用（源码定位） | 优先级 | 阶段 | 工作量 | 实施状态 |
|---|---|---|---|---|---|---|---|---|
| FR-01 | 解析单个 bugreport 并返回结构化结果 | 功能 | Tool `analyze_bugreport(path\|content)` | `analyzer.Analyze` (`analyzer/core.go:84`) → `AnalysisResults` | P0 | P1 | 0.5d | ✅ A+B |
| FR-02 | 两个 bugreport 的耗电差分 | 功能 | Tool `compare_bugreports(a,b)` | `analyzer.Compare` (`core.go:113`) → `CompareResult`（含 `presenter.CombineCheckinData` `presenter.go:421`） | P0 | P1 | 0.5d | ✅ A+B |
| FR-03 | 查询系统级聚合指标 | 功能 | Tool `query_system_stats(id)` | `AnalysisResult.Checkin`（`aggregated.Checkin` `aggregated_stats.go:298`） | P0 | P1 | 0.25d | ✅ A+B |
| FR-04 | 查询应用级耗电（Top-N / 指定 uid） | 功能 | Tool `query_app_stats(id, uid?, topN?)` | `AnalysisResult.AppStats`（`presenter.AppStat` `presenter.go:251` 内部组装） | P0 | P1 | 0.5d | ✅ A+B |
| FR-05 | 查询健康度直方图指标 | 功能 | Tool `query_histogram(id)` | `AnalysisResult.HistogramStats`（由 `analyzer.extractHistogramStats` `analyzer.go:609` 内部产出） | P1 | P1 | 0.25d | ✅ A+B |
| FR-06 | 查询 Userspace/Kernel wakelock 明细 | 功能 | Tool `query_wakelocks(id, kind?)` | `Checkin.UserspaceWakelocks/KernelWakelocks` | P1 | P2 | 0.25d | ✅ B only |
| FR-07 | 查询唤醒原因 | 功能 | Tool `query_wakeup_reasons(id)` | `Checkin.WakeupReasons` + `wakeupreason` 解码 | P1 | P2 | 0.25d | ✅ B only |
| FR-08 | 查询同步任务 | 功能 | Tool `query_sync_tasks(id)` | `Checkin.SyncTasks` | P2 | P2 | 0.25d | ✅ B only |
| FR-09 | 以 Resource 暴露系统指标 | 功能 | Resource `bugreport://{id}/system_stats` | `aggregated.Checkin` → JSON | P1 | P2 | 0.25d | ✅ A+B |
| FR-10 | 以 Resource 暴露单应用指标 | 功能 | Resource `bugreport://{id}/app_stats/{uid}` | `presenter.AppStat` → JSON | P1 | P2 | 0.25d | ✅ A+B |
| FR-11 | 以 Resource 暴露原始 checkin（proto→json） | 功能 | Resource `bugreport://{id}/raw_checkin` | `bspb.BatteryStats` + `jsonpb` | P2 | P2 | 0.25d | ✅ A+B |
| FR-12 | 根因分析提示词模板 | 功能 | Prompt `battery_root_cause` | 无（提示词工程） | P1 | P1 | 0.25d | ✅ A+B |
| FR-13 | A/B 报告提示词模板 | 功能 | Prompt `battery_ab_report` | 无（提示词工程） | P1 | P1 | 0.25d | ✅ A+B |
| FR-14 | 大文件/长耗时处理 | 功能 | 所有解析类 Tool | `maxFileSize=100MB` (`analyzer.go:60`)；Form A HTTP 超时 5 分钟 (`mcp-server/historian.go:24`) | P1 | P1/P2 | 0.5d | ✅ A+B |
| FR-15 | 结果体积控制（Top-N / 分页） | 功能 | 所有列表类 Tool | MCP 层按 `AppStat.DevicePowerPrediction` (`presenter.go:61`) 降序自排序（`presenter.parseAppStats` 仅按应用名排，不可复用） | P1 | P1 | 0.5d | ✅ A+B |
| FR-16 | SDK 版本守卫 | 功能 | 所有解析类 Tool | `minSupportedSDK=21` (`analyzer.go:62`)；`criticalError` 字段 | P1 | P1 | 0.25d | ✅ A+B |
| FR-17 | 以 Resource 暴露图表 HTML（P3-B） | 功能 | Resource `bugreport://{id}/chart` | `AnalysisResult.PlotHTML`（`generateV2ChartSVG` `chart_v2.go:119` fallback，或 `AnalyzeWithChart` 走 Python） | P2 | P3-B | 0.5d | ✅ B only |
| FR-18 | 以 Resource 暴露分析报告页（P3-B） | 功能 | Resource `bugreport://{id}/report` | `AnalysisResult.ReportHTML`（`generateReportHTML` `chart_v2.go:301`） | P2 | P3-B | 0.5d | ✅ B only |
| FR-19 | 健康度评分查询（P3-C） | 功能 | Tool `query_health(id)` + Resource `bugreport://{id}/health` + Prompt `battery_health_report` | `AnalysisResult.Health`（`health.Evaluate` `analyzer/health/health.go:81`，6 维度加权） | P2 | P3-C | 1d | ✅ B only |
| NFR-01 | 构建模块化（引入 go.mod） | 工程 | — | module `github.com/reedhoop/ai-battery-historian`，go 1.25.5（全仓 import 已替换，含 P3-B/C 新增 6 文件） | P0 | P1/P2 | 0.5d | ✅ 主体完成 |
| NFR-02 | MCP 路径不依赖 Python 2.7 | 工程 | — | Form B `ParsedData.skipPlot=true` (`core.go:85`) 跳过 `generateHistorianPlot`；Form A 仍触发 `doHistorian`（`analyzer.go:1043`），Python 缺失仅报错 | P0 | P1 | 0.5d(A)/1.5d(B) | ✅ A+B |
| NFR-03 | protobuf 依赖共存 | 工程 | — | `golang/protobuf v1.3.5` ↔ `google.golang.org/protobuf`（mcp-go v0.56.0 间接引入）共存 | P1 | P2 | 0.5d | ✅ 编译通过 |
| NFR-04 | 安全：文件大小与路径穿越 | 工程 | — | `maxFileSize` (`analyzer.go:60`) + Form B `mcp.go` 加固：base64 **编码长度预检**（`maxEncodedLen`）避免解码 DoS、`filepath.Clean` + `IsRegular()` 拒绝目录/设备/socket；`bugreportutils.Contents` 校验；三个 prompt handler 加 `wrapUserData` 注入防护 | P1 | P1 | 0.25d | ✅ A+B（B 已 P0/P1 加固） |
| NFR-05 | 传输方式 | 工程 | — | stdio 默认；streamable HTTP 可选（Form A `--transport=http` / Form B `--mcp_transport=http`） | P1 | P1 | 0.25d | ✅ A+B |
| NFR-06 | 错误透传 | 工程 | — | `uploadResponse.CriticalError/Note` → `AnalysisResult.CriticalError/Note` | P1 | P1 | 0.25d | ✅ A+B |

---

## 三、按阶段归集的需求包

### P1 · 独立 MCP 进程包裹 Demo ✅ 已完成（Form A）
- **形态 A（HTTP 代理 `POST /historian/`）**：`mcp-server/` 独立 module（`github.com/google/battery-historian/mcp-server`，v0.1.0），5 tools / 3 resources / 2 prompts，能最快交付**完整**数据（含 FR-05 HistogramStats）；代价是每次请求触发一次失败/空的 Python 调用（可容忍，结构化数据不受影响）。
- 功能：FR-01、FR-02、FR-03、FR-04、FR-05、FR-12、FR-13、FR-14、FR-15、FR-16
- 工程：NFR-01（独立 module，不改主仓）、NFR-02(形态A)、NFR-04、NFR-05、NFR-06
- 交付：可接入 Claude Desktop / WorkBuddy 的 MCP server，能解析真实 bugreport 并答出根因。
- ~~形态 B（CLI shell）~~：原计划降为 P1.5 / 并入 P2；实际实施时直接做成进程内 `analyzer.Analyze` 直调（即下文 Form B），未走 CLI shell 路径。

### P2 · 原生嵌入 `--mcp` ✅ 已完成（Form B）
- **形态 B（进程内 `analyzer.Analyze`/`Compare` 直调）**：`cmd/battery-historian/mcp.go` + `mcp_store.go`（主仓，v0.2.0），9 tools / 6 resources / 3 prompts，是 Form A 功能超集，含 P3-B/C 能力。
- 功能：FR-06 ~ FR-11（细分查询 + Resource）、补全 P1 未覆盖项
- 工程：NFR-01（主仓模块化）、NFR-02(形态B)、NFR-03（protobuf 共存）
- 交付：`cmd/battery-historian --mcp`，同二进制内嵌 MCP 服务，跳过画图直接出结构化结果。
- **import 路径 bug 已修复**，`go build ./...` 通过。

### P3 · 增强（部分完成）
- **P3-B 图表 fallback ✅ 已完成**：`analyzer/chart_v2.go` 用纯 Go `generateV2ChartSVG` 替代 Python 图表，作为 `bugreport://{id}/chart` resource；`generateReportHTML` 生成自包含分析报告页作为 `bugreport://{id}/report` resource。仅适用 Format:2 报告。对应 FR-17/18。
- **P3-C 健康度评分 ✅ 已完成**：`analyzer/health/health.go` 落地 6 维度加权评分，通过 `query_health` tool + `bugreport://{id}/health` resource + `battery_health_report` prompt 暴露。对应 FR-19。
- **P3-A Python 3 迁移 ⏸ 未做**：P3-B 的 SVG fallback 已能满足 Format:2 报告需求；legacy Format:1 报告若需 Historian 风格图表仍需 `--mcp_with_chart` + Python 3 + 已迁移的 `scripts/historian.py`。

---

## 四、依赖与阻塞一览

| 阻塞点 | 关联需求 | 缓解措施 | 状态 |
|---|---|---|---|
| ~~无 go.mod（GOPATH 遗留）~~ | NFR-01 | ~~P1 用独立 module 隔离；P2 再主仓模块化~~ 主仓已加 `go.mod`（module `github.com/reedhoop/ai-battery-historian`，go 1.25.5） | ✅ 已解决 |
| Python 2.7 仅用于画图 | NFR-02 | Form A 走 `parseBugReport` 仍触发 `doHistorian`（Python 缺失时仅该 goroutine 报错，结构化数据正常）；Form B `ParsedData.skipPlot=true` 完全不调 Python；P3-B 用纯 Go SVG fallback | ✅ 已绕开 |
| `golang/protobuf` ↔ `google.golang.org/protobuf` 共存 | NFR-03 | `golang/protobuf v1.3.5` 已是 wrapper；引入 mcp-go v0.56.0 后实测编译通过 | ✅ 已解决 |
| **~~P3-B/P3-C 新增文件 import 路径 bug~~** | NFR-01/02/03 | ~~6 个文件误用 `github.com/google/battery-historian/...` 旧路径~~ 已批量替换为 `github.com/reedhoop/ai-battery-historian/...` | ✅ 已解决 |
| **【Code Review 修复，2026-07-19】P0/P1 安全 + P2/P3 健康度** | NFR-04 + FR-06/07/10/11 + FR-19 | 详见 §六.3 修订记录（14 项优化全部修复） | ✅ 已解决 |
| 解析重 IO/同步、结果大 | FR-14、FR-15 | Form A HTTP 超时 5 分钟 + Top-N 默认返回 + 大数据走 Resource | ✅ 已落实 |
| 低版本报告数据有限 | FR-16 | 显式返回 `criticalError`，不静默空结果 | ✅ 已落实 |

**曾经受 import 路径 bug 影响的 6 个文件**（已全部修复）：
- `analyzer/core.go`（import aggregated / analyzer/health / pb/batterystats_proto / presenter）
- `analyzer/health/health.go`（import aggregated / presenter）
- `analyzer/health/health_test.go`（import aggregated / presenter）
- `cmd/battery-historian/mcp.go`（import aggregated / analyzer / presenter / wakeupreason）
- `cmd/battery-historian/mcp_store.go`（import analyzer）
- `cmd/healthcheck/main.go`（import analyzer）

---

## 五、MCP 能力 → 源码 速查（行号已校准）

| 能力 | 直接复用 |
|---|---|
| `analyze_bugreport` | `analyzer.Analyze` (`analyzer/core.go:84`) |
| `compare_bugreports` | `analyzer.Compare` (`core.go:113`) → 内部调 `presenter.CombineCheckinData` (`presenter.go:421`) |
| `query_system_stats` | `AnalysisResult.Checkin`（`aggregated.Checkin` `aggregated_stats.go:298`） |
| `query_app_stats` | `AnalysisResult.AppStats`（`presenter.AppStat` `presenter.go:251` 内部组装） |
| `query_histogram` | `AnalysisResult.HistogramStats`（`presenter.HistogramStats` `presenter.go:125`） |
| `query_wakelocks` / `wakeup_reasons` / `sync_tasks` | `Checkin.UserspaceWakelocks` / `WakeupReasons` / `SyncTasks` |
| `query_health` (P3-C) | `AnalysisResult.Health`（`health.Evaluate` `analyzer/health/health.go:81`） |
| `bugreport://{id}/chart` (P3-B) | `AnalysisResult.PlotHTML`（`generateV2ChartSVG` `chart_v2.go:119` fallback） |
| `bugreport://{id}/report` (P3-B) | `AnalysisResult.ReportHTML`（`generateReportHTML` `chart_v2.go:301`） |
| 既有 CLI 兜底 | `cmd/checkin-parse`、`cmd/history-parse`、`cmd/checkin-delta`、`cmd/healthcheck`（P3-C） |
| HTTP 代理兜底 | `POST /historian/` → `uploadResponseCompare` JSON（Form A `mcp-server/` 代理） |

---

## 六、修订记录

### 2026-07-17 代码级审计（初稿修订）
针对初稿的四处问题已修订，结论：**问题全部成立，已更正**。
1. **FR-15 / §6 排序错误（事实性）**：原称"复用 `presenter.parseAppStats` 排序做 Top-N"——错误。`parseAppStats`（`presenter.go:251`，未导出）内部按 `byName`（应用名 alphabetical）排序，不能按耗电占比排。已改为「MCP 层按 `AppStat.DevicePowerPrediction` 降序自排序」。
2. **NFR-02 两种形态差异说清**：HTTP 代理形态(形态A)走 `parseBugReport` 必然触发 `doHistorian`→`generateHistorianPlot`（Python 缺失仅该 goroutine 报错，结构化数据正常）；CLI 形态(形态B)完全不调 Python，但只产出原始 `*bspb.BatteryStats` proto，**缺** `aggregated.Checkin`/`[]AppStat`/`HistogramStats`，需 MCP 侧自行组装。
3. **AnalyzeHistory 非纯函数（小）**：`AnalyzeHistory(csvWriter io.Writer, ...)`（`parseutils.go:3094`）向 `io.Writer` 写 CSV 作副作用，P2 抽 Core 时需传 `io.Discard` 或仅取 `AnalysisReport.Summaries`。
4. **设计层补充已落实**：① `analysisStore` 增加 LRU + 上限 N + 无持久化；② `AnalysisResult` 增加未导出 `rawCheckin *bspb.BatteryStats` 供 `Compare` 差分；③ P1 默认形态A；④ Phase 0 增加「mcp-go 能力核对」。

### 2026-07-18 现状校准（实施后回填）
代码已实现 P1/P2/P3-B/P3-C，对需求矩阵做现状校准，发现并修正 5 处偏差：
1. **主需求矩阵表加"实施状态"列**：每行明确 Form A / Form B 落地情况；删除原"技术风险"列（已无意义），用"实施状态"列替代。
2. **新增 FR-17/18/19**：原矩阵未规划 P3-B/C 需求，补 chart resource / report resource / health 查询（tool + resource + prompt 三件套）。
3. **底层复用源码定位修正**：
   - `combineCheckinData` → `CombineCheckinData`（**首字母大写已导出**），行号 419 → 421
   - `analyzer.parseBugReport` (`analyzer.go:659`) → `analyzer.Analyze` (`core.go:84`)（MCP 层不再直接调 `parseBugReport`）
   - `presenter.Data` (:826) 行号 → 828
   - `maxFileSize` (`analyzer.go:59`) → `analyzer.go:60`（59 是注释行）
   - `minSupportedSDK` (`analyzer.go:61`) → `analyzer.go:62`（61 是注释行）
   - `generateHistorianPlot` (`analyzer.go:1023`) → `analyzer.go:1043`
   - `aggregated.Checkin` 补行号 `aggregated_stats.go:298`、`ParseCheckinData` 补行号 441
   - `extractHistogramStats` 补行号 `analyzer.go:609`
   - `AppStat.DevicePowerPrediction` 补行号 `presenter.go:61`
4. **NFR-02 实施路径修正**：原设计"形态B = CLI shell `local_checkin_parse`"未采用，实际 Form B = 进程内 `analyzer.Analyze` 直调（`ParsedData.skipPlot=true`），避免了暴露 `parseAppStats`/`extractHistogramStats` 的重组工作。
5. **§四阻塞点表更新**：~~无 go.mod~~ 已解决；~~protobuf 共存~~ 已解决；新增"P3-B/P3-C 新增文件 import 路径 bug"为当前唯一阻塞项。
6. **§五速查表补全**：补 P3-B/C 能力行 + 实际调用路径（MCP 层调 `analyzer.Analyze`/`Compare`，不直接调 `parseBugReport`）。

### 2026-07-19 Code Review 修复回填
import 路径 bug 已批量修复，并完成一轮 code review 发现的 14 项 P0/P1/P2/P3 优化，对需求矩阵做同步回填：

1. **import 路径 bug 状态**：6 个误用旧路径的文件已全部替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` / `go vet` / `go test ./analyzer/...` 全绿，§二主表与 §四阻塞表中所有"B 待修 import bug"标记已清除。
2. **NFR-04 加固**：原仅"文件大小 + `bugreportutils.Contents` 校验"，本轮 Form B `mcp.go` 增加 (a) base64 **编码长度预检** `maxEncodedLen`（解码前预检，避免 DoS）；(b) `filepath.Clean` + `fi.Mode().IsRegular()` 路径沙箱（拒绝目录/设备/socket/管道）；(c) 三个 prompt handler 加 `wrapUserData` + `promptInjectionGuard` 注入防护。
3. **FR-06/07/10/11 修复（前一轮完成，本轮回填文档）**：
   - FR-10 `extractIDs` 3 段 URI 解析（原 `uid` 取到字面量 `"app_stats"`）。
   - FR-06 `query_wakelocks` 注册 `kind` 参数（`mcp.Enum("userspace", "kernel")`）。
   - FR-07 `wakeupReasonsHandler` 接入 `wakeupreason.FindSubsystem` 真正解码。
   - FR-11 `raw_checkin` 改用 `jsonpb.Marshaler` 输出 JSON（原为 proto text）。
4. **FR-19 `query_health` 评分逻辑加固**：
   - 新增 `isFinite` helper，`Evaluate` / `lerpDown` / `lerpUp` 防护 NaN/Inf 输入。
   - 全维度 `Valid=false` 时返回 `Grade="N/A"` 而非误导性的 `0 分 / F`。
   - `wakelock_burden` / `wakeup_sync_freq` / `app_stability` / `doze_adoption` / `modem_activity` 5 个维度增加"无数据"判定。
   - `buildAlerts` 新增 `alertValue` helper 按维度返回带单位值（`%/h` / `%` / `次/h` / `次`）；排序从单 level 改为 level + score 升序二级排序。
   - `summarize` worst 列表按 score 升序，扣分最严重的维度先出。
5. **【P1 新增】Compare B 报告可通过 `report_index` 访问**：全部 7 个 query_* 工具新增可选 `report_index` 参数（默认 0=A，1=B），`resultForID` / `reportIndexFromReq` helper 校验范围；A/B 对比的 B 报告现在可被任意 query_* 工具访问（原 `resultForID` 恒返回 `Results[0]`，B 报告无法查）。
6. **【P1 修复】`UsingComparison` 判定**：`Compare` 增加 `&& results[0].IsDiff` 校验，避免两份独立报告被误判为 delta。
7. **【P1 修复】report/chart 图表一致性**：`generateReportHTML` 签名从 `(r, contents string)` 改为 `(r, plotHTML string)`，直接复用 `r.PlotHTML`，避免重新解析 V2 bugreport 导致 `/report` 与 `/chart` 图表不一致。

> 对应设计侧修订见 `MCP概要设计.md` 第十二节。
