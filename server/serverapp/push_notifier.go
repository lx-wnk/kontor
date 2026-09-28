package serverapp

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	wpservice "github.com/lx-wnk/kontor/server/internal/webpush"
)

type pushPermissionNotifier struct{ svc *wpservice.Service }

// newPushPermissionNotifier returns a tasks.PermissionNotifier backed by svc,
// or a true-nil interface when svc is nil (no webpush service configured).
func newPushPermissionNotifier(svc *wpservice.Service) tasks.PermissionNotifier {
	if svc == nil {
		return nil
	}
	return pushPermissionNotifier{svc: svc}
}

func pushPermissionPayload(taskID, taskTitle, tool string) []byte {
	b, _ := json.Marshal(map[string]string{
		"title": "Approval needed",
		"body":  taskTitle + ": " + tool,
		"url":   "/",
		"tag":   "permission-" + taskID,
	})
	return b
}

func (n pushPermissionNotifier) PermissionRequested(ctx context.Context, taskID, taskTitle, tool string) {
	if _, err := n.svc.SendToAll(ctx, pushPermissionPayload(taskID, taskTitle, tool)); err != nil {
		slog.Warn("push: permission request notification failed", "taskID", taskID, "tool", tool, "err", err)
	}
}
