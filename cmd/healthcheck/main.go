package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/reedhoop/ai-battery-historian/analyzer"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: healthcheck <bugreport.txt>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}

	results, err := analyzer.Analyze(string(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "analyze:", err)
		os.Exit(1)
	}
	for i, r := range results {
		fmt.Printf("=== REPORT %d: %s ===\n", i, r.FileName)
		fmt.Printf("SDK=%d Model=%q CriticalError=%q\n", r.SDKVersion, r.DeviceModel, r.CriticalError)
		if r.Health == nil {
			fmt.Println("HEALTH: nil")
			continue
		}
		b, _ := json.MarshalIndent(r.Health, "", "  ")
		fmt.Println("HEALTH:")
		fmt.Println(string(b))
	}
}
