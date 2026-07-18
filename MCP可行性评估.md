# Battery Historian 增加 AI / MCP 能力 —— 可行性评估

> 评估对象：`d:\02_project_my\03_small_app\ai-battery-historian`（Google Battery Historian v2 的 fork，Go + JS 前端）
> 评估目标：判断「让 AI 助手（如 Claude / WorkBuddy / Cursor）通过 MCP 协议直接分析 Android bugreport 耗电」是否可行，并给出落地方案与风险。
> 评估方式：直接阅读源码与构建系统（非运行），结论基于代码事实。

> **实施状态（2026-07-19 校准）**：可行性已得到代码验证。P1（Form A 独立 module + HTTP 代理）、P2（Form B 原生 `--mcp` 嵌入）、P3-B（chart/report HTML）、P3-C（健康度评分）均已落地。本评估文档保留为「设计阶段归档 + 现状对照」用途，最新事实以本文「实施状态」小节与 `MCP概要设计.md` 为准。**import 路径 bug 已修复**：6 个误用 `github.com/google/battery-historian/...` 旧路径的文件已批量替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` / `go vet ./analyzer/... ./cmd/battery-historian/...` / `go test ./analyzer/...` 全部通过。同时完成 code review 发现的 P0/P1 安全加固（base64 预检 / 路径沙箱 / prompt 注入防护 / Compare `report_index`）与 P2/P3 健康度优化（alert 单位 / 二级排序 / 全无效 N/A），详见 §六.6 与 §十二。

---

## 一、一句话结论

**已验证可行，且收益已部分兑现。** Battery Historian 的核心解析管线（checkin 解析、history 汇总、app 级统计、A/B 对比）全部是**纯 Go**，与前端 JS 完全解耦，已通过两条路径封装为 MCP 工具：(1) Form A 独立 module（`mcp-server/`）走 HTTP 代理；(2) Form B 原生嵌入（`cmd/battery-historian --mcp`）进程内直调 `analyzer.Analyze/Compare`。P3-B 已用纯 Go `generateV2ChartSVG` 实现 Format:2 报告的电量曲线图（fallback 自包含 SVG），P3-C 已用 `analyzer/health` 包实现 6 维度健康度评分。import 路径 bug 已修复，`go build ./...` / `go vet` / `go test ./analyzer/...` 全部通过；并完成 code review 发现的 P0/P1 安全加固（base64 预检 / 路径沙箱 / prompt 注入防护 / Compare `report_index`）与 P2/P3 健康度优化（alert 单位 / 二级排序 / 全无效 N/A）。

---

## 二、项目现状关键信息（基于代码探查）

| 维度 | 现状 | 对 MCP 的影响 |
|---|---|---|
| 解析管线 | `analyzer.parseBugReport` → 并行调用 `checkinparse` / `parseutils` / `activity` / `broadcasts` / `dmesg` / `presenter` → 产出 `uploadResponse`（JSON） | 核心数据已是结构化 JSON，**可直接喂给 LLM** |
| 纯 Go 解析 | `checkinparse.ParseBatteryStats`、`parseutils.AnalyzeHistory`、`aggregated.ParseCheckinData`、`presenter.Data` 均为纯 Go | 不依赖浏览器，适合命令行/服务化 |
| 既有 CLI 入口 | `cmd/checkin-parse`、`cmd/history-parse`、`cmd/checkin-delta` 已是独立可执行分析工具 | 证明「脱离 Web 也能跑分析」，可直接复用 |
| 数据产物 | `aggregated.Checkin`（系统级 100+ 指标）、`[]presenter.AppStat`（按应用耗电）、`HistogramStats`、`CombinedCheckinSummary`（A/B diff） | 这些是 MCP 最该暴露的「金矿」数据 |
| 构建系统 | **已加 `go.mod`**（module `github.com/reedhoop/ai-battery-historian`，go 1.25.5），全仓 import 路径已统一（含 P3-B/C 新增的 6 个文件），`go build ./...` / `go vet` / `go test ./analyzer/...` 全部通过 | 主体已模块化，无遗留问题 |
| Python 依赖 | `scripts/historian.py`、`scripts/kernel_trace.py` 原为 **Python 2.7**，**仅用于生成 Historian v2 时间轴图表 HTML** | **核心耗电统计不依赖它**；Form B 走 `analyzer.Analyze`（`skipPlot=true`）彻底不调 Python；P3-B 通过 `generateV2ChartSVG`（纯 Go，仅适用 Format:2）提供 fallback 图表 |
| 现有 HTTP 接口 | `POST /historian/`（multipart 上传）→ 返回 `uploadResponseCompare` JSON（含 AppStats、BatteryStats、HistogramStats） | 已被 Form A（`mcp-server/`）代理复用 |

### 关键数据结构（MCP 将直接返回这些）
- `aggregated.Checkin`：设备型号、实际/估算放电 mAh、亮/灭屏放电率、各 wakelock/同步/唤醒原因/CPU/GPS/相机/手电筒的时长与占比、Wi-Fi/蓝牙/Modem 传输与放电。
- `presenter.AppStat`：每个应用的 `RawStats`（`bspb.BatteryStats_App`，含 wakeups、cpu、sensor、power use mah）、设备耗电占比预测、CPU 耗电预测。
- `HistogramStats`：屏幕/信号/移动网络/蓝牙/Modem 等各项百分比与每小时速率，正好适合 AI 做「健康度评分」。
- `CombinedCheckinSummary`：两个 bugreport 的逐项差值（A/B 对比），AI 可直接解读「哪类耗电恶化了」。

---

## 三、为什么值得做（价值）

Battery Historian 当前是**静态可视化工具**：人要在几十张表里自己找异常。接入 MCP 后，AI 能：
1. **自然语言查询**：「这台手机待机耗电快，最可能的原因是什么？」→ AI 调用 `query_system_stats` + `query_app_stats` 后给出根因。
2. **跨指标关联推理**：把 wakelock 时长、CPU 占用、同步频率、信号扫描时间综合判断，而不是孤立看表。
3. **A/B 报告自动化**：diff 两个 bugreport，自动生成「版本升级后耗电变化」的结论文本。
4. **CI / 自动化**：把分析接进研发流水线，每次提测自动出耗电评估。

---

## 四、三种集成方案对比

| 方案 | 做法 | 改动量 | 依赖冲突风险 | 实施状态 |
|---|---|---|---|---|
| **A. 独立 MCP 进程包裹现有 HTTP/CLI** | 新起一个 Go（独立 module）或 Python/TS 的 MCP server，调用已运行的 Historian HTTP 接口（`POST /historian/`）或 shell 现有 CLI 工具，把 JSON 整理成 MCP 工具 | **极小（≈0 行改 legacy 代码）** | 低（隔离在独立进程/模块） | **已实施**：`mcp-server/` 独立 module（`github.com/google/battery-historian/mcp-server`，v0.1.0），5 tools / 3 resources / 2 prompts，HTTP 代理运行中的 Historian 服务 |
| **B. 原生嵌入（同二进制 `--mcp`）** | 在 `cmd/battery-historian` 增加 `--mcp` 标志，嵌入 `mark3labs/mcp-go`，新增 `analyzer.Analyze(contents)(*AnalysisResults,error)` 纯函数跳过画图，直接返回结构化结果 | 中（重构 + 加 go.mod + 依赖） | **中**（老 `golang/protobuf` 与新 `google.golang.org/protobuf` 共存，实测编译通过；P3-B/C 新增文件 import 路径已修复） | **已实施**：`cmd/battery-historian/mcp.go`（v0.2.0，9 tools / 6 resources / 3 prompts），是 Form A 的功能超集，含 P3-B/C 能力；`go build ./...` 通过 |
| **C. 纯前端/插件式** | 给 JS 前端加 AI 面板 | 高且与 MCP 协议无关 | — | 不推荐（偏离 MCP 目标），未实施 |

> 实施结论：A、B 两条路径都已落地，B 是 A 的功能超集。建议日常使用 Form B（`--mcp`），Form A 作为「无主仓改动的隔离部署」备用方案保留。

---

## 五、已暴露的 MCP 能力（映射到真实代码）

> 实施后实际暴露的能力（Form B / `cmd/battery-historian --mcp`，v0.2.0）。Form A（`mcp-server/`，v0.1.0）仅含前 5 个 tool + 3 个 resource + 2 个 prompt，是 Form B 子集。

### Tools（动作）
| MCP Tool | 底层复用 | 实施 |
|---|---|---|
| `analyze_bugreport(path or bytes)` | `analyzer.Analyze` → `AnalysisResults`（不画图） | ✅ Form A + B |
| `compare_bugreports(a, b)` | `analyzer.Compare` → `*CompareResult`（含 `presenter.CombineCheckinData`） | ✅ Form A + B |
| `query_system_stats(id)` | `AnalysisResult.Checkin`（`aggregated.Checkin`） | ✅ Form A + B |
| `query_app_stats(id, uid?, topN?)` | `AnalysisResult.AppStats`（Top-N 自排序） | ✅ Form A + B |
| `query_histogram(id)` | `AnalysisResult.HistogramStats` | ✅ Form A + B |
| `query_wakelocks(id, kind?)` | `AnalysisResult.Checkin.UserspaceWakelocks/KernelWakelocks` | ✅ Form B only |
| `query_wakeup_reasons(id)` | `AnalysisResult.Checkin.WakeupReasons` + `wakeupreason` 解码 | ✅ Form B only |
| `query_sync_tasks(id)` | `AnalysisResult.Checkin.SyncTasks` | ✅ Form B only |
| `query_health(id)` | `AnalysisResult.Health`（`*health.Report`，P3-C） | ✅ Form B only |

### Resources（上下文）
- `bugreport://{id}/system_stats` → Checkin JSON（Form A + B）
- `bugreport://{id}/app_stats/{uid}` → 单应用 JSON（Form A + B）
- `bugreport://{id}/raw_checkin` → proto 转 JSON（Form A + B）
- `bugreport://{id}/chart` → `AnalysisResult.PlotHTML`（P3-B，Form B only）
- `bugreport://{id}/report` → `AnalysisResult.ReportHTML`（P3-B，Form B only）
- `bugreport://{id}/health` → `AnalysisResult.Health` JSON（P3-C，Form B only）

