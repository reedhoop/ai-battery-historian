# Battery Historian · OEM 功耗分析扩展设计（P0：唤醒源闭环）

> 配套文档：`MCP可行性评估.md` / `MCP概要设计.md` / `MCP需求矩阵.md`
> 设计目标：在现有 MCP 能力（仅覆盖 `dumpsys batterystats` 一个段）基础上，补齐 bugreport 中其他与功耗相关的 dumpsys 段，让 OEM 设备测试期能做完整的「唤醒源归因 + 功耗大户行为佐证」分析。
> 设计主线：**bugreportutils 新增段抽取器 → AnalysisResult 扩字段 → MCP 注册新 query_* 工具**，三层增量落地，不动 `parseBugReport`。

---

## ⚠️ 功能移除声明（2026-07-19）

> 自定义电池**健康度评分**功能（原 P3-C）已于 commit `8a271c6` 整体移除：含 `analyzer/health` 包、`query_health` tool、`bugreport://{id}/health` resource、`battery_health_report` prompt、`cmd/healthcheck` CLI。用户认为其对问题分析实际意义不大。**原生 batterystats `HistogramStats`（FR-05 `query_histogram`，即「健康度直方图」）保留不受影响。** 本设计文档 §1.1 现状与 §5.3 提到的「健康度评分 / battery_health_report」均指向**已移除**能力，请勿据此对接。

---

## 1. 背景与动机

### 1.1 现状
当前 MCP（Form B，v0.2.0）9 tools（含已移除的 `query_health`，现 8）/ 6 resources / 2 prompts（`battery_health_report` 已于 commit 8a271c6 移除）全部基于 `dumpsys batterystats` 一个段。能回答：
- 功耗大户排名（按 mAh 预测）
- wakelock 时长排名、唤醒原因 Top-N、sync 任务频率
- 待机/亮屏放电率、A/B delta、健康度评分（⚠️ 自定义健康度评分功能已于 commit 8a271c6 移除，原生 `HistogramStats` 直方图保留）

### 1.2 不能回答的问题（OEM 测试痛点）
- **"这个 wakelock 是谁持有的、持了多久没释放？"** → batterystats 只给聚合时长，给不了实时快照
- **"为什么设备每 5 分钟醒一次？"** → batterystats 不知道定时唤醒源，alarm 段才有
- **"功耗大户是因为一直在前台，还是被反复拉起？"** → activity 段才有进程状态/被杀记录
- **"应用被杀重启了几次？内存压力多大？"** → procstats 段才有进程状态时长分布

### 1.3 P0 目标
接入 4 个 dumpsys 段，构成完整唤醒源闭环：

| 段 | 解决的问题 | 与 batterystats 的互补 |
|---|---|---|
| `power` | 实时 wakelock 持有快照、suspend 次数、wakefulness 状态机 | batterystats 给聚合，power 给当前快照 |
| `alarm` | 定时唤醒源队列、Top-N alarm 排名 | batterystats 不知道谁定时唤醒 |
| `activity` | 进程被杀/ANR/重启次数、前台切换 | 把功耗大户与实际行为关联 |
| `procstats` | 进程状态时长分布、内存压力 | 频繁被杀重启=CPU 重新拉起=耗电 |

---

## 2. bugreport 段定位机制（关键约束）

### 2.1 两类段标记
bugreport 里 dumpsys 段有两种标记格式，**现有 `historianutils.ServiceDumpRE` 只匹配第二种**：

```
# 格式 A：带优先级前缀（CRITICAL / HIGH / NORMAL）
DUMP OF SERVICE CRITICAL power:
DUMP OF SERVICE CRITICAL activity:
DUMP OF SERVICE HIGH meminfo:

# 格式 B：无前缀
DUMP OF SERVICE activity:
DUMP OF SERVICE alarm:
DUMP OF SERVICE procstats:
```

实测同一段名可能两种格式都出现（如 `activity` 在 CRITICAL 段和无前缀段都有），需统一匹配。

### 2.2 ServiceDumpRE 升级
`historianutils/historianutils.go:32` 现有正则：
```go
ServiceDumpRE = regexp.MustCompile(`^DUMP\s+OF\s+SERVICE\s+(?P<service>\S+):`)
```

升级为支持可选优先级前缀（**保留原 capture group 名 `service`**，向后兼容）：
```go
// ServiceDumpRE matches the start of a dumpsys service section. The optional
// priority token (CRITICAL / HIGH / NORMAL) introduced in newer bugreports is
// NOT captured — only the service name goes into the `service` group, so all
// existing call sites keep working.
ServiceDumpRE = regexp.MustCompile(`^DUMP\s+OF\s+SERVICE\s+(?:CRITICAL|HIGH|NORMAL\s+)?(?P<service>\S+):`)
```

> ⚠️ `extractSensorInfo`（bugreportutils.go:188）依赖此正则匹配 `sensorservice`，升级后仍能命中（`DUMP OF SERVICE CRITICAL sensorservice:`），向后兼容。

### 2.3 activity 段的子段切分
activity 段单段可达 5 万行，必须二次切分。实测子段标题格式：
```
ACTIVITY MANAGER LAST ANR (dumpsys activity lastanr)
ACTIVITY MANAGER LMK KILLS (dumpsys activity lmk)
ACTIVITY MANAGER PROCESS EXIT INFO (dumpsys activity exit-info)
ACTIVITY MANAGER RUNNING PROCESSES (dumpsys activity processes)
ACTIVITY MANAGER LRU PROCESSES (dumpsys activity start-info)
```

