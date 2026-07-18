# Battery Historian MCP Server (Phase 1, Form A)

A **standalone MCP server** that exposes Battery Historian's battery analysis to
AI assistants (Claude Desktop / WorkBuddy / Cursor) as MCP Tools, Resources, and
Prompts. It proxies a *running* Historian HTTP service, so it does **not** import
the legacy `battery-historian` packages and is independently buildable.

## Architecture
```
AI Client ──MCP (stdio / streamable-HTTP)──▶ this server ──HTTP POST /historian/──▶ Battery Historian
```
- Tools: `analyze_bugreport`, `compare_bugreports`, `query_system_stats`, `query_app_stats`, `query_histogram`
- Resources: `bugreport://{id}/system_stats`, `bugreport://{id}/app_stats/{uid}`, `bugreport://{id}/raw_checkin`
- Prompts: `battery_root_cause`, `battery_ab_report`

## Build & run
```bash
cd mcp-server
# Requires Go 1.25.5+ (mcp-go v0.56.0 pins the toolchain).
# If the default module proxy is unreachable, use a mirror first:
#   export GOPROXY=https://goproxy.cn
go mod tidy
go build -o battery-historian-mcp .

# 1) Start the official Historian service (needs its own setup):
#    go run cmd/battery-historian/battery-historian.go --port 9999

# 2) Start this MCP server (stdio, default):
./battery-historian-mcp --historian-url http://localhost:9999

# Or serve over HTTP (streamable):
./battery-historian-mcp --transport http --addr :8080 --historian-url http://localhost:9999
```

## Notes / limitations (see MCP需求矩阵.md §3.3)
- Form A reuses the official HTTP handler, which always triggers the Historian
  plot (`generateHistorianPlot`, Python 2.7). If Python is absent the plot
  goroutine errors but the **structured stats still return** — acceptable.
- System stats are surfaced from the `batteryStats` proto + `histogramStats`
  returned by the HTTP API. The pre-aggregated `aggregated.Checkin` (per-report
  wakelock totals) is only reconstructable by importing the `aggregated` package
  (Phase 2 / Form B). The A/B `CombinedCheckin` is fully available via
  `compare_bugreports`.
- Results are cached in an in-memory LRU (default 20, `--max-entries`); no
  persistence, cleared on restart.
