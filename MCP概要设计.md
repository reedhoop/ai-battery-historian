# Battery Historian · AI/MCP 能力概要设计

> 配套文档：`MCP可行性评估.md`（可行性）、`MCP需求矩阵.md`（需求）
> 设计目标：在**最小侵入**现有代码的前提下，让 AI 助手通过 MCP 协议解析 Android bugreport、做耗电根因分析与 A/B 对比。
> 设计主线：**抽离 Analysis Core（纯 Go 解析核心）→ 传输无关 → MCP Server 复用 Core**。

> **实施状态（2026-07-19 校准）**：设计已全部落地。Analysis Core 实现于 `analyzer/core.go`；MCP Server 有两套并行实现：Form A（`mcp-server/` 独立 module，HTTP 代理）与 Form B（`cmd/battery-historian --mcp`，进程内直调 Core）。P3-B（chart/report HTML via `chart_v2.go`）与 P3-C（健康度评分 via `analyzer/health`）也已实现。本文档原"设计建议"语气已改为"已实施描述"，§3.1 契约对齐 `core.go` 实际签名，§5 能力清单对齐实际注册的 tool/resource/prompt。**import 路径 bug 已修复**：6 个误用 `github.com/google/battery-historian/...` 旧路径的文件已批量替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` / `go vet` / `go test ./analyzer/...` 全部通过；同步完成 code review 发现的 P0/P1 安全加固 + P2/P3 健康度优化（详见 §12 修订记录）。

---

## 1. 设计目标与原则

### 1.1 目标
- 对外提供一组 MCP Tool / Resource / Prompt，使 AI 能直接消费 Battery Historian 的解析能力。**已实现**：Form A 5/3/2，Form B 9/6/3。
- P1（独立部署、零改动 legacy）与 P2（原生嵌入同二进制）**均已交付**。

### 1.2 设计原则
| 原则 | 说明 | 落实情况 |
|---|---|---|
| 复用优先 | 直接复用 `checkinparse` / `parseutils` / `activity` / `aggregated` / `presenter` 等纯 Go 库，不重写解析逻辑 | ✅ `core.go` 仅做 facade，未改解析库 |
| 传输无关 | 解析核心（Analysis Core）与传输层（HTTP / MCP）解耦；同一 Core 可被两种协议调用 | ✅ Form B 进程内直调 Core，Form A 走 HTTP |
| 最小侵入 | 不修改现有解析库函数语义；仅做"提取/封装"，必要时新建 facade | ✅ 仅新增 `core.go` / `chart_v2.go` / `health/`，未改 `parseBugReport` |
| 渐进交付 | P1 独立进程包裹现有 HTTP；P2 主仓模块化与原生嵌入 | ✅ 两阶段都已交付 |
| 跳过画图 | MCP 路径不调用 `historian.py`，仅取结构化数据，彻底规避 Python 2.7 | ✅ Form B 走 `skipPlot=true`；P3-B 用纯 Go SVG 替代 |

---

## 2. 总体架构

```
┌──────────────────────────────────────────────────────────┐
│                      AI Client                             │
│          (Claude Desktop / WorkBuddy / Cursor)             │
└───────────────────────────┬──────────────────────────────┘
                            │  MCP (stdio 本地 / SSE 远程)
┌───────────────────────────▼──────────────────────────────┐
│                   MCP Server Layer                         │
│  • Tool 路由: analyze/compare/query_system/app/histogram… │
│  • Resource 路由: bugreport://{id}/system_stats | app…    │
│  • Prompt 模板: battery_root_cause / battery_ab_report     │
│  • 格式化: Top-N 排序 / 分页 / proto→json                 │
│  • 会话缓存: analysisStore (id → *AnalysisResult)         │
└───────────────────────────┬──────────────────────────────┘
                            │  调用
┌───────────────────────────▼──────────────────────────────┐
│              Analysis Core (新建 facade)                   │
│  Analyze(contents []byte) → *AnalysisResult              │
│  Compare(a,b []byte)        → *CombinedCheckinSummary     │
│  （纯 Go；跳过 generateHistorianPlot）                     │
└───────────────────────────┬──────────────────────────────┘
                            │  复用（不改逻辑）
┌───────────────────────────▼──────────────────────────────┐
│              现有解析库（保持原样）                         │
│  analyzer · checkinparse · parseutils · activity ·         │
│  broadcasts · dmesg · aggregated · presenter · pb(proto)   │
└──────────────────────────────────────────────────────────┘
```

### 2.1 两种部署形态（共用同一 Analysis Core 契约）
- **P1 独立进程**：新建独立 module 的 MCP server，通过「HTTP 代理 `POST /historian/`」或「shell 现有 CLI 工具」拿到结构化 JSON，再由 Analysis Core 契约统一收敛。legacy 代码**零改动**。
- **P2 同二进制**：`cmd/battery-historian` 增加 `--mcp` 标志，进程内直接调用 Analysis Core（不经 HTTP），性能更好、无 Python 依赖。

---

## 3. 模块划分

### 3.1 Analysis Core（已实现于 `analyzer/core.go`）
职责：把"原始 bugreport 字节"转换为"AI 友好的结构化结果"，**不依赖 Web、不画图**。

实际对外契约（`analyzer/core.go`）：
```go
// Analyze 解析单个 bug report，返回结构化结果（不生成 Historian HTML plot，无 Python 依赖）。
// contents 为 bugreport 全文（string，非 []byte）。
func Analyze(contents string) (AnalysisResults, error)               // core.go:84

