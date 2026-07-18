package main

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPrompts adds the two analytical prompt templates (design §5.3).
func registerPrompts(s *server.MCPServer) {
	s.AddPrompt(mcp.NewPrompt("battery_root_cause",
		mcp.WithPromptDescription("Given system/app battery metrics, produce a structured root-cause analysis."),
		mcp.WithArgument("metrics",
			mcp.ArgumentDescription("System stats + top app stats (JSON) from query_system_stats / query_app_stats."),
		),
	), rootCausePrompt)

	s.AddPrompt(mcp.NewPrompt("battery_ab_report",
		mcp.WithPromptDescription("Given an A/B diff, summarize what changed in battery behavior after an upgrade."),
		mcp.WithArgument("diff",
			mcp.ArgumentDescription("CombinedCheckin diff JSON from compare_bugreports."),
		),
	), abReportPrompt)
}

func rootCausePrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	metrics := ""
	if req.Params.Arguments != nil {
		metrics = req.Params.Arguments["metrics"]
	}
	text := `You are an Android battery performance expert. Given the following battery metrics, identify the most likely root causes of excessive drain and rank them by impact.

Metrics (JSON):
` + metrics + `

Analyze by correlating these signals:
- High wakelock time / count (userspace + kernel) -> background wakeups preventing sleep.
- High CPU power % -> hot app or runaway background work.
- High sync task frequency -> excessive content syncing.
- High signal scanning / mobile active time -> poor RF conditions or chatty radio.
- High screen-on discharge rate -> display/brightness; high screen-off rate -> background drain.
- GPS / camera / flashlight usage -> specific app features.

Return: (1) a ranked list of root causes with evidence, (2) the top suspect apps (uid/name), (3) concrete remediation suggestions.`

	return mcp.NewGetPromptResult("Battery root-cause analysis", []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleAssistant, mcp.NewTextContent(text)),
	}), nil
}

func abReportPrompt(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	diff := ""
	if req.Params.Arguments != nil {
		diff = req.Params.Arguments["diff"]
	}
	text := `You are an Android battery performance expert. Compare two bugreports (before/after an upgrade) using the diff below and summarize the change in battery behavior.

A/B diff (JSON):
` + diff + `

For each degraded metric (positive delta), explain the likely cause and which component/app is responsible. Highlight the top 3 regressions and top 3 improvements. Conclude with a go/no-go style recommendation.`

	return mcp.NewGetPromptResult("Battery A/B report", []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleAssistant, mcp.NewTextContent(text)),
	}), nil
}
