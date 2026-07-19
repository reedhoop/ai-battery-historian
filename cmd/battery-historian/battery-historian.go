// Copyright 2016 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Historian v2 analyzes bugreports and outputs battery analysis results.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"path"

	"github.com/reedhoop/ai-battery-historian/analyzer"
)

var (
	optimized = flag.Bool("optimized", true, "Whether to output optimized js files. Disable for local debugging.")
	useCDN    = flag.Bool("use_cdn", false, "Load frontend assets from external bootcdn.net CDN instead of the local vendored copies in static/vendor. Default false = local/offline.")
	port      = flag.Int("port", 9999, "service port")

	compiledDir   = flag.String("compiled_dir", "./compiled", "Directory containing compiled js file for Historian v2.")
	jsDir         = flag.String("js_dir", "./js", "Directory containing uncompiled js files for Historian v2.")
	scriptsDir    = flag.String("scripts_dir", "./scripts", "Directory containing Historian and kernel trace Python scripts.")
	staticDir     = flag.String("static_dir", "./static", "Directory containing static files.")
	templateDir   = flag.String("template_dir", "./templates", "Directory containing HTML templates.")
	thirdPartyDir = flag.String("third_party_dir", "./third_party", "Directory containing third party files for Historian v2.")

	// resVersion should be incremented whenever the JS or CSS files are modified.
	resVersion = flag.Int("res_version", 2, "The current version of JS and CSS files. Used to force JS and CSS reloading to avoid cache issues when rolling out new versions.")

	// Phase 2 (native MCP) flags.
	mcpMode       = flag.Bool("mcp", false, "Run as an MCP server (stdio) instead of the HTTP web server.")
	mcpTransport  = flag.String("mcp_transport", "stdio", "MCP transport when --mcp is set: stdio | http.")
	mcpAddr       = flag.String("mcp_addr", ":8080", "Listen address when --mcp_transport=http.")
	mcpMaxEntries = flag.Int("mcp_max_entries", 20, "Max cached analyses in the MCP store (LRU eviction).")
	mcpWithChart  = flag.Bool("mcp_with_chart", false, "Generate the Historian plot HTML at analyze time so it can be served via bugreport://{id}/chart (requires Python 3 + scripts/historian.py).")
)

type analysisServer struct{}

func (s *analysisServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("Trace starting analysisServer processing for: %s", r.Method)
	defer log.Printf("Trace finished analysisServer processing for: %s", r.Method)

	switch r.Method {
	case "GET":
		analyzer.UploadHandler(w, r)
	case "POST":
		r.ParseForm()
		analyzer.HTTPAnalyzeHandler(w, r)
	default:
		http.Error(w, fmt.Sprintf("Method %s not allowed", r.Method), http.StatusMethodNotAllowed)
	}
}

func compiledPath() string {
	dir := *compiledDir
	if dir == "" {
		dir = "./compiled"
	}
	return dir
}

func jsPath() string {
	dir := *jsDir
	if dir == "" {
		dir = "./js"
	}
	return dir
}

func staticPath() string {
	dir := *staticDir
	if dir == "" {
		dir = "./static"
	}
	return dir
}

func thirdPartyPath() string {
	dir := *thirdPartyDir
	if dir == "" {
		dir = "./third_party"
	}
	return dir
}

func initFrontend() {
	urlPrefix := []string{"/", "/historian/"} // Add all paths relative to root
	urlDirs := map[string]string{
		"compiled":    compiledPath(),
		"static":      staticPath(),
		"third_party": thirdPartyPath(),
	}

	for _, p := range urlPrefix {
		http.Handle(p, &analysisServer{})

		for u, f := range urlDirs {
			url := path.Join(p, u) + "/"
			http.Handle(url, http.StripPrefix(url, http.FileServer(http.Dir(f))))
		}
		if *optimized == false {
			// Need to handle calls to fetch closure library and js files.
			j := path.Join(p, "js") + "/"
			http.Handle(j, http.StripPrefix(j, http.FileServer(http.Dir(jsPath()))))
		}
	}
}

func main() {
	flag.Parse()

	// Phase 2: native MCP mode runs the analysis core in-process and skips the
	// HTTP web server (and the Python plot). It is mutually exclusive with the
	// Historian v2 web UI.
	if *mcpMode {
		// Honor --scripts_dir so the chart subprocess can locate
		// scripts/historian.py regardless of the current working directory.
		analyzer.SetScriptsDir(*scriptsDir)
		startMCPServer(*mcpTransport, *mcpAddr, *mcpMaxEntries)
		return
	}

	initFrontend()
	analyzer.InitTemplates(*templateDir)
	analyzer.SetScriptsDir(*scriptsDir)
	analyzer.SetResVersion(*resVersion)
	analyzer.SetIsOptimized(*optimized)
	analyzer.SetUseCDN(*useCDN)
	log.Println("Listening on port: ", *port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
