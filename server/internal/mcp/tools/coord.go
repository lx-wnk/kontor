package tools

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

// CoordDeps holds the repositories required by the coordination tools.
type CoordDeps struct {
	Scratch repo.ScratchpadRepo
	Locks   repo.CoordLockRepo
}

// RegisterCoordTools registers the 5 agent:coord tools into the given registry.
func RegisterCoordTools(registry mcp.ToolRegistry, d CoordDeps) {
	registerWriteScratchpad(registry, d)
	registerReadScratchpad(registry, d)
	registerListScratchpad(registry, d)
	registerAcquireLock(registry, d)
	registerReleaseLock(registry, d)
	registerWaitForPort(registry)
}

func registerWaitForPort(registry mcp.ToolRegistry) {
	const maxTimeoutSeconds = 300
	registry.Register(&mcp.ToolDef{
		Name:        "wait_for_port",
		Description: "Poll a TCP endpoint until it accepts connections or the timeout elapses. Localhost-oriented; bounded to 300 s.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host":           map[string]any{"type": "string", "description": "Hostname or IP (typically 127.0.0.1)"},
				"port":           map[string]any{"type": "number", "description": "TCP port (1–65535)"},
				"timeoutSeconds": map[string]any{"type": "number", "description": "Max seconds to wait (1–300, default 60)"},
			},
			"required": []string{"host", "port"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			host, err := mcp.StringArg(args, "host")
			if err != nil {
				return nil, err
			}
			portF, ok := mcp.OptionalFloat64(args, "port")
			if !ok {
				return nil, mcp.Fail("port is required")
			}
			port := int(portF)
			if port < 1 || port > 65535 {
				return nil, mcp.Fail("port must be between 1 and 65535")
			}

			timeoutSecs := float64(60)
			if f, ok := mcp.OptionalFloat64(args, "timeoutSeconds"); ok {
				timeoutSecs = f
			}
			if timeoutSecs < 1 || timeoutSecs > maxTimeoutSeconds {
				return nil, mcp.Fail(fmt.Sprintf("timeoutSeconds must be between 1 and %d", maxTimeoutSeconds))
			}

			addr := net.JoinHostPort(host, strconv.Itoa(port))
			deadline := time.Now().Add(time.Duration(timeoutSecs) * time.Second)
			for time.Now().Before(deadline) {
				conn, dialErr := net.DialTimeout("tcp", addr, time.Second)
				if dialErr == nil {
					_ = conn.Close()
					return mcp.OK(map[string]any{"reached": true, "timedOut": false})
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
			}
			return mcp.OK(map[string]any{"reached": false, "timedOut": true})
		},
	})
}

// ownerFromCtx resolves the calling task's identity: auth KeyID takes precedence,
// falling back to the optional ownerTaskId argument for unauthenticated callers.
func ownerFromCtx(ctx context.Context, args map[string]any) string {
	if info := mcp.AuthFromContext(ctx); info != nil && info.KeyID != "" {
		return info.KeyID
	}
	return mcp.OptionalString(args, "ownerTaskId")
}

func registerWriteScratchpad(registry mcp.ToolRegistry, d CoordDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "write_scratchpad",
		Description: "Write or overwrite a key-value entry in an agent scratchpad namespace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace":   map[string]any{"type": "string"},
				"key":         map[string]any{"type": "string"},
				"value":       map[string]any{"type": "string"},
				"ownerTaskId": map[string]any{"type": "string", "description": "Caller task ID (inferred from auth when available)"},
			},
			"required": []string{"namespace", "key", "value"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			ns, err := mcp.StringArg(args, "namespace")
			if err != nil {
				return nil, err
			}
			key, err := mcp.StringArg(args, "key")
			if err != nil {
				return nil, err
			}
			value, err := mcp.StringArg(args, "value")
			if err != nil {
				return nil, err
			}
			owner := ownerFromCtx(ctx, args)
			if err := d.Scratch.Write(ctx, ns, key, value, owner); err != nil {
				return nil, mcp.Fail("write_scratchpad: " + err.Error())
			}
			return mcp.OK(map[string]any{"ok": true})
		},
	})
}

func registerReadScratchpad(registry mcp.ToolRegistry, d CoordDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "read_scratchpad",
		Description: "Read a single entry from an agent scratchpad namespace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
				"key":       map[string]any{"type": "string"},
			},
			"required": []string{"namespace", "key"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			ns, err := mcp.StringArg(args, "namespace")
			if err != nil {
				return nil, err
			}
			key, err := mcp.StringArg(args, "key")
			if err != nil {
				return nil, err
			}
			row, err := d.Scratch.Read(ctx, ns, key)
			if err != nil {
				return nil, mcp.Fail("read_scratchpad: " + err.Error())
			}
			return mcp.OK(map[string]any{"entry": row})
		},
	})
}

func registerListScratchpad(registry mcp.ToolRegistry, d CoordDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_scratchpad",
		Description: "List all entries in an agent scratchpad namespace.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
			},
			"required": []string{"namespace"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			ns, err := mcp.StringArg(args, "namespace")
			if err != nil {
				return nil, err
			}
			rows, err := d.Scratch.List(ctx, ns)
			if err != nil {
				return nil, mcp.Fail("list_scratchpad: " + err.Error())
			}
			return mcp.OK(map[string]any{"entries": rows})
		},
	})
}

func registerAcquireLock(registry mcp.ToolRegistry, d CoordDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "acquire_lock",
		Description: "Attempt to acquire a distributed coordination lock.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace":   map[string]any{"type": "string"},
				"key":         map[string]any{"type": "string"},
				"ttlSeconds":  map[string]any{"type": "number", "description": "Lock TTL in seconds (default 300)"},
				"ownerTaskId": map[string]any{"type": "string", "description": "Caller task ID (inferred from auth when available)"},
			},
			"required": []string{"namespace", "key"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			ns, err := mcp.StringArg(args, "namespace")
			if err != nil {
				return nil, err
			}
			key, err := mcp.StringArg(args, "key")
			if err != nil {
				return nil, err
			}
			owner := ownerFromCtx(ctx, args)

			ttl := float64(300)
			if f, ok := mcp.OptionalFloat64(args, "ttlSeconds"); ok {
				ttl = f
			}

			acquired, curOwner, expiresAt, err := d.Locks.Acquire(ctx, ns, key, owner, time.Duration(ttl)*time.Second)
			if err != nil {
				return nil, mcp.Fail("acquire_lock: " + err.Error())
			}
			return mcp.OK(map[string]any{
				"acquired":  acquired,
				"owner":     curOwner,
				"expiresAt": expiresAt,
			})
		},
	})
}

func registerReleaseLock(registry mcp.ToolRegistry, d CoordDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "release_lock",
		Description: "Release a coordination lock held by the calling task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace":   map[string]any{"type": "string"},
				"key":         map[string]any{"type": "string"},
				"ownerTaskId": map[string]any{"type": "string", "description": "Caller task ID (inferred from auth when available)"},
			},
			"required": []string{"namespace", "key"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			ns, err := mcp.StringArg(args, "namespace")
			if err != nil {
				return nil, err
			}
			key, err := mcp.StringArg(args, "key")
			if err != nil {
				return nil, err
			}
			owner := ownerFromCtx(ctx, args)
			if err := d.Locks.Release(ctx, ns, key, owner); err != nil {
				return nil, mcp.Fail(err.Error())
			}
			return mcp.OK(map[string]any{"released": true})
		},
	})
}