// AnalyzeWithChart 行为同 Analyze，但同时生成 Historian plot HTML（需 Python 3 + 已迁移的
// scripts/historian.py）。PlotHTML 可作为 MCP chart resource（P3-B）。
func AnalyzeWithChart(contents string) (AnalysisResults, error)      // core.go:98

// Compare 解析两份 bug report。若同 Android ID 且 batterystats 起始时钟相同，
// 返回 delta（UsingComparison=true, len(Reports)==1, IsDiff=true）；
// 否则作为两份独立报告返回（len(Reports)==2）。
func Compare(contentsA, contentsB string) (*CompareResult, error)    // core.go:113

// AnalysisResults 是单次分析或对比的逐报告结果集。
// 单 bugreport：恰好 1 个条目；对比：1 个（delta）或 2 个（独立）。
type AnalysisResults []*AnalysisResult                               // core.go:71

type AnalysisResult struct {                                         // core.go:33
    Checkin        aggregated.Checkin         // 系统级聚合指标
    AppStats       []presenter.AppStat        // 应用级耗电（已 Top-N 排序在 MCP 层做）
    HistogramStats presenter.HistogramStats   // 健康度直方图
    RawCheckin     *bspb.BatteryStats         // 原始 proto，供 Compare 差分复用（已导出）

    IsDiff      bool
    SDKVersion  int
    DeviceModel string
    FileName    string

    CriticalError string  // 沿用 uploadResponse.CriticalError（FR-16）
    Note          string
    Error         string
    Warning       string

    PlotHTML   string       // P3-B：Historian plot HTML（AnalyzeWithChart 或 V2 SVG fallback 填充）
    ReportHTML string       // P3-B：自包含分析报告 HTML（健康度卡片 + 图表 + 关键聚合）
    Health     *health.Report // P3-C：健康度评分（CriticalError 为空时填充）
}

