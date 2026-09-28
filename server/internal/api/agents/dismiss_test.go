package agents

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/testsupport/fakespawn"
)

func doDismiss(t *testing.T, pid int) *httptest.ResponseRecorder {
	t.Helper()
	h := NewSpawnHandler(nil)
	r := chi.NewRouter()
	r.Delete("/api/agents/{pid}/channel", h.DismissChannel)
	req := httptest.NewRequest(http.MethodDelete, "/api/agents/"+strconv.Itoa(pid)+"/channel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestDismissChannel_DeletesFilesForDeadPID(t *testing.T) {
	fs := fakespawn.New(t)
	ag := fs.Spawn(fakespawn.SpawnOpts{Pty: true})

	rec := doDismiss(t, ag.PID)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, err := os.Stat(fs.DiscoveryPath(ag.PID)); !os.IsNotExist(err) {
		t.Errorf("bridge file still present")
	}
	if _, err := os.Stat(fs.DiscoveryPtyPath(ag.PID)); !os.IsNotExist(err) {
		t.Errorf("pty file still present")
	}
}

func TestDismissChannel_IdempotentWhenAbsent(t *testing.T) {
	fakespawn.New(t)
	if rec := doDismiss(t, 999998); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestDismissChannel_RefusesLivePID(t *testing.T) {
	fs := fakespawn.New(t)
	pid := os.Getpid() // current process is alive
	bridge := channelconfig.DiscoveryFile(fs.Home, pid)
	if err := os.MkdirAll(filepath.Dir(bridge), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridge, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doDismiss(t, pid)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if _, err := os.Stat(bridge); err != nil {
		t.Errorf("live agent's discovery file was deleted")
	}
}

func TestDismissChannel_RejectsBadPID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewSpawnHandler(nil)
	r := chi.NewRouter()
	r.Delete("/api/agents/{pid}/channel", h.DismissChannel)
	req := httptest.NewRequest(http.MethodDelete, "/api/agents/abc/channel", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