新增正则（`bugreportutils` 包级变量）：
```go
// activitySubsectionRE matches the subsection headers inside the activity
// dumpsys section. The subsection name (e.g. "lastanr", "lmk", "processes")
// goes into the `subsection` group.
activitySubsectionRE = regexp.MustCompile(`^ACTIVITY MANAGER\s+[A-Z ]+\s+\(dumpsys activity (?P<subsection>\S+)\)`)
```

---

## 3. Core 契约变更

### 3.1 AnalysisResult 新增字段
`analyzer/core.go:33` 的 `AnalysisResult` 结构体追加 4 个字段：

```go
type AnalysisResult struct {
    // ... 既有字段不变 ...

    // P4（本设计）：其他 dumpsys 段解析结果。nil 表示该段在 bugreport
    // 中不存在或解析失败（不阻塞主流程）。每个字段的解析独立，单个失败
    // 不影响其他字段。
    //
    // 包路径约定：为避免与现有顶级 activity 包（解析 activity manager
    // 事件、输出 CSV）冲突，dumpsys activity 段的解析器放在
    // analyzer/dumpsysactivity 子包，其余三个直接放 analyzer/<name>。
    PowerSummary  *power.Summary           // dumpsys power
    AlarmSummary  *alarm.Summary           // dumpsys alarm
    ActivityStats *dumpsysactivity.Summary // dumpsys activity（含 ANR / LMK / 进程退出 / 运行中进程）
    ProcStats     *procstats.Summary       // dumpsys procstats
}
```

### 3.2 解析时机
在 `analyzer/core.go:131` 的 `analysisResults()` 函数末尾、`postProcess` 调用之前填充新字段。**不进 `parseBugReport`**，保证主解析路径零回归：

```go
// analysisResults 末尾追加：
for i := range results {
    r := results[i]
    raw := pd.bugReportContents // ParsedData 新增字段，存原始 bugreport 文本
    r.PowerSummary, _ = power.Parse(raw)
    r.AlarmSummary, _ = alarm.Parse(raw)
    r.ActivityStats, _ = dumpsysactivity.Parse(raw)
    r.ProcStats, _ = procstats.Parse(raw)
}
```

> `ParsedData` 新增 `bugReportContents string` 字段，在 `parseBugReport` 入口保存原始文本（已有 `contentsA` 参数，直接存即可）。

### 3.3 错误处理策略
- 段不存在 → 字段为 `nil`，不报错（MCP 工具返回 "section not found in bugreport"）
- 段存在但解析失败 → 字段为 `nil`，通过 `Note` 字段追加警告（不阻塞 CriticalError）
- 单个段失败不影响其他段

---

## 4. 数据结构设计

### 4.1 power 包（`analyzer/power/power.go`）

```go
package power

// Summary 是 dumpsys power 段的结构化镜像。
type Summary struct {
    Wakefulness       string        // "Awake" / "Asleep" / "Dreaming"
    IsPowered         bool
    PlugType          int           // 0=unplugged, 1=AC, 2=USB, 4=Wireless
    BatteryLevel      int           // 0-100
    DeviceIdleMode    bool
    LightDeviceIdleMode bool
    LastWakeTime      int64         // 毫秒，相对 boot
    LastSleepTime     int64
    LastSleepReason   string        // "timeout" / "powerkey" / "proximity" ...
    WakeLockSummary   int           // bitmask
    SuspendBlockers   []SuspendBlocker
    WakeLocks         []WakeLock    // 当前持有的 wakelock 实时快照
    UIDStates         []UIDState    // UID 活动状态
    BatterySaver      BatterySaverStats
}

type SuspendBlocker struct {
    Name      string // "PowerManagerService.WakeLocks" 等
    RefCount  int
    Holding   bool
    AcquiredAt string // 原始时间戳字符串
}

// WakeLock 是 power 段里 "Wake Locks:" 子段的实时快照。注意与
// aggregated.ActivityData（batterystats 聚合）区分：这里给的是当前
// 持有的 wakelock，含 ACQ 相对时间，不是聚合时长。
type WakeLock struct {
    Name      string // wakelock 名（含 tag，如 'AudioIn'）
    Level     string // "PARTIAL_WAKE_LOCK" / "FULL_WAKE_LOCK" ...
    UID       int32
    PID       int32
    AcquiredAgoMs int64 // ACQ=-15m12s377ms → 15*60*1000+12*1000+377
    Long      bool    // 是否标记 LONG
    WorkSource string // 原始 WorkSource 字符串
}

type UIDState struct {
    UID      int32
    Active   bool
    Count    int
    State    int
}

type BatterySaverStats struct {
    CurrentlyOn bool
    TimesFullEnabled    int
    TimesAdaptiveEnabled int
    // DrainStats 按 Doze 状态分组：NonDoze/Deep/Light × NonIntr/Intr
    DrainStats []DrainStat
}

type DrainStat struct {
    DozeMode    string // "NonDoze" / "Deep" / "Light"
    Interruptible bool
    DurationMin int
    MahUsed     float64
    PercentOfTotal float64
    MahPerHour  float64
}
```

### 4.2 alarm 包（`analyzer/alarm/alarm.go`）