type CompareResult struct {                                          // core.go:74
    Reports         AnalysisResults                 // 逐报告结果
    Combined        presenter.CombinedCheckinSummary // A/B 合并视图
    UsingComparison bool                            // 是否走 delta 路径
}
```

**与原设计契约的差异**（实施时调整）：
1. 入参 `[]byte` → `string`（与 `parseBugReport` 内部 `contents string` 对齐）。
2. **无 `Options` 类型**：`SkipPlot` 改用 `ParsedData.skipPlot` 私有字段（`Analyze` 恒 true，`AnalyzeWithChart` 恒 false + `drawPlot=true`）；`ScrubPII` 走 `parseBugReport` 既有参数。
3. `Analyze` 返回 `AnalysisResults`（复数切片）而非 `*AnalysisResult`，以兼容 compare 场景的两份独立报告。
4. `Compare` 返回 `*CompareResult`（包装 `CombinedCheckinSummary` + `Reports`）而非裸 `*CombinedCheckinSummary`。
5. 字段命名：`SystemStats` → `Checkin`、`Histogram` → `HistogramStats`、`rawCheckin`（未导出）→ `RawCheckin`（已导出）。
6. 新增 P3-B 字段 `PlotHTML` / `ReportHTML` 与 P3-C 字段 `Health`，原设计未规划。
7. **无 `ID` 字段**：会话 id 由 MCP 层（`Store.Put` 生成）维护，不进 Core。

实现要点（已落实）：
- `Analyze` 内部 `pd := &ParsedData{skipPlot: true}` + `pd.parseBugReport("bugreport.txt", contents, "", "")`（core.go:85-87），跳过 `generateHistorianPlot`。
- `parseBugReport` 的并行解析逻辑、`presenter.Data` 调用、`aggregated.ParseCheckinData` 都未改动，Core 仅做组装。
- **`AnalyzeHistory` 副作用已规避**：`parseutils.AnalyzeHistory(csvWriter io.Writer, ...)`（`parseutils.go:3094`）会向 `io.Writer` 写 CSV；`parseBugReport` 内部已传 `io.Discard` 吞掉 CSV、仅取 `AnalysisReport.Summaries`，Core facade 无需额外处理。
- `postProcess`（core.go:174）在 PlotHTML 为空时调 `generateV2ChartSVG` 填充 P3-B fallback，并始终调 `generateReportHTML` 填充 P3-B 报告页。
- `analysisResults`（core.go:131）从 `pd.data` / `pd.responseArr` 组装 `AnalysisResult`；`CriticalError` 为空时调 `health.Evaluate` 填充 P3-C 评分。
- **P1 形态 A（HTTP 代理）不走 Core**：它复用官方 `POST /historian/` 处理器，`parseBugReport` 仍会启动 `doHistorian`→`generateHistorianPlot`；Python 缺失时仅该 goroutine 报错，结构化数据（含 HistogramStats）正常返回。详见 §3.3。

### 3.2 MCP Server 层（已实现两套）
职责：协议适配 + 工具编排 + 结果呈现。
- **Server 启动**：stdio（本地默认）/ streamable HTTP（远程可选，Form B `--mcp_transport=http`，Form A `--transport=http`），满足 NFR-05。Form A 用 `mcp-go` v0.56.0 `server.NewMCPServer`（main.go:36）；Form B 同（mcp.go:750 `startMCPServer`）。
- **会话缓存 `Store`**：两套独立实现，结构相同（容量驱动 LRU + 无持久化），存储类型不同：
  - Form A `mcp-server/store.go`：存 `*UploadResponseCompare`（JSON RawMessage 镜像）
  - Form B `cmd/battery-historian/mcp_store.go`：存 `*storedItem{Results analyzer.AnalysisResults; Compare *analyzer.CompareResult}`（强类型）
  - **生命周期**：默认容量 20（Form B `--mcp_max_entries`，Form A `--max-entries`），LRU 驱逐最久未用，无 TTL，进程重启清空。
- **格式化**：列表类工具默认 `Top-N`（**MCP 层按 `AppStat.DevicePowerPrediction` 降序自排序**——`presenter.parseAppStats`（`presenter.go:251`，未导出）按 `byName` 应用名 alphabetical 排，不能复用），超大结果走 Resource 按需拉取（FR-15）。
- **错误透传**：解析异常以结构化错误返回，内容来自 `CriticalError` / `Note`（NFR-06）。

### 3.3 适配层（两套并行实现，已交付）

> 实施结论：原设计把"CLI shell 形态 B"作为 P1.5/P2 备选，实际实施时直接做成了原生嵌入（Form B），不再走 CLI shell 路径。两套形态是平行的替代方案，Form B 是 Form A 的功能超集。

| 形态 | 实现位置 | 是否调 Python | 能拿到的结构化数据 | Tools/Resources/Prompts | 状态 |
|---|---|---|---|---|---|
| **A. HTTP 代理 `POST /historian/`** | `mcp-server/` 独立 module（`github.com/google/battery-historian/mcp-server`，v0.1.0） | 会调（Python 缺失则该 goroutine 报错，其他解析仍完成） | `uploadResponseCompare` 全量（含 `CheckinSummary`、`[]AppStat`、`HistogramStats`） | 5 / 3 / 2 | ✅ 已交付 |
| **B. 原生 `--mcp` 进程内直调 Core** | `cmd/battery-historian/mcp.go` + `mcp_store.go`（主仓 `github.com/reedhoop/ai-battery-historian`，v0.3.0） | **不调**（`ParsedData.skipPlot=true` 跳过 `generateHistorianPlot`） | `analyzer.AnalysisResults` 全量（含 `aggregated.Checkin`、`[]AppStat`、`HistogramStats`、`RawCheckin`、`PlotHTML`、`ReportHTML`、`Health`，P4 起追加 `PowerSummary`/`AlarmSummary`/`ActivityStats`/`ProcStats`） | 13 / 10 / 3 | ✅ 已交付 |

- **Form B 不走"CLI shell `local_checkin_parse`"路径**：原设计 §3.3 的"形态 B"指的是 CLI shell，实际实施时直接做成了进程内 `analyzer.Analyze` 调用，避免了 `parseAppStats`/`extractHistogramStats` 未导出的重组工作——Core facade 内部通过 `pd.data`/`pd.responseArr` 直接拿到已组装的 `AnalysisResult`，无需重新暴露这两个未导出函数。
- NFR-02 验收据此区分：Form A「允许 doHistorian 报错但结构化数据正常」；Form B「完全不调 Python，结构化数据由 Core 直接组装」。

---

## 4. 核心流程

### 4.1 analyze_bugreport（FR-01）
```
AI → Tool.analyze_bugreport(path|content)
   → 读文件 / 解 base64 → 100MB 守卫 → AnalysisCore.Analyze(contents string)
   → 并行: checkinparse / parseutils / activity / broadcasts / dmesg / aggregated / presenter
   → postProcess: PlotHTML fallback (generateV2ChartSVG) + ReportHTML (generateReportHTML) + Health (health.Evaluate)
   → 组装 AnalysisResults[0] → Store.Put(id) → 返回 {id, deviceModel, sdkVersion, topN appStats, criticalError?}
```

### 4.2 compare_bugreports（FR-02）
```
AI → Tool.compare_bugreports(a, b)
   → 各自 loadInput → AnalysisCore.Compare(contentsA, contentsB)
       内部：pd.parseBugReport(a, b) 一次性解析两份，自动判断 delta vs 独立
   → 若同设备同时段：UsingComparison=true, Reports=[delta]，Reports[0].IsDiff=true
   → 否则：UsingComparison=false, Reports=[reportA, reportB]
   → presenter.CombineCheckinData(pd.data) → CompareResult.Combined
   → Store.Put(id) → 返回逐项差值（按 |差值| 排序）
```
> 注意：实际实现 **不**走"各自 Analyze 再取 RawCheckin 差分"路径，而是直接调 `analyzer.Compare`，其内部一次 `parseBugReport` 同时解析两份并自动判定 delta vs 独立。`presenter.CombineCheckinData`（**首字母大写已导出**，`presenter.go:421`）合并两份 `HTMLData`。`checkindelta.ComputeDeltaFromSameDevice(first, second *bspb.BatteryStats)`（`checkindelta/checkin_delta.go:70`）由 `parseBugReport` 内部调用，MCP 层不直接调。

### 4.3 query_*（FR-03~08, P3-C, P4）
```
AI → Tool.query_system_stats(id) / query_app_stats(id, uid?, topN?) / query_histogram(id)
       / query_wakelocks(id, kind?) / query_wakeup_reasons(id) / query_sync_tasks(id) / query_health(id)
       / query_power(id) / query_alarms(id, topN?) / query_activity(id, kind?) / query_procstats(id, topN?)   ← P4
   → Store.Get(id) 命中 → 取 AnalysisResult 的对应字段（Top-N / kind 过滤在 MCP 层做）
   → 返回 JSON（Resource 模式亦可 bugreport://{id}/... 直接读）