### Prompts（给 AI 的提示词模板）
- `battery_root_cause`：给定系统/应用指标，输出根因分析框架。（Form A + B）
- `battery_ab_report`：给定 A/B diff，输出升级前后耗电变化结论。（Form A + B）
- `battery_health_report`：给定健康度报告，输出解读与改进建议。（P3-C，Form B only）

---

## 六、关键技术风险与阻塞点

1. **~~无 go.mod / GOPATH 遗留~~ → 已解决**
   主仓已加 `go.mod`（module `github.com/reedhoop/ai-battery-historian`，go 1.25.5，require `golang/protobuf v1.3.5` + `mark3labs/mcp-go v0.56.0`），全仓 import 路径已从 `google/battery-historian` 替换为 `reedhoop/ai-battery-historian`。`golang/protobuf` 与 `mcp-go` 间接引入的 `google.golang.org/protobuf` 共存经实测编译通过（详见 NFR-03）。

2. **Python 2.7 依赖（仅画图，已通过两条路径绕开）**
   `historian.py` 生成时间轴图表 HTML，**核心统计不依赖它**。两条 MCP 路径均已绕开：
   - **Form A（HTTP 代理 `POST /historian/`）**：走官方 `parseBugReport`，其内 `go doHistorian(...)` **必然**触发 `generateHistorianPlot`；Python 缺失时仅该 goroutine 报错，结构化数据（含 HistogramStats）正常返回。
   - **Form B（原生 `--mcp`）**：`analyzer.Analyze` 内部用 `ParsedData.skipPlot=true`（core.go:85）彻底跳过 `generateHistorianPlot`，全程纯 Go。
   - **P3-B 已实现 fallback 图表**：`analyzer/chart_v2.go:119 generateV2ChartSVG` 从 bugreport 文本中解析 Format:2 历史，生成自包含 inline-SVG 电量曲线图（无 Python 依赖，仅适用 Format:2 报告）；由 `core.go:187 postProcess` 在 `PlotHTML == ""` 时自动填充。

