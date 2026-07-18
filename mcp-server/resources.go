package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerResources exposes analyzed results as MCP Resources (FR: Resources).
func registerResources(s *server.MCPServer) {
	// bugreport://{id}/system_stats
	s.AddResourceTemplate(mcp.NewResourceTemplate(
		"bugreport://{id}/system_stats",
		"System stats for an analyzed bugreport",
		mcp.WithTemplateDescription("Device capacity, histogram, raw batterystats proto and power estimates."),
		mcp.WithTemplateMIMEType("application/json"),
	), systemResourceHandler)

	// bugreport://{id}/app_stats/{uid}
	s.AddResourceTemplate(mcp.NewResourceTemplate(
		"bugreport://{id}/app_stats/{uid}",
		"Per-app stats for an analyzed bugreport",
		mcp.WithTemplateDescription("Raw batterystats for a single app uid."),
		mcp.WithTemplateMIMEType("application/json"),
	), appResourceHandler)

	// bugreport://{id}/raw_checkin
	s.AddResourceTemplate(mcp.NewResourceTemplate(
		"bugreport://{id}/raw_checkin",
		"Raw batterystats checkin proto (JSON) for an analyzed bugreport",
		mcp.WithTemplateDescription("The full batterystats proto as JSON."),
		mcp.WithTemplateMIMEType("application/json"),
	), rawCheckinResourceHandler)
}

// extractIDs pulls {id} and optional {uid} out of a bugreport:// URI.
func extractIDs(uri string) (id string, uid string, err error) {
	rest := strings.TrimPrefix(uri, "bugreport://")
	rest = strings.Trim(rest, "/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("malformed bugreport URI: %s", uri)
	}
	return parts[0], parts[1], nil
}

func systemResourceHandler(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	id, _, err := extractIDs(req.Params.URI)
	if err != nil {
		return nil, err
	}
	urc, ok := store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id: %s", id)
	}
	if len(urc.UploadResponse) == 0 {
		return nil, fmt.Errorf("empty result")
	}
	ur := urc.UploadResponse[0]
	payload := map[string]any{
		"sdkVersion":     ur.SDKVersion,
		"fileName":       ur.FileName,
		"deviceCapacity": ur.DeviceCapacity,
		"criticalError":  ur.CriticalError,
		"histogramStats": ur.HistogramStats,
		"batteryStats":   ur.BatteryStats,
		"powerEstimates": urc.CombinedCheckin.DevicePowerEstimatesCombined,
	}
	return jsonResource(req.Params.URI, payload)
}

func appResourceHandler(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	id, uid, err := extractIDs(req.Params.URI)
	if err != nil {
		return nil, err
	}
	urc, ok := store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id: %s", id)
	}
	if len(urc.UploadResponse) == 0 {
		return nil, fmt.Errorf("empty result")
	}
	for _, a := range urc.UploadResponse[0].AppStats {
		var k appKey
		if json.Unmarshal(a.RawStats, &k) != nil {
			continue
		}
		if uid32, ok := atoiSafe(uid); ok && k.UID == uid32 {
			return jsonResource(req.Params.URI, map[string]any{
				"uid":                   k.UID,
				"name":                  k.Name,
				"devicePowerPrediction": a.DevicePowerPrediction,
				"cpuPowerPrediction":    a.CPUPowerPrediction,
				"rawStats":              a.RawStats,
			})
		}
	}
	return nil, fmt.Errorf("uid %s not found", uid)
}

func rawCheckinResourceHandler(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	id, _, err := extractIDs(req.Params.URI)
	if err != nil {
		return nil, err
	}
	urc, ok := store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown id: %s", id)
	}
	if len(urc.UploadResponse) == 0 {
		return nil, fmt.Errorf("empty result")
	}
	return jsonResource(req.Params.URI, map[string]any{
		"batteryStats": urc.UploadResponse[0].BatteryStats,
	})
}

func jsonResource(uri string, v any) ([]mcp.ResourceContents, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      uri,
			MIMEType: "application/json",
			Text:     string(b),
		},
	}, nil
}

// atoiSafe parses a uid string, supporting negative uids (e.g. shared uids).
// Returns (value, true) on success, (0, false) if not a valid int32.
func atoiSafe(s string) (int32, bool) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}