```go
package alarm

type Summary struct {
    NowRTC        int64
    NowElapsed    int64
    RuntimeStartedISO string // "2026-07-13 17:53:27.030"
    RuntimeUptimeMs   int64  // elapsed uptime
    PendingAlarms int        // "69 pending alarms"
    Alarms        []Alarm
    TopAlarms     []Alarm    // 按 count 降序的前 N（默认 20）
    // 抽样统计
    NextWakeupAlarm     string // 原始行
    NextNonWakeupAlarm  string
    LastWakeup          string
    // AppStateTracker 摘要
    ForceAllAppsStandby bool
    ActiveUIDs          []int32
    // SCHEDULE_EXACT_ALARM 申请者
    ScheduleExactAlarmUIDs []int32
}

// Alarm 对应一条 "ELAPSED_WAKEUP #N: ..." 记录。
type Alarm struct {
    Seq           int      // 序号 #1, #2, ...
    Type          string   // "ELAPSED_WAKEUP" / "ELAPSED" / "RTC_WAKEUP" / "RTC"
    Tag           string   // tag=*alarm*:TIME_TICK 等
    PackageName   string   // com.android.nfc 等
    WhenElapsedMs int64    // 触发时间（elapsed 毫秒）
    WhenElapsedRel string  // "+20s754ms" 相对时间原文（保留可读性）
    RepeatIntervalMs int64 // 0=非重复，60000=每分钟
    Flags         int      // flags=0x8 等
    WindowMs      int64    // window=+7s500ms → 7500
    ExactAllowReason string // "allow-listed" / "listener" / ""
    Operation     string   // PendingIntent 目标，如 "com.android.nfc broadcastIntent"
    Listener      string   // listener 引用
}
```

### 4.3 dumpsysactivity 包（`analyzer/dumpsysactivity/activity.go`）

> 包名用 `dumpsysactivity` 而非 `activity`，因为顶级 `activity/` 包已被现有「activity manager 事件解析」占用。

```go
package dumpsysactivity

// Summary 是 dumpsys activity 段的精炼镜像。activity 段可达 5 万行，
// 这里只抽取与功耗相关的 4 个子段：LAST ANR / LMK KILLS / PROCESS EXIT
// INFO / RUNNING PROCESSES。
type Summary struct {
    LastANR         []ANRRecord
    LMKKills        []LMKKill
    ProcessExits    []ProcessExit
    RunningProcesses []RunningProcess
    TotalPersistent int // "Total persistent processes: 10"
}

// ANRRecord 对应 LAST ANR 子段的一条记录。
type ANRRecord struct {
    Timestamp string // 原始时间戳
    Process   string
    PID       int32
    UID       int32
    Package   string
    Reason    string // ANR 原因摘要
    FullText  string // 完整原文（截断到 4KB 防止过大）
}

// LMKKill 对应 LMK KILLS 子段。
type LMKKill struct {
    PID       int32
    UID       int32
    Package   string
    OomAdj    int
    Reason    string // "min" / "low" / "critical" 等
    Timestamp string
    RSSKB     int64 // 部分版本有
}

// ProcessExit 对应 PROCESS EXIT INFO 子段。
type ProcessExit struct {
    PID       int32
    UID       int32
    Package   string
    Reason    string // "Killed by system" / "Crashed" / "ANR" 等
    Timestamp string
    ExitCode  int
    RSSKB     int64
}

// RunningProcess 对应 RUNNING PROCESSES 子段的一条。
type RunningProcess struct {
    PID        int32
    UID        int32
    Package    string
    OomAdj     int
    OomAdjLabel string // "fore" / "vis" / "cache" 等
    State      string  // "PER" / "SVC" / "TOP" 等
    PSSKB      int64
}
```

### 4.4 procstats 包（`analyzer/procstats/procstats.go`）

```go
package procstats

// Summary 是 dumpsys procstats 段的结构化镜像。
type Summary struct {
    CommittedFrom string // "2026-07-13-15-35-12"
    Processes     []Process
}

// Process 对应一条 "* com.xxx / u0a106 / v20251243:" 块。
type Process struct {
    Package    string
    UID        string // 原样保留，可能是 "u0a106" / "1000" 等
    Version    string // v20251243
    Total      ProcessState // TOTAL 行
    States     []ProcessState // 各子状态行（Persistent / Top / Bnd Fgs / ...）
}

// ProcessState 对应一行 "         TOTAL: 99% (0,00-0,00-0,00/.../474MB-446MB-474MB over 2)"
type ProcessState struct {
    Label       string  // "TOTAL" / "Persistent" / "Top" / "Bnd Fgs" ...
    Percent     float64 // 99 / 35 / 64
    // 内存三列：min-avg-max over N
    RSSMinKB    int64
    RSSAvgKB    int64
    RSSMaxKB    int64
    RSSSamples  int    // "over 2" 的 2
    // CPU 三列（部分版本有）：min-avg-max
    CPUMinPct   float64
    CPUAvgPct   float64
    CPUMaxPct   float64
}
```

---

## 5. MCP 能力清单

### 5.1 新增 Tools（4 个）
全部沿用 `query_*` 前缀，按 dumpsys 服务名命名，与现有 `query_wakelocks` / `query_sync_tasks` 风格一致。

| Tool | Input | Output | 底层 |
|---|---|---|---|
| `query_power` | `{id, report_index?, minHeldMs?}` | `*power.Summary`（含实时 wakelock 快照、suspend blockers、UID 状态、battery saver drain） | `AnalysisResult.PowerSummary` |
| `query_alarms` | `{id, report_index?, topN?, package?, wakeupOnly?}` | `*alarm.Summary`（含 pending alarms + Top-N 排名） | `AnalysisResult.AlarmSummary` |
| `query_activity` | `{id, report_index?, kind?, topN?, package?, reason?, minRssKB?, oomAdjMax?}` | `*activity.Summary`（含 ANR / LMK / 进程退出 / 运行中进程） | `AnalysisResult.ActivityStats` |
| `query_procstats` | `{id, report_index?, topN?, package?, minPercent?}` | `*procstats.Summary`（含每进程状态时长分布 + 内存 RSS） | `AnalysisResult.ProcStats` |

