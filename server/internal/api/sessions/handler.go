// Package sessions provides HTTP handlers for session listing and output reading.
package sessions

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

// sessionIDHash returns a short hash prefix for logging — avoids correlating log
// entries to JSONL files when logs are shipped off-box.
func sessionIDHash(id string) string {
	h := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", h[:6])
}

var sessionIDRE = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// List handles GET /api/sessions.
// Returns the 100 most recently modified sessions with token/model/preview info.
//
// Security note (PRIV-003): authentication is enforced at the router level via
// RequireAuth (auth mode) or RequireSameOriginForMutations (bypass mode). In
// bypass mode this is a single-user local dashboard so no per-user scoping is
// needed. In auth mode, session JSONL files are keyed by an encoded CWD path —
// there is no direct mapping from a GitHub user ID to those paths — so
// filesystem-level per-user filtering is not feasible without a separate
// user→projects registry. Callers in auth mode see all sessions on the host;
// this is an accepted limitation of the local-dashboard model documented in the
// architecture decision records.
func List(w http.ResponseWriter, r *http.Request) {
	sessions, err := parser.GetSessions(r.Context())
	if err != nil {
		slog.Error("sessions list", "err", err)
		http.Error(w, `{"error":"failed to list sessions"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

// Output handles GET /api/agents/{sessionId}/output.
// Returns all messages from the session JSONL, or just the last assistant message
// when ?last=1 is set.
// Auth is enforced at router level; per-user scoping limitation applies (see List).
func Output(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	if !sessionIDRE.MatchString(sessionID) {
		http.Error(w, `{"error":"invalid sessionId format"}`, http.StatusBadRequest)
		return
	}

	lastOnly := r.URL.Query().Get("last") == "1"
	messages, err := parser.ParseFullSession(sessionID, lastOnly)
	if err != nil {
		slog.Error("session output", "sessionHash", sessionIDHash(sessionID), "err", err)
		http.Error(w, `{"error":"failed to read session output"}`, http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []parser.OutputMessage{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": messages})
}