3. **解析是重 IO/CPU 且同步**
   现有管线写临时文件、并行 spawn 子解析，单报告可能耗时数秒~数十秒。MCP 工具已**设置较长超时**（Form A 的 `HistorianClient.HTTP.Timeout = 5 * time.Minute`），并优先返回「聚合摘要」而非原始 proto，必要时分页/按需查询。

4. **结果体积大**
   bugreport 可达 10–100MB，AppStats 列表可能很长。MCP 工具已默认返回 Top-N（按 `AppStat.DevicePowerPrediction` 降序自排序），原始数据走 Resource 按需拉取。

5. **SDK 版本约束**
   `minSupportedSDK = 21`（`analyzer.go:62`），低于 Android 5.0 的报告会走「unsupported」分支，返回数据有限——MCP 已明确返回 `criticalError` 字段而非静默空结果。

6. **~~P3-B/P3-C 新增文件 import 路径 bug~~ → 已解决**
   P3-B/P3-C 阶段新增的 6 个 Go 文件曾误用 `github.com/google/battery-historian/...` 旧 import 路径，与 `go.mod` 声明的 `github.com/reedhoop/ai-battery-historian` 模块路径冲突，且 `go.mod` 无 replace 兜底。Go 工具链会去公网拉取上游 `github.com/google/battery-historian`，但上游缺 `analyzer/health` 子包、缺 `AnalyzeWithChart` 函数、缺 `AnalysisResult.PlotHTML/ReportHTML/Health` 字段，导致符号解析失败。
   **受影响文件**（已修复）：
   - `analyzer/core.go`（import aggregated / analyzer/health / pb/batterystats_proto / presenter）
   - `analyzer/health/health.go`（import aggregated / presenter）
   - `analyzer/health/health_test.go`（import aggregated / presenter）
   - `cmd/battery-historian/mcp.go`（import aggregated / analyzer / presenter / wakeupreason）
   - `cmd/battery-historian/mcp_store.go`（import analyzer）
   - `cmd/healthcheck/main.go`（import analyzer）
   **已修复**：把这 6 个文件中所有 `github.com/google/battery-historian/...` 批量替换为 `github.com/reedhoop/ai-battery-historian/...`（与仓内其它文件一致）。`go build ./...` 通过。

