package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// ToolResult is the MCP content block every tool returns.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a single MCP content item.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// OK wraps data as a JSON text content block.
func OK(data any) (*ToolResult, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal result: %w", err)
	}
	return &ToolResult{Content: []ContentBlock{{Type: "text", Text: string(b)}}}, nil
}

// MCPError is an error type that the JSON-RPC handler surfaces as a tool error.
type MCPError struct{ Message string }

func (e *MCPError) Error() string { return e.Message }

// Fail creates an MCPError — call this inside tool handlers to signal tool-level failures.
func Fail(msg string) error { return &MCPError{Message: msg} }

// ToolHandler is the function signature every tool implements.
type ToolHandler func(ctx context.Context, args map[string]any) (*ToolResult, error)

// ToolDef holds a registered tool's metadata and handler.
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema object
	Handler     ToolHandler
	// Available gates listing and calling per request; nil means always.
	Available func() bool
}

// available reports whether def may be listed or called right now.
func available(def *ToolDef) bool { return def.Available == nil || def.Available() }

// ToolRegistry maps tool names to definitions.
type ToolRegistry map[string]*ToolDef

// Register adds a tool to the registry.
// Panics on duplicate name or missing ToolScopeMap entry (both are programming errors).
func (r ToolRegistry) Register(def *ToolDef) {
	if _, exists := r[def.Name]; exists {
		panic("mcp: duplicate tool registration: " + def.Name)
	}
	if _, hasScopeEntry := ToolScopeMap[def.Name]; !hasScopeEntry {
		panic("mcp: tool registered without a ToolScopeMap entry: " + def.Name)
	}
	r[def.Name] = def
}

// StringArg extracts a required string argument. Returns Fail error if missing or wrong type.
func StringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", Fail(key + " is required")
	}
	s, ok := v.(string)
	if !ok {
		return "", Fail(key + " must be a string")
	}
	return s, nil
}

// OptionalStringArg returns the named string argument, or "" when the key is
// absent or explicitly null. A present value of any other type is an error
// rather than a silent "", so a rule keyed on emptiness cannot be bypassed by
// sending the wrong type.
func OptionalStringArg(args map[string]any, key string) (string, error) {
	if raw, present := args[key]; !present || raw == nil {
		return "", nil
	}
	return StringArg(args, key)
}

// OptionalString extracts an optional string; returns "" if absent.
// Prefer OptionalStringArg when emptiness carries a rule — this variant reads a
// wrong-typed value as absent instead of rejecting it.
func OptionalString(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return v
}

// OptionalBool extracts an optional bool; returns false if absent.
func OptionalBool(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

// OptionalFloat64 extracts an optional JSON number; returns (0, false) if absent.
func OptionalFloat64(args map[string]any, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

// RebindJSON round-trips args through JSON into dst struct (useful for complex args).
func RebindJSON(args map[string]any, dst any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return Fail("cannot re-encode args: " + err.Error())
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return Fail("invalid arguments: " + err.Error())
	}
	return nil
}