```
> 会话内二次查询**不重新解析**，仅从内存结果取数，秒级响应。P4 的 4 个 dumpsys 段在 `analysisResults()` 末尾一次性解析并缓存到 `AnalysisResult`，query 时零再解析。

---

## 5. MCP 能力详细设计（接口规格）

> 下表反映 Form B（`cmd/battery-historian --mcp`，v0.3.0）实际注册的能力。Form A（`mcp-server/`，v0.1.0）仅含前 5 个 tool + 前 3 个 resource + 前 2 个 prompt（即 FR-01..FR-05 + FR-12/13）；P4 的 4 个 dumpsys 段工具 / 资源为 Form B 独有。

### 5.1 Tools
| Tool | Input | Output | 底层调用 | 实施 |
|---|---|---|---|---|
| `analyze_bugreport` | `{path?:string, content?:base64}` | `{id, deviceModel, sdkVersion, appStatsTopN[], criticalError?}` | `analyzer.Analyze` | ✅ A+B |
| `compare_bugreports` | `{path_a\|content_a, path_b\|content_b}` | `CompareResult`（含 `Combined`） | `analyzer.Compare` → `presenter.CombineCheckinData` | ✅ A+B |
| `query_system_stats` | `{id, report_index?}` | `aggregated.Checkin` | `AnalysisResult.Checkin` | ✅ A+B |
| `query_app_stats` | `{id, report_index?, uid?, topN?}` | `[]AppStat`（Top-N） | `AnalysisResult.AppStats` | ✅ A+B |
| `query_histogram` | `{id, report_index?}` | `HistogramStats` | `AnalysisResult.HistogramStats` | ✅ A+B |
| `query_wakelocks` | `{id, report_index?, kind?:userspace\|kernel}` | `[]ActivityData`（按耗时排序） | `Checkin.UserspaceWakelocks/KernelWakelocks` | ✅ B only |
| `query_wakeup_reasons` | `{id, report_index?}` | `[]ActivityData`（解码后） | `Checkin.WakeupReasons` + `wakeupreason` | ✅ B only |
| `query_sync_tasks` | `{id, report_index?}` | `[]ActivityData` | `Checkin.SyncTasks` | ✅ B only |
| `query_health` | `{id, report_index?}` | `*health.Report`（P3-C） | `AnalysisResult.Health` | ✅ B only |
| `query_power` | `{id, report_index?}` | `*power.Summary`（P4） | `AnalysisResult.PowerSummary` | ✅ B only |
| `query_alarms` | `{id, report_index?, topN?}` | `*alarm.Summary`（P4，TopAlarms 截断到 topN） | `AnalysisResult.AlarmSummary` | ✅ B only |
| `query_activity` | `{id, report_index?, kind?:anr\|lmk\|exits\|running\|all}` | `*dumpsysactivity.Summary` 或子集（P4） | `AnalysisResult.ActivityStats` | ✅ B only |
| `query_procstats` | `{id, report_index?, topN?}` | `*procstats.Summary`（P4，Processes 截断到 topN） | `AnalysisResult.ProcStats` | ✅ B only |

### 5.2 Resources
| URI 模板 | 内容 | 对应结构 | 实施 |
|---|---|---|---|
| `bugreport://{id}/system_stats` | 系统级全量指标 | `aggregated.Checkin` | ✅ A+B |
| `bugreport://{id}/app_stats/{uid}` | 单应用明细 | `presenter.AppStat` | ✅ A+B |
| `bugreport://{id}/raw_checkin` | 原始 batterystats proto→json | `bspb.BatteryStats` + `protojson` | ✅ A+B |
| `bugreport://{id}/chart` | Historian plot HTML 或 V2 SVG fallback（P3-B） | `AnalysisResult.PlotHTML` | ✅ B only |
| `bugreport://{id}/report` | 自包含分析报告 HTML（P3-B） | `AnalysisResult.ReportHTML` | ✅ B only |
| `bugreport://{id}/health` | 健康度评分 JSON（P3-C） | `*health.Report` | ✅ B only |
| `bugreport://{id}/power` | dumpsys power 段完整 JSON（P4） | `*power.Summary` | ✅ B only |
| `bugreport://{id}/alarms` | dumpsys alarm 段完整 JSON（P4） | `*alarm.Summary` | ✅ B only |
| `bugreport://{id}/activity` | dumpsys activity 段完整 JSON（P4） | `*dumpsysactivity.Summary` | ✅ B only |
| `bugreport://{id}/procstats` | dumpsys procstats 段完整 JSON（P4） | `*procstats.Summary` | ✅ B only |

### 5.3 Prompts
| Prompt | 入参 | 产出 | 实施 |
|---|---|---|---|
| `battery_root_cause` | 系统/应用指标 | 结构化根因分析框架（引导 AI 关联 wakelock/CPU/同步/信号） | ✅ A+B |
| `battery_ab_report` | A/B diff | 升级前后耗电变化结论文本 | ✅ A+B |
| `battery_health_report` | 健康度报告 | 解读与改进建议（P3-C） | ✅ B only |

---

## 6. 关键数据结构设计