7. **【Code Review 修复，2026-07-19】P0/P1 安全加固 + P2/P3 健康度优化**
   import 路径 bug 修复后做了一轮 code review，发现并修复以下问题：
   - **FR-10 `extractIDs` P0 bug**（`mcp.go`）：3 段 URI `bugreport://{id}/app_stats/{uid}` 解析错误，`uid` 取到字面量 `"app_stats"`。已改为按 `parts[1] == "app_stats"` 判定取 `parts[2]`。
   - **FR-06 `kind` 参数缺失**（`mcp.go`）：`query_wakelocks` 未注册 `kind` 参数。已补 `mcp.WithString("kind", mcp.Enum("userspace", "kernel"))`，`wakelocksHandler` 按 `kind` 过滤。
   - **FR-07 未真正解码**（`mcp.go`）：`wakeupReasonsHandler` 未调 `FindSubsystem`。已接入 `wakeupreason.FindSubsystem`，每条 reason 返回 `subsystem` / `matched` / `decodeError` 字段。
   - **FR-11 格式偏差**（`mcp.go`）：`raw_checkin` 用 proto text 而非 JSON。已改用 `jsonpb.Marshaler` 输出 JSON。
   - **`UsingComparison` 判定不严**（`core.go`）：`Compare` 仅看 `len(results)==1`，未校验 `IsDiff`。已加 `&& results[0].IsDiff`。
   - **report/chart 图表不一致**（`core.go` / `chart_v2.go`）：`generateReportHTML` 重新解析 V2 而非复用 `r.PlotHTML`。已把签名改为 `(r, plotHTML string)` 直接嵌入已解析 PlotHTML。
   - **Health 全无效得 0 分**（`health.go`）：所有维度 `Valid=false` 时返回 `Score=0 / Grade="F"`，误导用户。已改为返回 `Grade="N/A"` + 说明性 Summary。
   - **NaN/Inf 穿透**（`health.go`）：`lerpDown`/`lerpUp`/`Evaluate` 未防护 NaN/Inf 输入。已加 `isFinite` helper（`!math.IsNaN && !math.IsInf`）多重防护。
   - **各维度无数据判定缺失**（`health.go`）：5 个维度在数据缺失时仍按 0 分参与加权。已为 `wakelock_burden` / `wakeup_sync_freq` / `app_stability` / `doze_adoption` / `modem_activity` 增加 `Valid=false` 分支。
   - **【P1】base64 DoS 防护**（`mcp.go loadInput`）：原代码先解码再校验大小，超大 base64 输入会拖慢服务。已加 `maxEncodedLen` 预检（`maxFileSize*4/3 + 4`），编码长度超限直接拒绝。
   - **【P0】路径沙箱**（`mcp.go loadInput`）：原 `os.Stat` 仅查大小，未拒绝非常规文件。已加 `filepath.Clean` 规范化 + `fi.Mode().IsRegular()` 守卫，拒绝目录 / 设备 / socket / 管道。
   - **【P1】prompt 注入防护**（`mcp.go` 三个 prompt handler）：原代码将用户参数直接拼到 prompt 文本，无注入防护。已加 `promptInjectionGuard`（在 `<user_data>` 标签内标注为不可信 DATA）+ `wrapUserData` 包装。
   - **【P1】Compare 第二份报告可通过 `report_index` 访问**（`mcp.go resultForID` + 所有 query_* 工具）：原 `resultForID` 恒返回 `Results[0]`，A/B 对比的 B 报告无法查。已加 `report_index` 参数到全部 7 个 query_* 工具，`reportIndexFromReq` helper 校验范围。
   - **【P2】alert 单位**（`health.go buildAlerts`）：原 `Value` 字段为 `fmt.Sprintf("%.1f", ...)` 无单位。已加 `alertValue` helper，按维度返回 `%/h` / `%` / `次/h` / `次`。
   - **【P2】alert 二级排序**（`health.go buildAlerts`）：原仅按 level 排序，同 level 内无序。已加 score 升序二级排序，同 severity 内更差者先出。
   - **【P3】summarize worst 排序**（`health.go summarize`）：原 worst 列表按维度声明顺序输出。已改为按 score 升序，扣分最严重的维度先出。
   - **验证**：`go build ./...` ✅；`go vet ./analyzer/... ./cmd/battery-historian/...` ✅；`go test ./analyzer/...` ✅（`analyzer` + `analyzer/health` 全绿）。

