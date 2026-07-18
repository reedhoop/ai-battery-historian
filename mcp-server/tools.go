package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func jsonMarshalIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

// registerTools wires the FR-01..FR-05 tool set onto the MCP server.
func registerTools(s *server.MCPServer) {
	// FR-01 analyze_bugreport
	s.AddTool(mcp.NewTool("analyze_bugreport",
		mcp.WithDescription("Parse an Android bugreport and return structured battery analysis. Returns a result id used by query_* tools / resources."),
		mcp.WithString("path", mcp.Description("Local path to the bugreport file.")),
		mcp.WithString("content", mcp.Description("Base64-encoded bugreport contents (alternative to path).")),
	), analyzeHandler)

	// FR-02 compare_bugreports
	s.AddTool(mcp.NewTool("compare_bugreports",
		mcp.WithDescription("Diff two bugreports and return per-metric deltas (A/B comparison)."),
		mcp.WithString("path_a", mcp.Description("Local path to the first bugreport.")),
		mcp.WithString("content_a", mcp.Description("Base64-encoded first bugreport (alternative to path_a).")),
		mcp.WithString("path_b", mcp.Description("Local path to the second bugreport.")),
		mcp.WithString("content_b", mcp.Description("Base64-encoded second bugreport (alternative to path_b).")),
	), compareHandler)

	// FR-03 query_system_stats
	s.AddTool(mcp.NewTool("query_system_stats",
		mcp.WithDescription("Return system-level battery stats for a previously analyzed report (device capacity, histogram, raw batterystats proto, power estimates)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Result id from analyze_bugreport.")),
	), systemStatsHandler)

	// FR-04 query_app_stats
	s.AddTool(mcp.NewTool("query_app_stats",
		mcp.WithDescription("Return per-app power stats. Defaults to Top-N by device power prediction; pass uid to get one app."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Result id from analyze_bugreport.")),
		mcp.WithNumber("uid", mcp.Description("App uid to filter for (optional).")),
		mcp.WithNumber("topN", mcp.Description("Max apps to return (default 20).")),
	), appStatsHandler)

	// FR-05 query_histogram
	s.AddTool(mcp.NewTool("query_histogram",
		mcp.WithDescription("Return the histogram health metrics (screen/signal/network/bluetooth/modem percentages and per-hour rates)."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Result id from analyze_bugreport.")),
	), histogramHandler)
}

// argString / argFloat are tiny helpers over the tool request arguments.
func argString(req mcp.CallToolRequest, key string) string {
	if v, ok := req.GetArguments()[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func argFloat(req mcp.CallToolRequest, key string) (float64, bool) {
	if v, ok := req.GetArguments()[key]; ok {
		if f, ok := v.(float64); ok {
			return f, true
		}
	}
	return 0, false
}

const maxFileSize = 100 * 1024 * 1024 // 100 MB, mirrors analyzer.go:59 (NFR-04)

// resolveToTemp reads a bugreport from a local path or base64 content, applies
// the 100 MB guard (NFR-04), and writes it to a temp file. The caller owns the
// returned path and must os.Remove it.
func resolveToTemp(pathKey, contentKey string, req mcp.CallToolRequest) (string, error) {
	p := argString(req, pathKey)
	c := argString(req, contentKey)
	var data []byte
	var err error
	switch {
	case p != "":
		if fi, statErr := os.Stat(p); statErr == nil && fi.Size() > maxFileSize {
			return "", fmt.Errorf("%s exceeds 100MB limit", p)
		}
		data, err = os.ReadFile(p)
	case c != "":
		data, err = base64.StdEncoding.DecodeString(c)
		if err == nil && len(data) > maxFileSize {
			return "", fmt.Errorf("decoded %s exceeds 100MB limit", contentKey)
		}
	default:
		return "", fmt.Errorf("either '%s' or '%s' (base64) is required", pathKey, contentKey)
	}
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "br-*.txt")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		return "", err
	}
	return tmp.Name(), nil
}

func analyzeHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	tmp, err := resolveToTemp("path", "content", req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer os.Remove(tmp)

	body, err := historian.postBugreports([]string{"bugreport"}, []string{tmp})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	urc, err := parseAnalyze(body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	id := store.Put(urc)
	return toolResultJSON(buildSummary(id, urc))
}

func compareHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	fa, err := resolveToTemp("path_a", "content_a", req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer os.Remove(fa)
	fb, err := resolveToTemp("path_b", "content_b", req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	defer os.Remove(fb)

	body, err := historian.postBugreports([]string{"bugreport", "bugreport2"}, []string{fa, fb})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	urc, err := parseAnalyze(body)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	id := store.Put(urc)
	return toolResultJSON(map[string]any{
		"id":              id,
		"usingComparison": urc.UsingComparison,
		"combined":        urc.CombinedCheckin,
		"note":            "Use resources bugreport://<id>/... or query_* tools for details.",
	})
}

func systemStatsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urc, ok := store.Get(argString(req, "id"))
	if !ok {
		return mcp.NewToolResultError("unknown id (analyze first)"), nil
	}
	if len(urc.UploadResponse) == 0 {
		return mcp.NewToolResultError("empty analysis result"), nil
	}
	ur := urc.UploadResponse[0]
	return toolResultJSON(map[string]any{
		"sdkVersion":     ur.SDKVersion,
		"fileName":       ur.FileName,
		"location":       ur.Location,
		"deviceCapacity": ur.DeviceCapacity,
		"criticalError":  ur.CriticalError,
		"note":           ur.Note,
		"histogramStats": ur.HistogramStats,
		"batteryStats":   ur.BatteryStats,
		"powerEstimates": urc.CombinedCheckin.DevicePowerEstimatesCombined,
	})
}

func appStatsHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urc, ok := store.Get(argString(req, "id"))
	if !ok {
		return mcp.NewToolResultError("unknown id (analyze first)"), nil
	}
	if len(urc.UploadResponse) == 0 {
		return mcp.NewToolResultError("empty analysis result"), nil
	}
	apps := urc.UploadResponse[0].AppStats

	// Single-app lookup by uid.
	if uid, ok := argFloat(req, "uid"); ok {
		for _, a := range apps {
			var k appKey
			if json.Unmarshal(a.RawStats, &k) == nil && float64(k.UID) == uid {
				return toolResultJSON(map[string]any{
					"uid":                   k.UID,
					"name":                  k.Name,
					"devicePowerPrediction": a.DevicePowerPrediction,
					"cpuPowerPrediction":    a.CPUPowerPrediction,
					"rawStats":              a.RawStats,
				})
			}
		}
		return mcp.NewToolResultError(fmt.Sprintf("uid %v not found", int(uid))), nil
	}

	// Top-N by DevicePowerPrediction (NOT by name — presenter sorts by name only).
	sort.SliceStable(apps, func(i, j int) bool {
		return apps[i].DevicePowerPrediction > apps[j].DevicePowerPrediction
	})
	topN := 20
	if n, ok := argFloat(req, "topN"); ok && int(n) > 0 {
		topN = int(n)
	}
	if topN > len(apps) {
		topN = len(apps)
	}
	out := make([]map[string]any, 0, topN)
	for _, a := range apps[:topN] {
		var k appKey
		_ = json.Unmarshal(a.RawStats, &k)
		out = append(out, map[string]any{
			"uid":                   k.UID,
			"name":                  k.Name,
			"devicePowerPrediction": a.DevicePowerPrediction,
			"cpuPowerPrediction":    a.CPUPowerPrediction,
		})
	}
	return toolResultJSON(map[string]any{"count": len(apps), "topN": out})
}

func histogramHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	urc, ok := store.Get(argString(req, "id"))
	if !ok {
		return mcp.NewToolResultError("unknown id (analyze first)"), nil
	}
	if len(urc.UploadResponse) == 0 {
		return mcp.NewToolResultError("empty analysis result"), nil
	}
	return toolResultJSON(map[string]any{
		"histogramStats": urc.UploadResponse[0].HistogramStats,
	})
}