> **精细化参数设计（v0.3.1+）**：dumpsys activity 单段可达数万行（T807D 真机 exits=624 / running=83，vegas_g exits=1740），默认 `kind=all` 全返回会导致 LLM 单次 tool 返回 token 爆炸。改造为"LLM 多轮 + tool 精细化参数"模式：
>
> - **topN（默认 20）**：4 个 tool 统一支持，每个子段最多返回 N 条；`topN=0` 表示不限制；`topN=5` 适合做概览
> - **package（子串，大小写不敏感）**：`query_activity` / `query_alarms` / `query_procstats` 共用，按包名缩小范围
> - **reason（子串）**：`query_activity` 专用，按退出原因过滤（如 `reason=CRASH` / `reason=ANR` / `reason=OTHER KILLS`）
> - **minRssKB（数值下限）**：`query_activity` 专用，过滤低 RSS 进程（如 `minRssKB=200000` 只看 RSS≥200MB 的）
> - **oomAdjMax（数值上限）**：`query_activity` 专用，按 oom_adj 上限过滤（如 `oomAdjMax=-800` 只看前台及以上进程）
> - **minPercent（数值下限）**：`query_procstats` 专用，过滤低占用进程（如 `minPercent=1.0` 跳过 <1% 噪声）
> - **wakeupOnly（布尔）**：`query_alarms` 专用，只返回唤醒型 alarm（Type 含 `_WAKEUP`）
> - **minHeldMs（数值下限）**：`query_power` 专用，过滤短暂持有的 wakelock（如 `minHeldMs=60000` 只看持有 ≥1 分钟的锁）
>
> **推荐多轮工作流**：
> 1. 第 1 轮：`query_activity kind=all, topN=5` → 拿到每个子段 Top 5 概览
> 2. 第 2 轮：发现 `com.foo` 频繁退出 → `query_activity kind=exits, package=com.foo, topN=50`
> 3. 第 3 轮：怀疑内存压力 → `query_activity kind=running, minRssKB=200000`
> 4. 第 4 轮：确认是 crash → `query_activity kind=exits, reason=CRASH`
>
> `query_activity` 单 kind 模式额外返回 `processExitsTotal` / `runningTotal` 字段，让 LLM 判断是否被 topN 截断、是否需要再调一次 `topN=0` 拿全部。

### 5.1.1 历史工具的精细化参数改造（v0.3.2）

除 P4 新增的 4 个 dumpsys 段工具外，对原有 batterystats 聚合工具也按"LLM 多轮 + tool 精细化参数"原则做了改造。改造依据是 5 个真机 bugreport 的实测数据：

| Bugreport | Checkin 全量 | UserspaceWakelocks | AppStats |
|---|---|---|---|
| 9185W | 297 KB | 295 条 / 74 KB | 114 条 / 332 KB |
| T705M | 141 KB | 51 条 / 12 KB | 76 条 / 73 KB |
| T807D (Android 17) | 133 KB | 52 条 / 11 KB | 71 条 / 83 KB |
| **T952K** | **563 KB** | **658 条 / 192 KB** | **160 条 / 688 KB** |
| vegas_g | 194 KB | 25 条 / 5 KB | 116 条 / 86 KB |

**`query_system_stats` 拆分 section 参数**（原直接返回整个 `r.Checkin`，T952K 高达 563 KB）：

| section | 返回内容 | 估算大小 |
|---|---|---|
| `overview`（默认） | metadata + system summary + 所有 `Agg*` 聚合字段（不含列表） | ~5 KB |
| `wakelocks` | UserspaceWakelocks + KernelWakelocks + AggKernelWakelocks | ~200 KB（topN=20 后 ~10 KB） |
| `sync` | SyncTasks + AggSyncTasks + ScheduledJobs + AggScheduledJobs | ~5 KB |
| `network` | TopMobileTrafficApps + TopWifiTrafficApps + WifiScanActivity + WifiFullLockActivity | ~10 KB |
| `power` | DevicePowerEstimates + TopMobileActiveApps + discharge 字段 | ~10 KB |
| `cpu` | CPUUsage + AggCPUUsage | ~5 KB |
| `wakeups` | WakeupReasons + AppWakeups + AppWakeupsByAlarmName + 聚合 | ~10 KB |
| `anr` | ANRAndCrash + TotalAppANR* + TotalAppCrash* | ~2 KB |
| `histogram` | ScreenBrightness / SignalStrength / WifiSignalStrength / BluetoothState / DataConnection maps | ~5 KB |
| `all` | 完整 Checkin（保留兼容，不推荐默认调用） | 100–600 KB |

`topN`（默认 20）对列表类 section 生效；wakelocks/sync/network/power/cpu/wakeups 还返回 `*Total` 字段让 LLM 判断是否被截断。

**`query_wakelocks` 新增 topN + minDurationMs**（原 T952K userspace 658 条 / 192 KB 全返回）：
- `topN`（默认 20）：每类 wakelock 最多返回 N 条
- `minDurationMs`：按总 Duration 下限过滤短时 wakelock
- 额外返回 `userspaceTotal` / `kernelTotal` 让 LLM 判断是否被截断

**`query_app_stats` / `query_histogram` / `query_wakeup_reasons` / `query_sync_tasks` 维持现状**：前两者已有 topN 过滤；后两者实测最大 9 条 / 16 条，数据量天然可控。

**推荐多轮工作流（系统级分析）**：
1. 第 1 轮：`query_system_stats section=overview` → 拿到全局聚合指标（~5 KB）
2. 第 2 轮：发现 `aggKernelWakelocks.Duration` 异常 → `query_system_stats section=wakelocks, topN=20`
3. 第 3 轮：发现某 wakelock 名字可疑 → `query_wakelocks kind=userspace, minDurationMs=60000` 深挖
4. 第 4 轮：怀疑网络耗电 → `query_system_stats section=network` 看流量大户