- **复用而非新建**：系统级直接用 `aggregated.Checkin`（`aggregated/aggregated_stats.go:298 type Checkin struct`，含放电 mAh、亮/灭屏放电率、各类 wakelock/同步/CPU/GPS 时长与占比）；应用级用 `presenter.AppStat`（含 `RawStats *bspb.BatteryStats_App`、`DevicePowerPrediction float32` 字段 `presenter.go:61`）；健康度直方图用 `presenter.HistogramStats`（`presenter.go:125`）。
- **Core 层 `AnalysisResult`**（`analyzer/core.go:33`，已实现，字段详见 §3.1）：含 `Checkin` / `AppStats` / `HistogramStats` / `RawCheckin` + 元数据 + P3-B `PlotHTML`/`ReportHTML` + P3-C `Health`。
- **分页/Top-N 包装**（FR-15，MCP 层实现）：
  ```go
  type AppStatsPage struct {
      Total   int         `json:"total"`
      TopN    []AppStat   `json:"topN"`     // 默认 20，按 DevicePowerPrediction 降序
      Next    *string     `json:"next,omitempty"` // 游标分页（未来扩展）
  }
  ```
- **proto→json**：Resource `raw_checkin` 用 `github.com/golang/protobuf/jsonpb`（沿用 legacy protobuf 生态，避免与 `google.golang.org/protobuf/encoding/protojson` 共存引入额外依赖）。
- **P3-C 健康度报告**（`analyzer/health/health.go:59`）：
  ```go
  type Report struct {
      Score       float64     `json:"score"`       // 0~100
      Grade       string      `json:"grade"`       // A≥85 / B≥70 / C≥55 / D≥40 / F<40
      Summary     string      `json:"summary"`
      Dimensions  []Dimension `json:"dimensions"`  // 6 个维度
      Alerts      []Alert     `json:"alerts"`
      GeneratedAt time.Time   `json:"generatedAt"`
  }
  ```
  6 维度加权（权重和=1.0，`health.go:69-76`）：standby_drain 0.30 / wakelock_burden 0.20 / wakeup_sync_freq 0.15 / app_stability 0.15 / doze_adoption 0.10 / modem_activity 0.10。
- **P3-B 图表 HTML**：`AnalysisResult.PlotHTML` 有两种填充路径——(a) `AnalyzeWithChart` 走 Python `scripts/historian.py`（需 `--mcp_with_chart`）；(b) `Analyze`/`Compare` 在 `postProcess` 中 `PlotHTML == ""` 时调 `generateV2ChartSVG`（`chart_v2.go:119`，纯 Go，仅 Format:2 报告）生成自包含 inline-SVG 电量曲线。`ReportHTML` 由 `generateReportHTML`（`chart_v2.go:301`）始终填充，含设备元数据 + 健康度卡片 + 电量曲线 + 关键聚合统计。

---

## 7. 与现有系统的集成与改造点

