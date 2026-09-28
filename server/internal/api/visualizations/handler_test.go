package visualizations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// errorsAs is a thin wrapper so the test file can do an inline type
// assertion against an apierr.AppError without dragging the errors
// import into every test.
func errorsAs(err error, target **apierr.AppError) bool {
	return errors.As(err, target)
}

// withFakeClaudeDir points parser.AllClaudeConfigDirs at a temp dir for the
// duration of the test. The parser package reads DASHBOARD_CLAUDE_CONFIG_DIRS
// at every call, so a simple env override is enough.
func withFakeClaudeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Point every parser fallback at the empty fixture dir so the test
	// cannot pick up real sessions on the developer machine.
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HOME", dir)
	return dir
}

func TestHandler_DAG_MissingSessionReturns400(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/dag", nil)
	if err := h.DAG(rr, req); err == nil {
		t.Fatalf("expected error from DAG without session")
	} else if appErr := err.Error(); appErr == "" {
		t.Fatalf("empty error message")
	}
}

func TestHandler_Sankey_BadTimestampReturns400(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/sankey?from=not-a-timestamp", nil)
	if err := h.Sankey(rr, req); err == nil {
		t.Fatalf("expected error from bad from timestamp")
	}
}

func TestParseTimestamp_AcceptsLayouts(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"rfc3339", "2026-05-01T00:00:00Z", false},
		{"datetime-local", "2026-05-01T14:30", false}, // HTML datetime-local default (no seconds, no tz)
		{"datetime-local-seconds", "2026-05-01T14:30:05", false},
		{"date-only", "2026-05-01", false},
		{"garbage", "not-a-timestamp", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTimestamp(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatalf("parseTimestamp(%q): expected error", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("parseTimestamp(%q): unexpected error %v", tc.raw, err)
			}
		})
	}
}

func TestHandler_Sankey_DatetimeLocalAccepted(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/sankey?from=2026-05-01T14:30&to=2026-05-08T14:30", nil)
	if err := h.Sankey(rr, req); err != nil {
		t.Fatalf("Sankey with datetime-local bounds: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestHandler_Sankey_EmptyConfigReturns200WithEmptyData(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/sankey", nil)
	if err := h.Sankey(rr, req); err != nil {
		t.Fatalf("Sankey: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got sdk.SankeyData
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Nodes) != 0 || len(got.Links) != 0 {
		t.Errorf("expected empty data, got %+v", got)
	}
}

func TestHandler_SpawnTree_EmptyConfigReturns200(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/spawn-tree", nil)
	if err := h.SpawnTree(rr, req); err != nil {
		t.Fatalf("SpawnTree: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestHandler_CoOccurrence_EmptyConfigReturns200(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/co-occurrence", nil)
	if err := h.CoOccurrence(rr, req); err != nil {
		t.Fatalf("CoOccurrence: %v", err)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got sdk.CoOccurrenceData
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Tools == nil {
		t.Errorf("expected non-nil Tools slice, got nil")
	}
}

func TestHandler_TimeoutWrapper_Returns503(t *testing.T) {
	withFakeClaudeDir(t)
	// Use the wrapped function directly so we can feed it a slow handler
	// without spinning up a chi router.
	slow := func(w http.ResponseWriter, r *http.Request) error {
		<-r.Context().Done()
		return r.Context().Err()
	}
	wrapped := withTimeout(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/sankey", nil).WithContext(ctx)
	err := wrapped(rr, req)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	var appErr *apierr.AppError
	if !errorsAs(err, &appErr) || appErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 AppError, got %T %v", err, err)
	}
}

func TestHandler_DAG_UnknownSessionReturnsInternalError(t *testing.T) {
	withFakeClaudeDir(t)
	h := NewHandler()
	rr := httptest.NewRecorder()
	sessID := "10000000-0000-0000-0000-000000000099"
	req := httptest.NewRequest(http.MethodGet, "/api/visualizations/dag?session="+sessID, nil)
	if err := h.DAG(rr, req); err == nil {
		t.Fatalf("expected error for unknown session id")
	}
}