### 5.2 新增 Resources（4 个）
对应 4 个段的完整 JSON，方便 AI 客户端按需拉取超大原始数据。

| URI 模板 | 内容 | MIME |
|---|---|---|
| `bugreport://{id}/power` | `*power.Summary` 完整 JSON | application/json |
| `bugreport://{id}/alarms` | `*alarm.Summary` 完整 JSON | application/json |
| `bugreport://{id}/activity` | `*activity.Summary` 完整 JSON | application/json |
| `bugreport://{id}/procstats` | `*procstats.Summary` 完整 JSON | application/json |

### 5.3 不新增 Prompt
现有 2 个 prompt（`battery_root_cause` / `battery_ab_report`，`battery_health_report` 已于 commit 8a271c6 移除）是模板，AI 客户端可自行把 `query_power` 等结果作为 `metrics` 参数传入，无需新增 prompt。

### 5.4 report_index 兼容
4 个新 Tool 全部支持 `report_index` 参数，复用现有 `reportIndexFromReq` helper（mcp.go:216），A/B 对比时可访问 B 报告。

---

## 6. 解析器实现要点

### 6.1 通用抽取框架
在 `bugreportutils/bugreportutils.go` 新增通用段抽取函数（仿 `ExtractBatterystatsCheckin`）：

```go
// ExtractServiceDump 抽取指定 dumpsys 服务的完整段原文。service 参数
// 不含 "DUMP OF SERVICE" 前缀（如 "power" / "alarm" / "activity"）。
// 支持带优先级前缀（CRITICAL / HIGH / NORMAL）的段标记。
// 返回段正文（不含段标记行），未找到返回 ""。
func ExtractServiceDump(input, service string) string {
    inSection := false
    var lines []string
    for _, line := range strings.Split(input, "\n") {
        if m, result := historianutils.SubexpNames(historianutils.ServiceDumpRE, line); m {
            inSection = (result["service"] == service)
            continue
        }
        if inSection {
            lines = append(lines, line)
        }
    }
    return strings.Join(lines, "\n")
}
```

### 6.2 power 解析器
`analyzer/power/power.go` 的 `Parse(raw string) (*Summary, error)`：

1. 调 `bugreportutils.ExtractServiceDump(raw, "power")` 取段正文
2. 逐行匹配关键字段：
   - `mWakefulness=(\w+)` → Wakefulness
   - `mIsPowered=(\w+)` → IsPowered
   - `mBatteryLevel=(\d+)` → BatteryLevel
   - `mDeviceIdleMode=(\w+)` → DeviceIdleMode
   - `mLastWakeTime=(\d+)` → LastWakeTime
   - `mLastSleepReason=(\w+)` → LastSleepReason
3. 进入 `Wake Locks: size=N` 子段，每行解析一条 wakelock：
   ```
   PARTIAL_WAKE_LOCK 'AudioIn' ACQ=-15m12s377ms LONG (uid=1041 ws=...)
   ```
   正则：`(?P<level>\w+_WAKE_LOCK)\s+'(?P<name>[^']+)'\s+ACQ=(?P<acq>-[\d\w]+)(?P<long>\s+LONG)?\s+\(uid=(?P<uid>\d+)(?:\s+pid=(?P<pid>\d+))?\s+ws=(?P<ws>[^)]+)\)`
4. `ACQ=-15m12s377ms` 解析为毫秒：写 `parseDurationMs(s)` helper，正则 `-(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?(?:(\d+)ms)?`
5. `Suspend Blockers: size=N` 子段类似处理
6. `Battery saving stats:` 子段含 `Drain stats:` 表格，按行解析

### 6.3 alarm 解析器
`analyzer/alarm/alarm.go` 的 `Parse(raw string) (*Summary, error)`：

1. 抽段：`ExtractServiceDump(raw, "alarm")`
2. 关键行匹配：
   - `nowRTC=(\d+)` → NowRTC
   - `RuntimeStarted=(.+)` → RuntimeStartedISO
   - `(\d+) pending alarms:` → PendingAlarms
3. 进入 pending alarms 列表，每条 alarm 是两行：
   ```
   ELAPSED_WAKEUP #1: Alarm{...}
     tag=*walarm*:WriteBufferAlarm
     type=ELAPSED_WAKEUP origWhen=+9s809ms window=+7s500ms repeatInterval=0 count=0 flags=0x8
     whenElapsed=+9s809ms maxWhenElapsed=+17s309ms
     listener=android.app.AlarmManager$ListenerWrapper@a7d26b5
   ```
   正则匹配 `(?P<type>\w+) #(?P<seq>\d+):` 开启一条记录，`tag=(?P<tag>\S+)` / `type=(?P<t>\w+) origWhen=(?P<ow>\S+) window=(?P<w>\S+) repeatInterval=(?P<ri>\d+)` / `operation=PendingIntent{... (?P<pkg>\S+) ...}` 抽字段
4. `TopAlarms` 在解析完所有 alarm 后按 `repeatInterval > 0` 优先 + 序号升序排，取前 N（默认 20）

### 6.4 dumpsysactivity 解析器
`analyzer/dumpsysactivity/activity.go` 的 `Parse(raw string) (*Summary, error)`：

1. 抽段：`ExtractServiceDump(raw, "activity")`
2. 用 `activitySubsectionRE` 切分子段，记录每个子段的起止行号
3. 仅解析 4 个子段：
   - `lastanr` → `parseLastANR(lines)` → `[]ANRRecord`
   - `lmk` → `parseLMK(lines)` → `[]LMKKill`
   - `exit-info` → `parseProcessExits(lines)` → `[]ProcessExit`
   - `processes` → `parseRunningProcesses(lines)` → `[]RunningProcess`