| 改造点 | 范围 | 实施情况 |
|---|---|---|
| 提取 Analysis Core | 中 | ✅ `analyzer/core.go` 落地 `Analyze` / `AnalyzeWithChart` / `Compare`。解析库函数本身未改。 |
| 跳过 Historian 画图 | 小 | ✅ Form B 通过 `ParsedData.skipPlot=true`（core.go:85）跳过 `generateHistorianPlot`；Form A 仍触发 doHistorian，Python 缺失仅报错、结构化数据不受影响（NFR-02） |
| 主仓模块化（P2） | 中 | ✅ `go.mod` 已加（module `github.com/reedhoop/ai-battery-historian`，go 1.25.5），全仓 import 路径已替换；`golang/protobuf`↔`google.golang.org/protobuf` 共存编译通过（NFR-03） |
| 新增 MCP Server 进程/包 | 中 | ✅ Form A `mcp-server/`（独立 module）+ Form B `cmd/battery-historian/mcp.go`（`--mcp` 标志，battery-historian.go:43-47） |
| 新增 P3-B 图表 fallback | 中 | ✅ `analyzer/chart_v2.go` 落地 `generateV2ChartSVG` + `generateReportHTML` |
| 新增 P3-C 健康度评分 | 中 | ✅ `analyzer/health/health.go` 落地 `Evaluate` + 6 维度评分 |
| **不改的部分** | — | 所有解析库（`checkinparse`/`parseutils`/`aggregated`/`presenter` 等）逻辑、前端 JS、proto 定义均保持原样 |
| **遗留 bug** | 小 | ~~P3-B/P3-C 新增的 6 个文件误用 `github.com/google/battery-historian/...` 旧 import 路径~~ → 已批量替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` 通过（详见 §10.5） |
| **Code Review 修复（2026-07-19）** | 中 | 14 项 P0/P1/P2/P3 优化全部修复：base64 预检 / 路径沙箱 / prompt 注入防护 / Compare `report_index` / alert 单位 + 二级排序 / Health 全无效 N/A + NaN/Inf 防护 / 5 维度无数据判定（详见 §12.3） |

---

## 8. 非功能性设计

| 方面 | 设计 | 实施情况 |
|---|---|---|
| 错误处理 | 复用 `uploadResponse.CriticalError/Note`；低版本（`<Android 5.0`，`minSupportedSDK=21` `analyzer.go:62`）显式返回而非空结果（FR-16） | ✅ |
| 安全 | 拒绝 `>100MB`（`analyzer.go:60 maxFileSize`，Form A `tools.go:75` / Form B `mcp.go:44` 各自镜像）；路径穿越校验复用 `bugreportutils.Contents`（NFR-04） | ✅ |
| 性能/超时 | 会话缓存避免重复解析；Form A `HistorianClient.HTTP.Timeout = 5 * time.Minute`（`mcp-server/historian.go:24`）；Form B 进程内调用无 HTTP 超时（解析为同步，足够） | ✅ |
| 大结果控制 | 列表默认 Top-20；原始大数据走 Resource 按需拉（FR-15） | ✅ |
| 传输 | stdio 本地默认；streamable HTTP 远程可选（NFR-05） | ✅ Form A `--transport=http` + Form B `--mcp_transport=http` |
| 可观测 | 复用现有 `log.Printf` 的 Trace 日志；MCP 层增加 tool 调用日志 | ✅ |

---

## 9. 分阶段实施设计映射

- **Phase 0（准备）✅ 已完成**：① 实际 go.mod 声明 go 1.25.5（非评估时的 1.21）；② 主仓 module 名 `github.com/reedhoop/ai-battery-historian`，全仓 import 路径已替换（P3-B/C 新增文件除外）；③ mcp-go 能力核对通过：`mark3labs/mcp-go v0.56.0` 支持 Tool/Resource/Prompt + stdio/streamable HTTP 双传输。
- **P1（独立进程包裹，Form A）✅ 已完成**：`mcp-server/` 独立 module → HTTP 代理 `POST /historian/` → 实现 FR-01/02/03/04/05(形态A)/12/13/14/15/16 + NFR-01(独立)/02(形态A)/04/05/06。零改 legacy。FR-05 在形态A 下完整交付（HistogramStats 随 `uploadResponseCompare` 返回）。
- **P2（原生嵌入，Form B）✅ 已完成（待修 import 路径 bug）**：主仓加 `go.mod` → `analyzer/core.go` 落地 `Analyze`/`AnalyzeWithChart`/`Compare` facade（进程内直调，`skipPlot=true` 彻底去 Python）→ 实现 FR-06/07/08/09/10/11 + NFR-01(主仓)/02(形态B)/03 → `cmd/battery-historian --mcp`。**未**走"暴露 `parseAppStats`/`extractHistogramStats`"路径——Core facade 通过 `pd.data`/`pd.responseArr` 直接拿到已组装结果，绕开了未导出函数的重组工作。
- **P3-B（图表 fallback）✅ 已完成**：`analyzer/chart_v2.go` 落地 `generateV2ChartSVG` + `generateReportHTML`，作为 `bugreport://{id}/chart` 与 `bugreport://{id}/report` resource。
- **P3-C（健康度评分）✅ 已完成**：`analyzer/health/health.go` 落地 `Evaluate` + 6 维度评分，通过 `query_health` tool + `bugreport://{id}/health` resource + `battery_health_report` prompt 暴露。
- **P3-A（Python 3 迁移）⏸ 未做**：P3-B 的 SVG fallback 已能满足 Format:2 报告需求；legacy Format:1 报告若需 Historian 风格图表仍需 `--mcp_with_chart` + Python 3 + 已迁移的 `scripts/historian.py`。
- **P4（OEM 功耗分析扩展）✅ 已完成**：详见 `OEM功耗分析扩展设计.md`。落地 4 个 dumpsys 段解析器（`analyzer/power` / `analyzer/alarm` / `analyzer/dumpsysactivity` / `analyzer/procstats`），在 `analysisResults()` 末尾一次性解析并缓存到 `AnalysisResult.PowerSummary/AlarmSummary/ActivityStats/ProcStats`；MCP 层追加 4 tools（`query_power` / `query_alarms` / `query_activity` / `query_procstats`）+ 4 resources（`bugreport://{id}/{power,alarms,activity,procstats}`），全部支持 `report_index`，与基于 `dumpsys batterystats` 的 `query_wakelocks` 等互补，构成「唤醒源归因 + 功耗大户行为佐证」闭环。端到端冒烟测试通过（真实 T952K bugreport：power 2 wakelocks/5 blockers/6 drainStats，alarm 69 pending/20 top，activity 12 LMK/624 exits/83 running，procstats 1586 进程）。

---

## 10. 设计层风险与对策

| 风险 | 对策 | 状态 |
|---|---|---|
| 重复解析浪费 IO | `Store` 会话缓存，二次查询内存取数（§3.2） | ✅ 已落实 |
| P2 protobuf 双版本共存 | `golang/protobuf` 已是 wrapper；引入 mcp-go 后编译验证，必要时加适配层（NFR-03） | ✅ 编译通过 |
| 结果过大撑爆上下文 | Top-N 默认 + Resource 按需（§6、FR-15） | ✅ 已落实 |
| 低版本报告数据缺失被误读 | 显式 `criticalError` 透传（FR-16） | ✅ 已落实 |
| **~~P3-B/P3-C 新增文件 import 路径 bug~~** | 把 6 个文件中 `github.com/google/battery-historian/...` 批量替换为 `github.com/reedhoop/ai-battery-historian/...` | ✅ 已修复，`go build ./...` 通过 |
| **Form A 与 Form B 代码重复** | 两套 Store / argString / buildSummary / prompts handler 重复，未来维护需双向同步 | ⏸ 待评估是否抽取公共子包 |
| **【2026-07-19 修复】base64 DoS / 路径沙箱 / prompt 注入** | `loadInput` 加 `maxEncodedLen` 预检 + `filepath.Clean` + `IsRegular` 守卫；prompt handler 加 `wrapUserData` 注入防护 | ✅ 已修复 |
| **【2026-07-19 修复】Compare B 报告无法查询 / alert 单位与排序 / Health 全无效** | 全部 7 个 query_* 工具加 `report_index`；`buildAlerts` 加 `alertValue` 单位 + score 二级排序；`Evaluate` 全无效返回 `Grade="N/A"` | ✅ 已修复 |

