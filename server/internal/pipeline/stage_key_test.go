package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// newFakeStageRunRepo constructs the package's existing fake repo.StageRunRepo
// stub (stage_run_service_test.go) — it already records the last Update call,
// which is all these tests assert on.
func newFakeStageRunRepo() *fakeStageRunRepo {
	return &fakeStageRunRepo{}
}

func repoUpdateStatus(s string) repo.UpdateStageRunInput {
	return repo.UpdateStageRunInput{Status: strPtr(s)}
}

var errRevokeBoom = errors.New("revoke boom")

func TestBuildTaskAPI_EmptyTokenReturnsNil(t *testing.T) {
	if got := buildTaskAPI(SpawnAgentOptions{MCPUrl: "http://127.0.0.1:13120"}); got != nil {
		t.Fatalf("got %+v, want nil (no credential minted)", got)
	}
}

func TestBuildTaskAPI_EmptyURLReturnsNil(t *testing.T) {
	if got := buildTaskAPI(SpawnAgentOptions{TaskAPIToken: "tok"}); got != nil {
		t.Fatalf("got %+v, want nil (no dashboard URL)", got)
	}
}

func TestBuildTaskAPI_BuildsEndpointFromMCPUrl(t *testing.T) {
	got := buildTaskAPI(SpawnAgentOptions{TaskAPIToken: "tok", MCPUrl: "http://127.0.0.1:13120"})
	if got == nil {
		t.Fatal("got nil, want a TaskAPI")
	}
	if want := "http://127.0.0.1:13120" + mcp.EndpointPath; got.URL != want {
		t.Fatalf("got URL %q, want %q", got.URL, want)
	}
	if got.Token != "tok" {
		t.Fatalf("got Token %q, want %q", got.Token, "tok")
	}
}

func TestStageRunService_RevokesOnTerminalStatus(t *testing.T) {
	var revoked []string
	svc := &stageRunService{
		repo:   newFakeStageRunRepo(),
		revoke: func(_ context.Context, id string) error { revoked = append(revoked, id); return nil },
	}
	ctx := context.Background()

	_, _ = svc.Update(ctx, "sr-1", repoUpdateStatus("running"))
	if len(revoked) != 0 {
		t.Fatalf("a running run must keep its credential, got %v", revoked)
	}

	_, _ = svc.Update(ctx, "sr-1", repoUpdateStatus("awaiting_user"))
	if len(revoked) != 0 {
		t.Fatalf("awaiting_user is resumable and must keep its credential, got %v", revoked)
	}

	for _, status := range []string{"done", "failed", "cancelled"} {
		revoked = nil
		_, _ = svc.Update(ctx, "sr-1", repoUpdateStatus(status))
		if len(revoked) != 1 || revoked[0] != "sr-1" {
			t.Fatalf("status %q must revoke the run's credentials, got %v", status, revoked)
		}
	}
}

func TestStageRunService_RevokeFailureDoesNotFailTheWrite(t *testing.T) {
	svc := &stageRunService{
		repo:   newFakeStageRunRepo(),
		revoke: func(context.Context, string) error { return errRevokeBoom },
	}
	if _, err := svc.Update(context.Background(), "sr-1", repoUpdateStatus("done")); err != nil {
		t.Fatalf("a failed revoke must not roll back the status write: %v", err)
	}
}