// buildSummary produces the concise result returned by analyze_bugreport.
func buildSummary(id string, urc *UploadResponseCompare) map[string]any {
	out := map[string]any{"id": id}
	if len(urc.UploadResponse) == 0 {
		return out
	}
	ur := urc.UploadResponse[0]
	out["fileName"] = ur.FileName
	out["sdkVersion"] = ur.SDKVersion
	out["deviceCapacity"] = ur.DeviceCapacity
	out["criticalError"] = ur.CriticalError
	out["note"] = ur.Note

	apps := append([]AppStat(nil), ur.AppStats...)
	sort.SliceStable(apps, func(i, j int) bool {
		return apps[i].DevicePowerPrediction > apps[j].DevicePowerPrediction
	})
	top := apps
	if len(top) > 20 {
		top = top[:20]
	}
	topApps := make([]map[string]any, 0, len(top))
	for _, a := range top {
		var k appKey
		_ = json.Unmarshal(a.RawStats, &k)
		topApps = append(topApps, map[string]any{
			"uid":                   k.UID,
			"name":                  k.Name,
			"devicePowerPrediction": a.DevicePowerPrediction,
		})
	}
	out["topApps"] = topApps
	out["hint"] = "Use query_system_stats / query_app_stats / query_histogram with this id, or resources bugreport://<id>/..."
	return out
}