---

## 11. 设计验收对照（对应需求矩阵）

- 每个 FR/NFR 在 §3~§8 均有落点；P1/P2/P3 阶段包与 `MCP需求矩阵.md` 第三节完全对齐，且均已交付（除 P3-A Python 3 迁移）。
- 架构满足"传输无关 + 最小侵入"：解析库零改动，新增仅 Core facade + MCP Server + P3-B/C 三层。
- **全量验收通过**：`go build ./...` / `go vet ./analyzer/... ./cmd/battery-historian/...` / `go test ./analyzer/...` 全绿；code review 发现的 14 项 P0/P1/P2/P3 问题全部修复（详见 §12.3）。

---

## 12. 修订记录

### 2026-07-17 代码级评审（初稿审计）
初稿经代码级审计，发现 4 处问题，已全部修正：
1. **§6 / FR-15 排序事实错误**：原称"复用 `presenter.parseAppStats` 排序做 Top-N"。实测 `parseAppStats`（`presenter.go:251`，未导出）按 `byName`=应用名 alphabetical 排序，不能按耗电占比排。改为 **MCP 层按 `AppStat.DevicePowerPrediction` 降序自排序**。
2. **§3.3 NFR-02 两种形态差异补全**：HTTP 代理形态(形态A)经 `parseBugReport` **必然**触发 `doHistorian`→`generateHistorianPlot`（Python 缺失仅该 goroutine 报错，结构化数据正常）；CLI 形态(形态B)纯 Go 不调 Python，但仅产出原始 `*bspb.BatteryStats` proto，**缺** `aggregated.Checkin`/`[]AppStat`/`HistogramStats`，需 MCP 侧自行重组（等价于 P2 Core 工作量）。
3. **AnalyzeHistory 非纯函数（小）**：`AnalyzeHistory(csvWriter io.Writer, ...)`（`parseutils.go:3094`）向 `io.Writer` 写 CSV 作副作用；已在 §3.1 标注 facade 须传 `io.Discard` 或仅取 `AnalysisReport.Summaries`。
4. **设计层补充**：① §3.2 `analysisStore` 增加 LRU+上限N+无持久化+可选 TTL；② §3.1 `AnalysisResult` 增加未导出 `rawCheckin *bspb.BatteryStats`；③ Phase 0 新增「mcp-go 能力核对」。

### 2026-07-18 现状校准（实施后回填）
代码已实现 P1/P2/P3-B/P3-C，对设计文档做现状校准，发现并修正 6 处偏差：
1. **§3.1 Core 契约对齐**：原设计 `Analyze([]byte, Options) (*AnalysisResult, error)` → 实际 `Analyze(string) (AnalysisResults, error)`；`Compare` 返回 `*CompareResult`（包装 `Combined`）而非裸 `*CombinedCheckinSummary`；无 `Options` 类型，改用 `ParsedData.skipPlot` 字段；字段名 `SystemStats→Checkin`、`Histogram→HistogramStats`、`rawCheckin→RawCheckin`（已导出）；新增 P3-B `PlotHTML/ReportHTML` + P3-C `Health` 字段；无 `ID` 字段（id 在 MCP 层维护）。
2. **§3.3 形态 B 实际路径修正**：原设计"CLI shell `local_checkin_parse`"未采用，实际做成进程内 `analyzer.Analyze` 直调，避免暴露 `parseAppStats`/`extractHistogramStats`。
3. **§4.2 Compare 流程修正**：原设计"各自 Analyze 再取 RawCheckin 差分"未采用，实际 `analyzer.Compare` 内部一次 `parseBugReport` 同时解析两份并自动判定 delta vs 独立；`presenter.CombineCheckinData`（**首字母大写已导出**，`presenter.go:421`）合并视图。
4. **§5 能力清单补全**：补 P3-B `chart`/`report` resource + P3-C `query_health` tool / `health` resource / `battery_health_report` prompt；标注 Form A/B 各自实现范围。
5. **§6 数据结构补全**：补 `health.Report` 结构与 6 维度权重；补 `PlotHTML` 两种填充路径。
6. **行号校准**（系统性偏小 2-20 行）：`parseBugReport` 659→669、`AnalyzeFiles` 554→564、`AnalyzeAndResponse` 543→553、`generateHistorianPlot` 1023→1043、`Data` 826→828、`CombineCheckinData` 419→421、`maxFileSize` 59→60、`minSupportedSDK` 61→62、`type Checkin struct` → 298、`ParseCheckinData` → 441。
7. **新增阻塞项**：P3-B/P3-C 新增的 6 个文件（`analyzer/core.go` / `analyzer/health/health.go` / `analyzer/health/health_test.go` / `cmd/battery-historian/mcp.go` / `cmd/battery-historian/mcp_store.go` / `cmd/healthcheck/main.go`）误用 `github.com/google/battery-historian/...` 旧 import 路径，与 go.mod 模块路径冲突，`go build ./...` 失败。

### 2026-07-19 Code Review 修复回填
import 路径 bug 已批量修复，并完成一轮 code review 发现的 14 项 P0/P1/P2/P3 优化，对设计文档做同步回填：

