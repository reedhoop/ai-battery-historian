// Copyright 2026. Battery Historian MCP server (Phase 1, Form A).
//
// A standalone MCP server that exposes Battery Historian's analysis as
// MCP Tools / Resources / Prompts. It proxies a *running* Historian HTTP
// service (POST /historian/), so it does NOT import the legacy
// battery-historian packages and is independently buildable.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Package-level singletons wired in main().
var (
	store     *Store
	historian *HistorianClient
)

func main() {
	historianURL := flag.String("historian-url", "http://localhost:9999",
		"Base URL of a running Battery Historian HTTP service")
	transport := flag.String("transport", "stdio", "MCP transport: stdio | http")
	addr := flag.String("addr", ":8080", "Listen address when --transport=http")
	maxEntries := flag.Int("max-entries", 20, "Max cached analysis results (LRU eviction)")
	flag.Parse()

	store = NewStore(*maxEntries)
	historian = NewHistorianClient(*historianURL)

	s := server.NewMCPServer(
		"battery-historian-mcp",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	registerTools(s)
	registerResources(s)
	registerPrompts(s)

	switch *transport {
	case "http", "sse", "streamable":
		hs := server.NewStreamableHTTPServer(s)
		log.Printf("MCP server (streamable HTTP) listening on %s -> proxying %s", *addr, *historianURL)
		if err := http.ListenAndServe(*addr, hs); err != nil {
			log.Fatalf("server error: %v", err)
		}
	default:
		log.Printf("MCP server (stdio) proxying %s", *historianURL)
		if err := server.ServeStdio(s); err != nil {
			log.Fatalf("server error: %v", err)
		}
	}
}

// toolResultJSON marshals v and wraps it as a text tool result.
func toolResultJSON(v any) (*mcp.CallToolResult, error) {
	b, err := jsonMarshalIndent(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
