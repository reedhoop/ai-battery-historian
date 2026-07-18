module github.com/reedhoop/ai-battery-historian

go 1.25.5

// Legacy Battery Historian packages were generated with the pre-1.4
// protobuf API (proto.RegisterType, no protoimpl), so they require
// golang/protobuf v1.3.x. mcp-go (v0.56.0) is protobuf-free, so the two
// coexist without a version conflict — no need to regenerate pb/ for P2.
require (
	github.com/golang/protobuf v1.3.5
	github.com/mark3labs/mcp-go v0.56.0
)

require (
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/text v0.14.0 // indirect
)