---

## 七、实施路线（分阶段，附完成状态）

**Phase 0 — 准备 ✅ 已完成**
- ~~确认运行环境~~：实际 go.mod 声明 go 1.25.5（非评估时的 1.21）。
- ~~在 `go.mod` 中声明 module `github.com/google/battery-historian`~~：实际 module 名为 `github.com/reedhoop/ai-battery-historian`，全仓 import 路径已替换。
- **mcp-go 能力核对 ✅**：`mark3labs/mcp-go v0.56.0` 支持 Tool / Resource / Prompt 三类原语 + stdio + streamable HTTP 双传输，已用于 Form A 与 Form B。

**Phase 1 — 独立 MCP 包裹 Demo ✅ 已完成（Form A）**
- 新建独立 Go module `mcp-server/`（module `github.com/google/battery-historian/mcp-server`，v0.1.0）。
- 复用方式采用 (a)：启动官方 Historian HTTP 服务，`POST /historian/` 拿 JSON，整理成 MCP 工具。
- 暴露 5 个 tool（`analyze_bugreport` / `compare_bugreports` / `query_system_stats` / `query_app_stats` / `query_histogram`）+ 3 个 resource + 2 个 prompt。
- 含独立 LRU Store（默认容量 20，无 TTL）。

**Phase 2 — 原生嵌入 ✅ 已完成（Form B）**
- 重构：新增 `analyzer.Analyze(contents string) (AnalysisResults, error)`（`analyzer/core.go:84`）、`AnalyzeWithChart`（core.go:98）、`Compare(contentsA, contentsB string) (*CompareResult, error)`（core.go:113），不调用 `historian.py`，复用 `parseBugReport` 内部逻辑直接返回结构化结果。
- 引入 `mark3labs/mcp-go`，在 `cmd/battery-historian` 加 `--mcp` 标志启动 stdio/streamable HTTP MCP server（`mcp.go:750 startMCPServer`）。
- 处理 `golang/protobuf` ↔ `google.golang.org/protobuf` 共存 ✅ 编译通过。
- 补全 9 个 tool + 6 个 resource + 3 个 prompt + Top-N 分页。
- **import 路径 bug 已修复**（详见 §六.6），`go build ./...` 通过。