4. 每个子段解析器单独函数，独立测试
5. `LastANR` 的 `FullText` 截断到 4KB（防止单条 ANR trace 过大撑爆 token）
6. `Total persistent processes: (\d+)` 单独匹配

### 6.5 procstats 解析器
`analyzer/procstats/procstats.go` 的 `Parse(raw string) (*Summary, error)`：

1. 抽段：`ExtractServiceDump(raw, "procstats")`
2. `COMMITTED STATS FROM (.+):` → CommittedFrom
3. 每个进程块以 `^  \* (?P<pkg>\S+) / (?P<uid>\S+) / (?P<ver>\S+):` 开头
4. 进程块内每行匹配：
   ```
            TOTAL: 99% (0,00-0,00-0,00/0,00-0,00-0,00/474MB-446MB-474MB over 2)
       Bnd Fgs: 64%
       Persistent: 35% (0,00-0,00-0,00/0,00-0,00-0,00/474MB-446MB-474MB over 2)
   ```
   正则：`(?P<label>[A-Za-z ]+):\s+(?P<pct>[\d,]+)%?(?:\s+\((?P<cpu>[\d,]+-[\d,]+-[\d,]+)/(?P<rss>[\d.]+[MG]B-[\d.]+[MG]B-[\d.]+[MG]B)\s+over\s+(?P<samples>\d+)\))?`
5. RSS 字符串（如 `474MB-446MB-474MB`）用 helper `parseRSSRange(s)` 转 KB 三元组
6. `topN` 参数：默认按 `Total.Percent` 降序取前 20

---

## 7. MCP 层实现要点

### 7.1 注册位置
`cmd/battery-historian/mcp.go` 的 `registerMCPTools`（L140）末尾追加 4 个 `s.AddTool`，`registerMCPResources`（L536）末尾追加 4 个 `s.AddResourceTemplate`。

### 7.2 handler 模板
4 个 query handler 模式一致，以 `query_power` 为例：

```go
func powerHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    r, err := resultForID(req) // 复用现有 helper，已支持 report_index
    if err != nil {
        return mcp.NewToolResultError(err.Error()), nil
    }
    if r.PowerSummary == nil {
        return mcp.NewToolResultError("power section not found in bugreport"), nil
    }
    return toolResultJSON(r.PowerSummary)
}
```

### 7.3 query_activity 的 kind 过滤
```go
func activityHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    r, err := resultForID(req)
    if err != nil { return mcp.NewToolResultError(err.Error()), nil }
    if r.ActivityStats == nil {
        return mcp.NewToolResultError("activity section not found in bugreport"), nil
    }
    kind := argString(req, "kind") // "" | "anr" | "lmk" | "exits" | "running"
    if kind == "" || kind == "all" {
        return toolResultJSON(r.ActivityStats)
    }
    // 按 kind 返回子集
    out := map[string]any{}
    switch kind {
    case "anr":     out["lastANR"] = r.ActivityStats.LastANR
    case "lmk":     out["lmkKills"] = r.ActivityStats.LMKKills
    case "exits":   out["processExits"] = r.ActivityStats.ProcessExits
    case "running": out["runningProcesses"] = r.ActivityStats.RunningProcesses
    }
    return toolResultJSON(out)
}
```

### 7.4 query_alarms / query_procstats 的 topN
- `query_alarms`：默认返回全部 pending alarms，额外 `topAlarms` 字段含 Top-20（按 repeatInterval 降序 + seq 升序）
- `query_procstats`：默认返回全部进程，`topN` 参数控制返回条数（默认 20，按 `Total.Percent` 降序）

### 7.5 Resource handler
4 个 resource handler 模式一致，以 `power` 为例：

```go
func powerResourceHandler(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
    r, err := primaryResult(req)
    if err != nil { return nil, err }
    if r.PowerSummary == nil {
        return nil, fmt.Errorf("power section not found in bugreport")
    }
    return jsonResource(req.Params.URI, r.PowerSummary)
}
```

---

## 8. 非功能设计

### 8.1 性能
- 4 个段解析全部纯 Go 正则，无外部依赖
- 实测 bugreport 文本 73MB，4 段合计抽取 + 解析预计 <500ms（单段 <150ms）
- 不阻塞主解析路径：在 `analysisResults()` 末尾顺序执行，单段失败不中断

### 8.2 安全（NFR-04 继承）
- 4 个新 Tool/Resource 全部走 `resultForID` / `primaryResult`，复用现有 id 校验
- `report_index` 复用现有 `reportIndexFromReq`，越界拒绝
- 段原文不直接返回（避免超大 bugreport 把整段塞给 MCP 客户端），只返回结构化 JSON
- `ANRRecord.FullText` 截断到 4KB

### 8.3 兼容性
- `ServiceDumpRE` 升级向后兼容（`service` group 名不变，仅加可选优先级前缀）
- `extractSensorInfo` 无需改动
- 现有 9 tools / 6 resources / 3 prompts 全部不受影响
- `AnalysisResult` 新增字段为指针类型，nil 零值兼容老调用方

### 8.4 可测试性
- 每个 package 提供 `Parse(raw string) (*Summary, error)` 入口，可独立单测
- 测试 fixture 放 `analyzer/{power,alarm,activity,procstats}/testdata/` 下，从真实 bugreport 抽取小段
- 解析器对异常行容错：未匹配行跳过，不报错

---

## 9. 文件清单

