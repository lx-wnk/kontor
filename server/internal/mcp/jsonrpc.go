package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sort"
)

const protocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCPHandler returns a chi-compatible http.HandlerFunc for POST /api/mcp.
// It handles: initialize, tools/list, tools/call.
// modules may be nil: a server built without a module source serves exactly the
// core tools.
func MCPHandler(registry ToolRegistry, modules ModuleTools, moduleGate ModuleToolAuthorizer) http.HandlerFunc {
	coreDefs := sortedToolDefs(registry)
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			return
		}
		if req.JSONRPC != "2.0" {
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "jsonrpc must be \"2.0\""}})
			return
		}

		switch req.Method {
		case "initialize":
			writeRPC(w, rpcResponse{
				JSONRPC: "2.0", ID: req.ID,
				Result: map[string]any{
					"protocolVersion": protocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": ServerName, "version": "1.0.0"},
				},
			})

		case "tools/list":
			// Filtered per request: an unavailable tool or a stopped module must be absent, never listed and then failing.
			tools := make([]map[string]any, 0, len(coreDefs))
			for _, def := range coreDefs {
				if !available(def) {
					continue
				}
				tools = append(tools, map[string]any{
					"name":        def.Name,
					"description": def.Description,
					"inputSchema": def.InputSchema,
				})
			}
			if modules != nil {
				offered := modules.List(r.Context())
				visible := moduleToolVisibility(r.Context(), moduleGate, offered)
				for _, t := range offered {
					// Listed only when the caller may call it: a tool an agent
					// can see but never use is an invitation to keep trying.
					if !visible[ModuleToolScope(t.ModuleID, t.Name)] {
						continue
					}
					tools = append(tools, map[string]any{
						"name":        t.QualifiedName(),
						"description": t.Description,
						"inputSchema": t.InputSchema,
					})
				}
			}
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})

		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
				return
			}
			if modules != nil {
				if moduleID, tool, isQualified := SplitQualifiedName(p.Name); isQualified {
					if _, known := registry[p.Name]; !known {
						handleModuleToolCall(w, r, req.ID, modules, moduleGate, moduleID, tool, p.Arguments)
						return
					}
				}
			}
			def, ok := registry[p.Name]
			if !ok || !available(def) {
				writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "tool not found: " + p.Name}})
				return
			}
			// Scope enforcement per tool
			auth := AuthFromContext(r.Context())
			requiredScope := ToolScopeMap[p.Name]
			if auth == nil || !auth.Scopes[requiredScope] {
				writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32003, Message: "Insufficient scope: requires " + requiredScope}})
				return
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			result, err := callHandler(def, r.Context(), p.Arguments)
			if err != nil {
				code := -32603 // internal error
				if _, isMCP := err.(*MCPError); isMCP {
					code = -32003 // tool-level failure (insufficient scope, not-found, etc.)
				}
				writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: err.Error()}})
				return
			}
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})

		default:
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
		}
	}
}

// callHandler invokes def.Handler and converts any panic into an error so the
// JSON-RPC response always includes the request id rather than returning a bare HTTP 500.
func callHandler(def *ToolDef, ctx context.Context, args map[string]any) (result *ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcp: handler panic", "tool", def.Name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
	return def.Handler(ctx, args)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// sortedToolDefs snapshots the registry once at startup, sorted by name for determinism.
func sortedToolDefs(registry ToolRegistry) []*ToolDef {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*ToolDef, 0, len(registry))
	for _, name := range names {
		out = append(out, registry[name])
	}
	return out
}

// handleModuleToolCall forwards a namespaced call to the module that offers it.
// The module is looked up in the live list rather than trusted from the name:
// a call naming a module that is not currently offering the tool is an unknown
// tool, which is what an agent can act on, not a failed round trip.
func handleModuleToolCall(w http.ResponseWriter, r *http.Request, id any, modules ModuleTools, moduleGate ModuleToolAuthorizer, moduleID, tool string, args map[string]any) {
	offered := false
	for _, t := range modules.List(r.Context()) {
		if t.ModuleID == moduleID && t.Name == tool {
			offered = true
			break
		}
	}
	if !offered {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32601, Message: "tool not found: " + moduleID + NamespaceSeparator + tool}})
		return
	}
	capName := ModuleToolScope(moduleID, tool)
	// Fail closed: a handler built without an authorizer refuses every module
	// tool rather than serving them ungoverned.
	if moduleGate == nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32003, Message: "not permitted: " + capName}})
		return
	}
	if err := moduleGate.Authorize(r.Context(), capName); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32003, Message: "not permitted: " + capName + ": " + err.Error()}})
		return
	}
	out, err := modules.Call(r.Context(), moduleID, tool, args)
	if err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}})
		return
	}
	writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: id, Result: map[string]any{
		"content": []map[string]any{{"type": "text", "text": out}},
	}})
}

// moduleToolVisibility asks once for every offered tool. Nil authorizer means
// nothing is visible, the same refusal the call path makes.
func moduleToolVisibility(ctx context.Context, gate ModuleToolAuthorizer, offered []ModuleTool) map[string]bool {
	if gate == nil || len(offered) == 0 {
		return nil
	}
	names := make([]string, 0, len(offered))
	for _, t := range offered {
		names = append(names, ModuleToolScope(t.ModuleID, t.Name))
	}
	return gate.Visible(ctx, names)
}
