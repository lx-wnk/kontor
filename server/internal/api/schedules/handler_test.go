package schedules_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/schedules"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
)

func newServerWithApps(t *testing.T) (*httptest.Server, repo.MCPApplicationRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	apps := repo.NewMCPApplicationRepo(bundle.Client)
	h := schedules.NewHandler(
		repo.NewTaskScheduleRepo(bundle.Client),
		scheduler.NewNLCron(nil),
		nil,
		apps,
		repo.NewTaskRepo(bundle.Client),
		repo.NewStageRunRepo(bundle.Client),
		true,
	)
	r := chi.NewRouter()
	h.Mount(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, apps
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newServerWithApps(t)
	return srv
}

func scheduleWith(applications []string) map[string]any {
	return map[string]any{
		"name": "inbox", "cronExpr": "*/5 * * * *", "slugPrefix": "inbox",
		"title": "Inbox", "cwd": "/tmp", "applications": applications,
	}
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func TestPreview_ValidPhrase(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules/preview", map[string]any{"nlText": "every weekday at 9am"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		CronExpr string   `json:"cronExpr"`
		NextRuns []string `json:"nextRuns"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.CronExpr != "0 9 * * 1-5" {
		t.Fatalf("cronExpr = %q", out.CronExpr)
	}
	if len(out.NextRuns) != 5 {
		t.Fatalf("want 5 next runs, got %d", len(out.NextRuns))
	}
}

func TestPreview_InvalidPhrase422(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules/preview", map[string]any{"nlText": "whenever the mood strikes"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestCreateAndList(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules", map[string]any{
		"name":       "nightly build",
		"nlText":     "every day at 2am",
		"slugPrefix": "nightly",
		"title":      "Nightly build",
		"cwd":        "/tmp",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var created struct {
		ID        string `json:"id"`
		CronExpr  string `json:"cronExpr"`
		NextRunAt string `json:"nextRunAt"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.CronExpr != "0 2 * * *" {
		t.Fatalf("cronExpr = %q", created.CronExpr)
	}
	if created.NextRunAt == "" {
		t.Fatal("expected next_run_at to be initialized on create")
	}

	listResp, err := http.Get(srv.URL + "/api/schedules")
	if err != nil {
		t.Fatalf("GET list: %v", err)
	}
	defer listResp.Body.Close()
	var list []map[string]any
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	if len(list) != 1 {
		t.Fatalf("want 1 schedule, got %d", len(list))
	}
}

func TestCreate_AcceptsKnownApplications(t *testing.T) {
	srv, apps := newServerWithApps(t)
	_, err := apps.Upsert(context.Background(), repo.UpsertMCPApplicationInput{ResourceID: "res-mail", ServerName: "mail"})
	if err != nil {
		t.Fatal(err)
	}

	resp := post(t, srv.URL+"/api/schedules", scheduleWith([]string{"res-mail"}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Applications []string `json:"applications"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Applications) != 1 || got.Applications[0] != "res-mail" {
		t.Fatalf("applications = %v", got.Applications)
	}
}

func TestCreate_RejectsUnknownApplication(t *testing.T) {
	srv, _ := newServerWithApps(t)
	resp := post(t, srv.URL+"/api/schedules", scheduleWith([]string{"res-does-not-exist"}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — an id that names no application must not be stored", resp.StatusCode)
	}
}

func TestCreate_InvalidPhrase422(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules", map[string]any{
		"name":       "bad",
		"nlText":     "sometime maybe",
		"slugPrefix": "bad",
		"title":      "Bad",
		"cwd":        "/tmp",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func patch(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build PATCH %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	return resp
}

// gitDir returns a temp dir initialized as a git repo, skipping the test when
// git is not on PATH.
func gitDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "init", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestCreate_DefaultRunModeIsJob(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules", scheduleWith(nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var got struct {
		RunMode      string `json:"runMode"`
		SkippedCount int    `json:"skippedCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.RunMode != "job" {
		t.Fatalf("runMode = %q, want job", got.RunMode)
	}
	if got.SkippedCount != 0 {
		t.Fatalf("skippedCount = %d, want 0", got.SkippedCount)
	}
}

func TestCreate_PipelineRunModeRequiresGitCwd(t *testing.T) {
	srv := newServer(t)
	body := scheduleWith(nil)
	body["runMode"] = "pipeline"
	body["cwd"] = t.TempDir()
	resp := post(t, srv.URL+"/api/schedules", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := bodyString(t, resp); !strings.Contains(got, "working directory is not a git repository") {
		t.Fatalf("body = %q, want git-repo error", got)
	}
}

func TestCreate_PipelineRunModeAcceptsGitCwd(t *testing.T) {
	srv := newServer(t)
	body := scheduleWith(nil)
	body["runMode"] = "pipeline"
	body["cwd"] = gitDir(t)
	resp := post(t, srv.URL+"/api/schedules", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestCreate_InvalidRunMode400(t *testing.T) {
	srv := newServer(t)
	body := scheduleWith(nil)
	body["runMode"] = "cron"
	resp := post(t, srv.URL+"/api/schedules", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := bodyString(t, resp); !strings.Contains(got, "runMode must be job or pipeline") {
		t.Fatalf("body = %q, want runMode error", got)
	}
}

func TestUpdate_JobToPipelineRequiresGitCwd(t *testing.T) {
	srv := newServer(t)
	createResp := post(t, srv.URL+"/api/schedules", scheduleWith(nil))
	var created struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	resp := patch(t, srv.URL+"/api/schedules/"+created.ID, map[string]any{"runMode": "pipeline"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestUpdate_PipelineCwdChangeRequiresGit(t *testing.T) {
	srv := newServer(t)
	body := scheduleWith(nil)
	body["runMode"] = "pipeline"
	body["cwd"] = gitDir(t)
	createResp := post(t, srv.URL+"/api/schedules", body)
	var created struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()

	resp := patch(t, srv.URL+"/api/schedules/"+created.ID, map[string]any{"cwd": t.TempDir()})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", resp.StatusCode, bodyString(t, resp))
	}
}

func TestView_NeverContainsCurrentStage(t *testing.T) {
	srv := newServer(t)
	resp := post(t, srv.URL+"/api/schedules", scheduleWith(nil))
	defer resp.Body.Close()
	if got := bodyString(t, resp); strings.Contains(got, "currentStage") {
		t.Fatalf("view contains currentStage: %s", got)
	}
}