1. **import 路径 bug 已解决**：6 个文件批量替换为 `github.com/reedhoop/ai-battery-historian/...`，`go build ./...` / `go vet ./analyzer/... ./cmd/battery-historian/...` / `go test ./analyzer/...` 全部通过。
2. **§3.1 契约补全**：`Compare` 的 `UsingComparison` 判定补充"必须 `Reports[0].IsDiff == true`"条件，避免两份独立报告被误判为 delta。
3. **§3.1 `generateReportHTML` 复用 PlotHTML**：签名从 `(r, contents string)` 改为 `(r, plotHTML string)`，直接嵌入已解析的 `r.PlotHTML` 而非重新解析 V2 bugreport，保证 `/report` 与 `/chart` 图表一致。
4. **§5.1 query_* 工具新增 `report_index` 参数**：全部 7 个 query_* 工具（`query_system_stats` / `query_app_stats` / `query_histogram` / `query_wakelocks` / `query_wakeup_reasons` / `query_sync_tasks` / `query_health`）增加可选 `report_index`（默认 0=A，1=B），`resultForID` / `reportIndexFromReq` helper 校验范围；A/B 对比的 B 报告现在可被任意 query_* 工具访问。
5. **§5.3 Prompts 注入防护**：三个 prompt handler（`battery_root_cause` / `battery_ab_report` / `battery_health_report`）的 user-supplied 参数改用 `wrapUserData` 包装为 `<user_data>...</user_data>`，并在 prompt 前置 `promptInjectionGuard` 声明该标签内为不可信 DATA。
6. **§6 `health.Report` 评分逻辑加固**：
   - 新增 `isFinite` helper（`!math.IsNaN && !math.IsInf`），`Evaluate` / `lerpDown` / `lerpUp` 防护 NaN/Inf 输入。
   - 全维度 `Valid=false` 时返回 `Grade="N/A"` + 说明性 Summary（不再返回误导性的 0 分 / F 等级）。
   - `wakelock_burden` / `wakeup_sync_freq` / `app_stability` / `doze_adoption` / `modem_activity` 5 个维度增加"无数据"判定（计数器零值 / 负值 / 字段未填充），返回 `Valid=false` 而非 0 分。
7. **§8 安全（NFR-04）加固**：`loadInput` 增加 (a) base64 **编码长度预检**（`maxEncodedLen = maxFileSize*4/3 + 4`）避免解码 DoS；(b) `filepath.Clean` 规范化 + `fi.Mode().IsRegular()` 守卫，拒绝目录 / 设备文件 / socket / 管道。
8. **§6 `health.Alert` 输出优化**：`buildAlerts` 新增 `alertValue` helper 按维度返回带单位值（`%/h` / `%` / `次/h` / `次`）；排序从单 level 改为 level + score 升序二级排序，同 severity 内更差者先出。
9. **§6 `summarize` worst 排序**：worst 列表按 score 升序，扣分最严重的维度先输出。
10. **FR-06/07/10/11 修复（前一轮完成，本轮回填文档）**：FR-10 `extractIDs` 3 段 URI 解析、FR-06 `kind` 参数注册、FR-07 `wakeupReasonsHandler` 接入 `FindSubsystem`、FR-11 `raw_checkin` 改用 `jsonpb` 输出 JSON。

### 2026-07-19 P4（OEM 功耗分析扩展）落地回填
依据 `OEM功耗分析扩展设计.md` 实施 P0 阶段，新增 4 个 dumpsys 段解析器与对应 MCP 能力，对设计文档做同步回填：

1. **§3.1 Core 契约扩展**：`AnalysisResult` 新增 4 字段 `PowerSummary *power.Summary` / `AlarmSummary *alarm.Summary` / `ActivityStats *dumpsysactivity.Summary` / `ProcStats *procstats.Summary`；`ParsedData` 新增 `bugReportContentsA/B string` 保存原始文本；`analysisResults()` 末尾一次性解析 4 段（不进 `parseBugReport`，主解析路径零回归）；单段失败只置 nil 不阻塞其他段。
2. **§3.3 形态 B 能力数升级**：Tools 9→13、Resources 6→10，版本号 v0.2.0→v0.3.0。
3. **§4.3 query_* 流程扩展**：追加 `query_power` / `query_alarms` / `query_activity` / `query_procstats` 4 个工具，复用 `resultForID` / `reportIndexFromReq`，支持 `report_index` 与 `topN` / `kind` 过滤。
4. **§5 能力清单补全**：§5.1 追加 4 行 tool 规格（含 `topN` / `kind` 参数语义）；§5.2 追加 4 行 resource 规格（`bugreport://{id}/{power,alarms,activity,procstats}`，返回完整 JSON 无裁剪）。
5. **§9 分阶段映射追加 P4 条目**：4 个解析器 package 路径、MCP 层 4 tools + 4 resources、端到端冒烟测试结论（真实 T952K bugreport 数据）。
6. **包路径约定**：dumpsys activity 段解析器放 `analyzer/dumpsysactivity` 子包（避开已被占用的顶级 `activity/` 包），其余三个直接放 `analyzer/<name>`。
7. **基础设施依赖**：`historianutils.ServiceDumpRE` 升级支持可选优先级前缀 `CRITICAL/HIGH/NORMAL`（保留 `service` group 名向后兼容）；`bugreportutils.ExtractServiceDump` + `ActivitySubsectionRE` 为 4 个解析器提供段 / 子段切分。
8. **安全继承**：4 个新 tool / resource 全部走 `resultForID` / `primaryResult`，复用现有 id + `report_index` 校验；段原文不直接返回（避免超大 bugreport 整段塞给 MCP 客户端），只返回结构化 JSON；`ANRRecord.FullText` 截断 4KB。

> 对应需求侧修订见 `MCP需求矩阵.md` 第六节。
