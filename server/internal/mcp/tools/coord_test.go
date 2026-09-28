package tools

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

func newCoordDepsForTest(t *testing.T) CoordDeps {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return CoordDeps{
		Scratch: repo.NewScratchpadRepo(bundle.Client),
		Locks:   repo.NewCoordLockRepo(bundle.Client),
	}
}

func invokeCoordTool(t *testing.T, registry mcp.ToolRegistry, ctx context.Context, name string, args map[string]any) map[string]any {
	t.Helper()
	tool, ok := registry[name]
	require.True(t, ok, "tool %q not registered", name)
	result, err := tool.Handler(ctx, args)
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &m))
	return m
}

func ctxWithKey(keyID string) context.Context {
	return mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: keyID})
}

func TestCoordTools(t *testing.T) {
	deps := newCoordDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterCoordTools(registry, deps)

	const ns = "test-ns"

	t.Run("write then read scratchpad", func(t *testing.T) {
		ctx := ctxWithKey("task-A")

		invokeCoordTool(t, registry, ctx, "write_scratchpad", map[string]any{
			"namespace": ns,
			"key":       "mykey",
			"value":     "myvalue",
		})

		out := invokeCoordTool(t, registry, ctx, "read_scratchpad", map[string]any{
			"namespace": ns,
			"key":       "mykey",
		})
		entry, ok := out["entry"].(map[string]any)
		require.True(t, ok, "entry must be a map")
		require.Equal(t, "myvalue", entry["value"])
	})

	t.Run("acquire lock as task-A", func(t *testing.T) {
		ctx := ctxWithKey("task-A")

		out := invokeCoordTool(t, registry, ctx, "acquire_lock", map[string]any{
			"namespace":  ns,
			"key":        "mylock",
			"ttlSeconds": float64(60),
		})
		require.Equal(t, true, out["acquired"])
		require.Equal(t, "task-A", out["owner"])
	})

	t.Run("acquire same lock as task-B fails", func(t *testing.T) {
		ctx := ctxWithKey("task-B")

		out := invokeCoordTool(t, registry, ctx, "acquire_lock", map[string]any{
			"namespace": ns,
			"key":       "mylock",
		})
		require.Equal(t, false, out["acquired"])
	})

	t.Run("release lock as task-A then task-B can acquire", func(t *testing.T) {
		ctxA := ctxWithKey("task-A")
		ctxB := ctxWithKey("task-B")

		invokeCoordTool(t, registry, ctxA, "release_lock", map[string]any{
			"namespace": ns,
			"key":       "mylock",
		})

		out := invokeCoordTool(t, registry, ctxB, "acquire_lock", map[string]any{
			"namespace": ns,
			"key":       "mylock",
		})
		require.Equal(t, true, out["acquired"])
		require.Equal(t, "task-B", out["owner"])
	})
}

func TestWaitForPort(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterCoordTools(registry, CoordDeps{})

	t.Run("reaches a live listener", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()
		port := ln.Addr().(*net.TCPAddr).Port

		out := invokeCoordTool(t, registry, context.Background(), "wait_for_port", map[string]any{
			"host":           "127.0.0.1",
			"port":           float64(port),
			"timeoutSeconds": float64(3),
		})
		require.Equal(t, true, out["reached"])
		require.Equal(t, false, out["timedOut"])
	})

	t.Run("times out when nothing listens", func(t *testing.T) {
		// Use an ephemeral port that was just released — closed before dial.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		out := invokeCoordTool(t, registry, context.Background(), "wait_for_port", map[string]any{
			"host":           "127.0.0.1",
			"port":           float64(port),
			"timeoutSeconds": float64(1),
		})
		require.Equal(t, false, out["reached"])
		require.Equal(t, true, out["timedOut"])
	})

	t.Run("rejects port out of range", func(t *testing.T) {
		tool := registry["wait_for_port"]
		require.NotNil(t, tool)
		_, err := tool.Handler(context.Background(), map[string]any{
			"host": "127.0.0.1",
			"port": float64(99999),
		})
		require.Error(t, err)
	})

	t.Run("rejects timeout above cap", func(t *testing.T) {
		tool := registry["wait_for_port"]
		require.NotNil(t, tool)
		_, err := tool.Handler(context.Background(), map[string]any{
			"host":           "127.0.0.1",
			"port":           float64(8080),
			"timeoutSeconds": float64(301),
		})
		require.Error(t, err)
	})
}
