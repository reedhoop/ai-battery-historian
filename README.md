# Battery Historian (ai-battery-historian fork)

> 本仓库是 Google [battery-historian](https://github.com/google/battery-historian) 的 fork，在原版基础上做了以下增强：
>
> - **Go module 化**：module 路径 `github.com/reedhoop/ai-battery-historian`，go 1.25.5，告别 GOPATH。
> - **新版 Android bugreport 兼容**：修复 checkin version 36（Android 11+）解析失败问题（`parseControllerData` 浮点格式整数兜底）。
> - **国内 CDN 镜像**：模板中 jQuery / Bootstrap 等前端资源改用 bootcdn.net，避免国外 CDN 加载失败。
> - **MCP 协议支持**：通过 [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) 暴露 Tool / Resource / Prompt 三类原语，让 AI 助手（Claude / WorkBuddy / Cursor 等）直接消费 Battery Historian 的解析能力。详见 [MCP可行性评估.md](MCP可行性评估.md) / [MCP概要设计.md](MCP概要设计.md) / [MCP需求矩阵.md](MCP需求矩阵.md)。

Battery Historian is a tool to inspect battery related information and events on an Android device running Android 5.0 Lollipop (API level 21) and later, while the device was not plugged in. It allows application developers to visualize system and application level events on a timeline with panning and zooming functionality, easily see various aggregated statistics since the device was last fully charged, and select an application and inspect the metrics that impact battery specific to the chosen application. It also allows an A/B comparison of two bugreports, highlighting differences in key battery related metrics.

## Getting Started

#### Using Docker

Install [Docker](<https://docs.docker.com/engine/installation/>).

Run the Battery Historian image. Choose a port number and replace `<port>` with
that number in the commands below:

```
docker -- run -p <port>:9999 gcr.io/android-battery-historian/stable:3.0 --port 9999
```

For Linux and Mac OS X:

* That's it, you're done! Historian will be available at
  `http://localhost:<port>`.

For Windows:

* You may have to [enable Virtualization in your
  BIOS](<http://www.itworld.com/article/2981515/virtualization/virtualbox-diagnose-and-fix-vt-xamd-v-hardware-acceleration-errors.html>).

* Once you start Docker, it should tell you the IP address of the machine it is
using. If, for example, the IP address is 123.456.78.90, Historian will be
available at `http://123.456.78.90:<port>`.

For more information about the port forwarding, see the [Docker
documentation](<https://docs.docker.com/engine/reference/run/#/expose-incoming-ports>).

#### Building from source code

> **本 fork 已 Go module 化**，不再依赖 GOPATH。建议 Go 1.21+（实测 go 1.25.5）。

##### 1. 准备环境

- **Go**：1.21+（推荐 1.25.5）。安装见 <https://go.dev/doc/install>。
- **Git**：<https://git-scm.com/downloads>。
- **Java**：用于 Closure 编译器打包前端 JS。<http://www.oracle.com/technetwork/java/javase/downloads/index.html>。
- **Python**（可选）：仅生成 Historian v2 时间轴图表 HTML 需要。
  - 旧版 fork 时期要求 Python 2.7，但**核心耗电统计不依赖 Python**。
  - 走 MCP 路径（见下文 §MCP）或 `--mcp` 模式时可彻底跳过 Python。
  - 若仍想生成 legacy Format:1 报告的 Historian 风格图表，需要 Python 3 + 已迁移到 py3 的 `scripts/historian.py`（注：Python 3 迁移未在 P3 范围内完成，默认 `scripts/historian.py` 仍是 Python 2.7 语法）。

##### 2. 拉取代码

```
$ git clone https://github.com/reedhoop/ai-battery-historian.git
$ cd ai-battery-historian
```

##### 3. 编译前端 JS（一次性）

```
$ go run setup.go
```

这会调用 Closure 编译器把 `js/` 下的源文件打包为 `compiled/` 下的 `historian.js` 等产物。

##### 4. 运行 HTTP 服务（Web UI 模式）

```
# 默认端口 9999
$ go run cmd/battery-historian/battery-historian.go --port 9999

# 或编译为二进制
$ go build -o battery-historian ./cmd/battery-historian
$ ./battery-historian --port 9999
```

打开 <http://localhost:9999> 上传 bugreport 即可分析。

> **Windows + 国内网络建议**：首次构建需拉取 `golang/protobuf` / `mark3labs/mcp-go` 等依赖，可设置国内代理加速：
> ```powershell
> $env:GOPROXY = "https://goproxy.cn,direct"
> $env:GOSUMDB = "sum.golang.org"
> ```

##### 5. 新版 Android bugreport 兼容性说明

本 fork 修复了 checkin version 36（Android 11+）bugreport 解析问题：原版 `parseControllerData` 严格按整数解析，遇到浮点格式整数字段（如 `0.0`）会失败，导致 "Could not parse aggregated battery stats" 错误。本 fork 改为「先尝试整数转换，失败再按 float 解析后转 int64」，可正确解析 2026 年新版 Android bugreport。

> Android 17 新增 history key 兼容：`Mrc`（modem rail charge）、`Wrc`（wifi rail charge）、`Ud`（USB data link）、`Eds`（display state changed）、`Esc`（state change）均已支持，不再打印 "Unknown history key" 警告。
>
> 已知遗留：`Chtp`（charging type）仍会打印 "Unknown history key" 警告，但不影响主流程解析与时间轴绘制。


#### How to take a bug report

To take a bug report from your Android device, you will need to enable USB debugging under `Settings > System > Developer Options`. On Android 4.2 and higher, the Developer options screen is hidden by default. You can enable this by following the instructions [here](<http://developer.android.com/tools/help/adb.html#Enabling>).

To obtain a bug report from your development device running Android 7.0 and
higher:

```
$ adb bugreport bugreport.zip
```

For devices 6.0 and lower:

```
$ adb bugreport > bugreport.txt
```

### Start analyzing!

You are all set now. Run `historian` and visit <http://localhost:9999> and
upload the `bugreport.txt` file to start analyzing.

## MCP 模式（AI 助手接入）

本 fork 通过 [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) 把 Battery Historian 的解析能力暴露为 [MCP (Model Context Protocol)](https://modelcontextprotocol.io) 原语，让 AI 助手（Claude / WorkBuddy / Cursor 等）直接消费。提供两套并行实现，Form B 是 Form A 的功能超集。

### Form B：原生嵌入（推荐）

`cmd/battery-historian` 加 `--mcp` 标志启动内嵌 MCP server，进程内直调 `analyzer.Analyze` / `analyzer.Compare`，**完全不依赖 Python**。

```
# stdio 传输（默认，对接 Claude Desktop / WorkBuddy 本地客户端）
$ go run cmd/battery-historian/battery-historian.go --mcp

# streamable HTTP 传输（对接远程客户端）
$ go run cmd/battery-historian/battery-historian.go --mcp --mcp_transport=http --mcp_addr=:8080

# 可选：同时生成 Historian 风格图表（需 Python 3 + 已迁移的 historian.py）
$ go run cmd/battery-historian/battery-historian.go --mcp --mcp_with_chart
```

**已注册能力（v0.3.0）**：12 tools / 9 resources / 2 prompts。

> ⚠️ 自定义电池**健康度评分**功能（原 P3-C：`query_health` tool / `bugreport://{id}/health` resource / `battery_health_report` prompt）已于 commit `8a271c6` 整体移除，对问题分析意义不大；原生「健康度直方图」（`query_histogram` 的 `HistogramStats`）保留。下表以删除线标注已移除项。

| 类别 | 名称 | 说明 |
|---|---|---|
| Tool | `analyze_bugreport` | 解析单个 bugreport（path 或 base64 content） |
| Tool | `compare_bugreports` | A/B 差分两个 bugreport |
| Tool | `query_system_stats` | 系统级聚合指标（`aggregated.Checkin`） |
| Tool | `query_app_stats` | 应用级耗电（Top-N / 指定 uid） |
| Tool | `query_histogram` | 健康度直方图指标 |
| Tool | `query_wakelocks` | Userspace/Kernel wakelock 明细（支持 `kind` 过滤） |
| Tool | `query_wakeup_reasons` | 唤醒原因（接入 `wakeupreason.FindSubsystem` 解码） |
| Tool | `query_sync_tasks` | 同步任务频率与时长 |
| Tool | ~~`query_health`~~ **已移除（commit 8a271c6）** | 原电池健康度评分（6 维度加权） |
| Tool | `query_power` | dumpsys power 段：实时 wakelock 快照 + suspend blockers + 省电 drain |
| Tool | `query_alarms` | dumpsys alarm 段：pending 队列 + Top-N 重复 alarm 排名（支持 `topN`） |
| Tool | `query_activity` | dumpsys activity 段：ANR / LMK / 进程退出 / 运行中进程（支持 `kind` 过滤） |
| Tool | `query_procstats` | dumpsys procstats 段：进程状态时长 + RSS 内存（支持 `topN`） |
| Resource | `bugreport://{id}/system_stats` | 系统级全量指标 JSON |
| Resource | `bugreport://{id}/app_stats/{uid}` | 单应用明细 JSON |
| Resource | `bugreport://{id}/raw_checkin` | 原始 batterystats proto → JSON |
| Resource | `bugreport://{id}/chart` | Historian plot HTML 或 V2 SVG fallback |
| Resource | `bugreport://{id}/report` | 自包含分析报告 HTML |
| Resource | ~~`bugreport://{id}/health`~~ **已移除（commit 8a271c6）** | 原健康度评分 JSON |
| Resource | `bugreport://{id}/power` | dumpsys power 段完整 JSON |
| Resource | `bugreport://{id}/alarms` | dumpsys alarm 段完整 JSON |
| Resource | `bugreport://{id}/activity` | dumpsys activity 段完整 JSON |
| Resource | `bugreport://{id}/procstats` | dumpsys procstats 段完整 JSON |
| Prompt | `battery_root_cause` | 根因分析提示词模板 |
| Prompt | `battery_ab_report` | A/B 报告提示词模板 |
| Prompt | ~~`battery_health_report`~~ **已移除（commit 8a271c6）** | 原健康度改进建议提示词模板 |

> 全部 10 个 `query_*` 工具支持 `report_index` 参数（默认 `0`=A 报告，`1`=B 报告），用于在 `compare_bugreports` 结果中访问第二份报告。
>
> **OEM 功耗分析扩展（P4）**：`query_power` / `query_alarms` / `query_activity` / `query_procstats` 解析 bugreport 中其他 dumpsys 段，与 `dumpsys batterystats`（`query_wakelocks` 等基于此）互补，构成完整的「唤醒源归因 + 功耗大户行为佐证」闭环。详见 `OEM功耗分析扩展设计.md`。

### Form A：独立 MCP 进程（HTTP 代理）

`mcp-server/` 是独立 Go module，通过 HTTP 代理运行中的 Historian 服务（`POST /historian/`）。能力是 Form B 子集（5 tools / 3 resources / 2 prompts），适合「无主仓改动的隔离部署」场景。

```
$ cd mcp-server
$ go run . --historian-url=http://localhost:9999
```

### 安全加固（NFR-04）

- **文件大小守卫**：单文件上限 100MB。
- **base64 DoS 防护**：编码长度预检（`maxEncodedLen = maxFileSize*4/3 + 4`），解码前拒绝超大输入。
- **路径沙箱**：`filepath.Clean` + `fi.Mode().IsRegular()` 守卫，拒绝目录 / 设备文件 / socket / 管道。
- **prompt 注入防护**：三个 prompt handler 的用户参数用 `<user_data>` 标签包装，并前置安全声明。

### 详细文档

- [MCP可行性评估.md](MCP可行性评估.md)：设计阶段可行性评估 + 现状对照。
- [MCP概要设计.md](MCP概要设计.md)：架构 / Core 契约 / 能力清单 / 非功能设计。
- [MCP需求矩阵.md](MCP需求矩阵.md)：FR-01..FR-19 + NFR-01..NFR-06 需求清单与实施状态。

## Screenshots

##### Timeline:

![Timeline](/screenshots/timeline.png "Timeline Visualization")

##### System stats:

![System](/screenshots/system.png "Aggregated System statistics since the device was last fully charged")

##### App stats:

![App](/screenshots/app.png "Application specific statistics")

## Advanced

To reset aggregated battery stats and history:

```
adb shell dumpsys batterystats --reset
```

##### Wakelock analysis

By default, Android does not record timestamps for application-specific
userspace wakelock transitions even though aggregate statistics are maintained
on a running basis. If you want Historian to display detailed information about
each individual wakelock on the timeline, you should enable full wakelock
reporting using the following command before starting your experiment:

```
adb shell dumpsys batterystats --enable full-wake-history
```

Note that by enabling full wakelock reporting the battery history log overflows
in a few hours. Use this option for short test runs (3-4 hrs).

##### Kernel trace analysis

To generate a trace file which logs kernel wakeup source and kernel wakelock
activities:

First, connect the device to the desktop/laptop and enable kernel trace logging:

```
$ adb root
$ adb shell

# Set the events to trace.
$ echo "power:wakeup_source_activate" >> /d/tracing/set_event
$ echo "power:wakeup_source_deactivate" >> /d/tracing/set_event

# The default trace size for most devices is 1MB, which is relatively low and might cause the logs to overflow.
# 8MB to 10MB should be a decent size for 5-6 hours of logging.

$ echo 8192 > /d/tracing/buffer_size_kb

$ echo 1 > /d/tracing/tracing_on
```

Then, use the device for intended test case.

Finally, extract the logs:

```
$ echo 0 > /d/tracing/tracing_on
$ adb pull /d/tracing/trace <some path>

# Take a bug report at this time.
$ adb bugreport > bugreport.txt
```

Note:

Historian plots and relates events in real time (PST or UTC), whereas kernel
trace files logs events in jiffies (seconds since boot time). In order to relate
these events there is a script which approximates the jiffies to utc time. The
script reads the UTC times logged in the dmesg when the system suspends and
resumes. The scope of the script is limited to the amount of timestamps present
in the dmesg. Since the script uses the dmesg log when the system suspends,
there are different scripts for each device, with the only difference being
the device-specific dmesg log it tries to find. These scripts have been
integrated into the Battery Historian tool itself.

##### Power monitor analysis

Lines in power monitor files should have one of the following formats, and the
format should be consistent throughout the entire file:

```
<timestamp in epoch seconds, with a fractional component> <amps> <optional_volts>
```

OR

```
<timestamp in epoch milliseconds> <milliamps> <optional_millivolts>
```

Entries from the power monitor file will be overlaid on top of the timeline
plot.

To ensure the power monitor and bug report timelines are somewhat aligned,
please reset the batterystats before running any power monitor logging:

```
adb shell dumpsys batterystats --reset
```

And take a bug report soon after stopping power monitor logging.

If using a Monsoon:

Download the AOSP Monsoon Python script from <https://android.googlesource.com/platform/cts/+/master/tools/utils/monsoon.py>

```
# Run the script.
$ monsoon.py --serialno 2294 --hz 1 --samples 100000 -timestamp | tee monsoon.out

# ...let device run a while...

$ stop monsoon.py
```

##### Modifying the proto files

If you want to modify the proto files (pb/\*/\*.proto), first download the
additional tools necessary:

Install the standard C++ implementation of protocol buffers from <https://github.com/google/protobuf/blob/master/src/README.md>

Download the Go proto compiler:

```
$ go get -u github.com/golang/protobuf/protoc-gen-go
```

The compiler plugin, protoc-gen-go, will be installed in $GOBIN, which must be
in your $PATH for the protocol compiler, protoc, to find it.

Make your changes to the proto files.

Finally, regenerate the compiled Go proto output files using `regen_proto.sh`.

##### Other command line tools

```
# System stats
$ go run cmd/checkin-parse/local_checkin_parse.go --input=bugreport.txt

# Timeline analysis
$ go run cmd/history-parse/local_history_parse.go --summary=totalTime --input=bugreport.txt

# Diff two bug reports
$ go run cmd/checkin-delta/local_checkin_delta.go --input=bugreport_1.txt,bugreport_2.txt
```


## Support

- G+ Community (Discussion Thread: Battery Historian): https://plus.google.com/b/108967384991768947849/communities/114791428968349268860

If you've found an error in this project, please file an issue:
<https://github.com/google/battery-historian/issues>

## License

Copyright 2016 Google, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  <http://www.apache.org/licenses/LICENSE-2.0>

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.  See the
License for the specific language governing permissions and limitations under
the License.