### 9.1 新增文件
| 文件 | 职责 |
|---|---|
| `analyzer/power/power.go` | power 段解析器 + 数据结构 |
| `analyzer/power/power_test.go` | 单测 |
| `analyzer/power/testdata/power_sample.txt` | 测试 fixture（从真实 bugreport 抽段） |
| `analyzer/alarm/alarm.go` | alarm 段解析器 + 数据结构 |
| `analyzer/alarm/alarm_test.go` | 单测 |
| `analyzer/alarm/testdata/alarm_sample.txt` | 测试 fixture |
| `analyzer/dumpsysactivity/activity.go` | dumpsys activity 段解析器（含 4 子段） + 数据结构 |
| `analyzer/dumpsysactivity/activity_test.go` | 单测 |
| `analyzer/dumpsysactivity/testdata/{lastanr,lmk,exit-info,processes}_sample.txt` | 4 个子段 fixture |
| `analyzer/procstats/procstats.go` | procstats 段解析器 + 数据结构 |
| `analyzer/procstats/procstats_test.go` | 单测 |
| `analyzer/procstats/testdata/procstats_sample.txt` | 测试 fixture |

### 9.2 修改文件
| 文件 | 修改点 |
|---|---|
| `historianutils/historianutils.go:32` | `ServiceDumpRE` 支持可选优先级前缀（CRITICAL/HIGH/NORMAL） |
| `bugreportutils/bugreportutils.go` | 新增 `ExtractServiceDump(input, service)` 通用函数 + `activitySubsectionRE` 正则 |
| `analyzer/analyzer.go` | `ParsedData` 新增 `bugReportContents string` 字段，在 `parseBugReport` 入口保存 |
| `analyzer/core.go` | `AnalysisResult` 新增 4 个字段；`analysisResults()` 末尾调用 4 个解析器 |
| `cmd/battery-historian/mcp.go` | `registerMCPTools` / `registerMCPResources` 各追加 4 个；新增 4 个 tool handler + 4 个 resource handler |

### 9.3 文档更新
| 文件 | 更新点 |
|---|---|
| `README.md` | MCP 能力表从 9/6/3 更新为 13/10/3；新增「OEM 功耗分析扩展」小节 |
| `MCP概要设计.md` | §5 能力清单表追加 4 tools + 4 resources；§3.1 AnalysisResult 契约追加 4 字段 |
| `MCP需求矩阵.md` | 新增 FR-20..FR-23 需求行 |

---

## 10. 验收标准

### 10.1 功能验收
- `go build ./...` 通过
- `go vet ./analyzer/... ./cmd/battery-historian/...` 通过
- `go test ./analyzer/...` 通过（含 4 个新 package 的单测）
- 用 `_samples/bugreport-T952K_EEA-BP2A.250605.031.A3-2026-07-13-22-30-48.txt` 跑 `analyze_bugreport`，4 个新 query_* 工具全部返回非空 JSON

### 10.2 场景验收
用真实 bugreport 验证 4 个 OEM 测试场景：

| 场景 | 验证方式 |
|---|---|
| "这个 wakelock 是谁持有的、持了多久没释放？" | `query_power` 返回 `wakeLocks[]`，含 `name` / `uid` / `acquiredAgoMs` |
| "为什么设备每 5 分钟醒一次？" | `query_alarms` 返回 `topAlarms[]`，含 `repeatIntervalMs=300000` 的条目 |
| "功耗大户是被反复拉起还是一直在前台？" | `query_activity kind=exits` 看进程退出次数；`query_procstats` 看 `Total.Percent` 与 `Top` 状态占比 |
| "应用被杀重启了几次？内存压力多大？" | `query_activity kind=lmk` 看 LMK 次数；`query_procstats` 看 RSS 与状态分布 |

### 10.3 兼容性验收
- 现有 9 tools / 6 resources / 3 prompts 行为不变
- `extractSensorInfo` 仍能正确解析 `DUMP OF SERVICE CRITICAL sensorservice:`
- 老格式 bugreport（无 CRITICAL 前缀）4 个新段也能正确抽取

---

## 11. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 不同 Android 版本 dumpsys 输出格式差异 | 解析失败 | 解析器对未匹配行容错跳过；每个 package 独立测试；fixture 覆盖至少 2 个 Android 版本 |
| activity 段过大（5 万行）解析慢 | 性能 | 只切 4 个子段，不解析全段；子段定位用正则，命中即停 |
| OEM 定制 dumpsys 字段（如 MTK `sMtkIpoManagerService`） | 字段缺失 | 不依赖 OEM 定制字段，只抽 AOSP 标准字段；OEM 字段保留原文在 `Note` |
| `ServiceDumpRE` 升级影响 `extractSensorInfo` | 回归 | 升级保留 `service` group 名；单测覆盖 `CRITICAL sensorservice` |
| ANR trace 过大撑爆 token | MCP 响应过大 | `FullText` 截断 4KB |

---

## 12. 实施顺序

建议按依赖关系顺序实施（每步可独立验证）：

1. **基础设施**（1 个 commit）
   - `historianutils.ServiceDumpRE` 升级 + 单测
   - `bugreportutils.ExtractServiceDump` + 单测
   - `bugreportutils.activitySubsectionRE`

2. **4 个解析器 package**（4 个 commit，可并行）
   - `analyzer/power/` + 单测 + fixture
   - `analyzer/alarm/` + 单测 + fixture
   - `analyzer/activity/` + 单测 + fixture
   - `analyzer/procstats/` + 单测 + fixture

3. **Core 集成**（1 个 commit）
   - `ParsedData.bugReportContents` 字段
   - `AnalysisResult` 4 个新字段
   - `analysisResults()` 末尾调用 4 个解析器
   - 集成测试：用真实 bugreport 验证 4 字段非 nil