**Phase 3 — 增强 ✅ 部分完成**
- **P3-B（已完成）**：用纯 Go `generateV2ChartSVG`（`analyzer/chart_v2.go:119`）+ `generateReportHTML`（`chart_v2.go:301`）替代 Python 图表，作为 `bugreport://{id}/chart` 与 `bugreport://{id}/report` resource。仅适用 Format:2 报告；legacy Format:1 报告若需图表仍需 `--mcp_with_chart`（启 Python 3 + 已迁移的 `scripts/historian.py`）。
- **P3-C（已完成）**：`analyzer/health/health.go:81 Evaluate` 实现 6 维度加权健康度评分（standby_drain 0.30 / wakelock_burden 0.20 / wakeup_sync_freq 0.15 / app_stability 0.15 / doze_adoption 0.10 / modem_activity 0.10），通过 `query_health` tool + `bugreport://{id}/health` resource + `battery_health_report` prompt 暴露。
- ~~Python 3 迁移~~：未在 P3 范围内完成；P3-B 的 SVG fallback 已能满足 Format:2 报告需求。

---

## 八、工作量估算（实际 vs 评估）

| 阶段 | 评估工作量 | 实施状态 | 备注 |
|---|---|---|---|
| Phase 1 独立包裹 Demo | 1~2 人天 | ✅ 已完成 | Form A `mcp-server/` |
| Phase 2 原生嵌入 | 3~5 人天 | ✅ 已完成 | Form B `cmd/battery-historian --mcp`；import 路径 bug 已修 |
| Phase 3-B 图表 fallback | 未评估 | ✅ 已完成 | `chart_v2.go` 纯 Go SVG |
| Phase 3-C 健康度评分 | 未评估 | ✅ 已完成 | `analyzer/health` 6 维度加权 |
| Phase 3-A Python 3 迁移 | 2~3 人天 | ⏸ 未做 | P3-B 已绕开需求 |
| Code Review 修复 | 未评估 | ✅ 已完成（2026-07-19） | P0/P1 安全加固 + P2/P3 健康度优化，详见 §六.7 |

**总评**：技术可行性已得到代码验证；A/B 两条 MCP 路径都已落地，P3-B/C 增值能力也已实现，import 路径 bug 已修复，code review 发现的 14 项 P0/P1/P2/P3 问题全部修复，`go build ./...` / `go vet` / `go test ./analyzer/...` 全绿。

---

## 九、附录：现有可复用入口（源码定位，行号已校准）

- 解析主流程：`analyzer/analyzer.go` → `parseBugReport`（第 669 行，方法 `func (pd *ParsedData) parseBugReport(...)`）、`AnalyzeFiles`（第 564 行）、`AnalyzeAndResponse`（第 553 行）。
- 聚合数据成型：`presenter/presenter.go` → 包级函数 `Data`（第 828 行）、`parseAppStats`（第 251 行，未导出）、`CombineCheckinData`（第 421 行，**注意首字母大写已导出**）。
- 系统级指标：`aggregated/aggregated_stats.go` → `type Checkin struct`（第 298 行）、`ParseCheckinData`（第 441 行）。
- 纯 Go 解析函数：`checkinparse/checkin_parse.go:483 ParseBatteryStats`、`parseutils/parseutils.go:3094 AnalyzeHistory`、`checkindelta/checkin_delta.go:70 ComputeDeltaFromSameDevice`、`packageutils/package_extractor.go:237 ExtractAppsFromBugReport`。
- 既有 CLI：`cmd/checkin-parse`、`cmd/history-parse`、`cmd/checkin-delta`、`cmd/healthcheck`（P3-C 健康度 CLI）。
- HTTP 接口：复用 `POST /historian/`（multipart → `uploadResponseCompare` JSON），Form A `mcp-server/` 代理。
- **新增（P2）Analysis Core**：`analyzer/core.go:84 Analyze`、`core.go:98 AnalyzeWithChart`、`core.go:113 Compare`、`core.go:33 AnalysisResult`、`core.go:71 AnalysisResults`、`core.go:74 CompareResult`。
- **新增（P3-B）图表 fallback**：`analyzer/chart_v2.go:119 generateV2ChartSVG`、`chart_v2.go:301 generateReportHTML`。
- **新增（P3-C）健康度评分**：`analyzer/health/health.go:81 Evaluate`、`health.go:59 Report`、6 维度函数 `health.go:160/189/206/223/240/257`。
- **新增（P2）MCP server**：`cmd/battery-historian/mcp.go`（Form B，9 tools/6 resources/3 prompts）、`cmd/battery-historian/mcp_store.go`（LRU Store）、`mcp-server/`（Form A 独立 module）。
- 关键常量：`analyzer/analyzer.go:60 maxFileSize = 100MB`、`analyzer/analyzer.go:62 minSupportedSDK = 21`、`analyzer/analyzer.go:1043 generateHistorianPlot`。