4. **MCP 注册**（1 个 commit）
   - `registerMCPTools` 追加 4 个
   - `registerMCPResources` 追加 4 个
   - 8 个 handler 函数
   - 端到端测试：`analyze_bugreport` + 4 个 `query_*`

5. **文档更新**（1 个 commit）
   - README / MCP概要设计 / MCP需求矩阵 同步

---

## 13. 不在本次范围

以下能力留待后续阶段（P1/P2），本次不实施：

- **P1 网络栈归因**：`connectivity` / `wifi` / `bluetooth` / `netstats` 段
- **P1 传感器归因**：扩展现有 `extractSensorInfo`，暴露 sensor 列表给 MCP；`location` 段 GPS 请求方
- **P2 温度与频率**：`thermalservice` / `cpufreq` / `cpuinfo`
- **P3 OEM 自定义服务**：`tcl_power` / `power_hal_mgr_service` 等厂商自有 dumpsys 段，建议做成可配置 section 名 + 解析规则
- **logcat 段的 `Killing` / `ANR` 事件聚合**：本设计只解析 dumpsys 段，不解析 logcat；logcat 里的进程被杀事件可在后续单独做

---

## 14. 修订记录

| 日期 | 修订内容 |
|---|---|
| 2026-07-19 | 初版。基于 `_samples/bugreport-T952K_EEA-BP2A.250605.031.A3-2026-07-13-22-30-48.txt`（73MB，Android 14，MTK 平台）核对真实格式后起草。 |
| 2026-07-19 | Android 17（reportVersion 36）兼容性优化。基于 `_samples/bugreport-T807D_EEA-CP2A.260306.002-2026-06-12-03-42-34.txt` 真机数据 + WSL 中 Android 17 AOSP 源码 review，修正 5 个解析器：(1) `power.wakeLockRE` 重写以支持 `isFrozen` / `isAttributedUidCached` / `powerGroupId` 新字段；(2) `power.drainStatFullRE/ContRE` 的 `mah/pct/mahh` 从 `[\d,]+` 改为 `[\d.,]+` 兼容 en_US locale；(3) `alarm` 新增 `AggregateTopAlarms` 字段，对应 AOSP dumpsys alarm "Top Alarms:" 段（按 aggregateTime 降序，区别于 pending 队列按 repeatInterval 排序的 `TopAlarms`）；(4) `procstats.stateLineRE` 的 `pct` 改为 `[\d.,]+`，`rssRE` 支持可选单位（KB/MB/GB，<1MB 无单位）；(5) `activity.exitReasonRE` 的 `reason/subreason` 改为 `.*?` 处理嵌套括号（如 `APP CRASH(EXCEPTION)`），`exitRssRE/procRssRE` 支持可选单位。`checkinparse` 同步升级：`maxParseReportVersion` 从 21 提到 36，`parseAppStateTime` 新增 rv36 分支按 Android 17 字段顺序（top/fs/fore/back/topSleep/heavyWeight/cached），`parseAppWakelock` 新增 rv36 分支解析 4 类 wakelock（新增 `bp` 背景_partial 段，4×6=24 字段），`parseSection` switch 显式容忍 9 个新 section（rpm/gcf/ctf/fgs/awl/jbc/jbd/wmc/wmct）。`analyzer/p4_smoke_test.go` 改为参数化遍历 `_samples/bugreport-*.txt`，5 个样本（含 T807D Android 17）全部端到端冲烟通过。 |
| 2026-07-19 | 4 个 query_* 工具精细化参数改造（v0.3.1）。真机数据显示 dumpsys activity 单段数据量巨大（T807D exits=624/running=83，vegas_g exits=1740/running=65），原 `kind=all` 默认全返回会导致 LLM 单次 tool 返回 token 爆炸。改造为"LLM 多轮 + tool 精细化参数"模式：4 个 tool 统一支持 `topN`（默认 20）；`query_activity` 新增 `package` / `reason` / `minRssKB` / `oomAdjMax` 4 个过滤参数，单 kind 模式额外返回 `processExitsTotal` / `runningTotal` 让 LLM 判断是否被截断；`query_alarms` 新增 `package` / `wakeupOnly`；`query_procstats` 新增 `package` / `minPercent`；`query_power` 新增 `minHeldMs`。`mcp.go` 新增 `argBool` / `containsFold` helper + `filterAlarms` / `filterProcessExits` / `filterRunningProcesses` / `truncActivitySlice` 4 个过滤函数。5 个 bugreport 冲烟测试全通过。 |
| 2026-07-19 | 历史工具精细化参数改造（v0.3.2）。实测 T952K `r.Checkin` 全量 JSON 达 563 KB（含 658 条 userspace wakelock / 192 KB），原 `query_system_stats` 直接返回整个 proto 会撑爆 LLM token。`query_system_stats` 拆分为 `section` 参数（默认 `overview`，可选 wakelocks/sync/network/power/cpu/wakeups/anr/histogram/all），`overview` 只返回 metadata + system summary + Agg* 聚合字段（~5 KB），其他 section 按主题深挖且支持 `topN`（默认 20）截断。`query_wakelocks` 新增 `topN` + `minDurationMs`，避免 T952K 658 条 userspace wakelock 全返回。新增 `truncAggregatedActivity` / `truncAggregatedNetwork` / `truncAggregatedPower` / `truncAggregatedRate` / `truncAggregatedCPU` 5 个泛型截断 helper + `filterAndTruncWakelocks` 过滤函数。`query_app_stats`（已有 topN）/ `query_histogram`（数据量小）/ `query_wakeup_reasons`（实测 ≤9 条）/ `query_sync_tasks`（实测 ≤16 条）维持现状。5 个 bugreport 冲烟测试全通过。 |
