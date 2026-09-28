package capabilities

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/askgate"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/serverask"
)

// fakeResolver records what it was handed and returns a fixed error.
type fakeResolver struct {
	called   bool
	gotID    string
	gotDecs  string
	pending  serverask.Pending
	returnEr error
}

func (f *fakeResolver) Resolve(id, decision string) (serverask.Pending, error) {
	f.called = true
	f.gotID = id
	f.gotDecs = decision
	return f.pending, f.returnEr
}

func doRequest(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/capabilities/decisions/respond", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Respond(rec, req)
	return rec
}

func TestRespond_AllowAndDenyReachResolverVerbatim(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			resolver := &fakeResolver{}
			h := New(resolver, nil)
			rec := doRequest(t, h, `{"id":"abc-123","decision":"`+decision+`"}`)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
			if !resolver.called {
				t.Fatal("resolver was not called")
			}
			if resolver.gotID != "abc-123" || resolver.gotDecs != decision {
				t.Fatalf("resolver got (%q, %q), want (%q, %q)", resolver.gotID, resolver.gotDecs, "abc-123", decision)
			}
		})
	}
}

func TestRespond_UnknownDecisionRejectedWithoutCallingResolver(t *testing.T) {
	resolver := &fakeResolver{}
	h := New(resolver, nil)
	rec := doRequest(t, h, `{"id":"abc-123","decision":"maybe"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if resolver.called {
		t.Fatal("resolver was called for an unknown decision — validation must happen at the boundary")
	}
}

func TestRespond_ErrNotPendingMapsTo404(t *testing.T) {
	resolver := &fakeResolver{returnEr: askgate.ErrNotPending}
	h := New(resolver, nil)
	rec := doRequest(t, h, `{"id":"abc-123","decision":"allow"}`)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRespond_ErrInvalidDecisionMapsTo400(t *testing.T) {
	resolver := &fakeResolver{returnEr: serverask.ErrInvalidDecision}
	h := New(resolver, nil)
	rec := doRequest(t, h, `{"id":"abc-123","decision":"allow"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestRespond_UnexpectedErrorMapsTo500WithoutLeakingText(t *testing.T) {
	secret := "some sensitive internal detail"
	resolver := &fakeResolver{returnEr: errors.New(secret)}
	h := New(resolver, nil)
	rec := doRequest(t, h, `{"id":"abc-123","decision":"allow"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("response body leaked the internal error text: %s", rec.Body.String())
	}
}

func TestRespond_MalformedJSONIs400(t *testing.T) {
	resolver := &fakeResolver{}
	h := New(resolver, nil)
	rec := doRequest(t, h, `{not json`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if resolver.called {
		t.Fatal("resolver was called for malformed JSON")
	}
}

// fakeRecorder captures the audit call and can fail on demand.
type fakeRecorder struct {
	calls    int
	userID   *string
	action   string
	target   string
	metadata map[string]any
	returnEr error
}

func (f *fakeRecorder) RecordAudit(_ context.Context, userID *string, action, target string, metadata map[string]any) error {
	f.calls++
	f.userID = userID
	f.action = action
	f.target = target
	f.metadata = metadata
	return f.returnEr
}

func TestRespond_RecordsWhatWasDecided(t *testing.T) {
	for _, tc := range []struct {
		decision string
		want     string
	}{
		{"allow", repo.AuditActionCapabilityAllow},
		{"deny", repo.AuditActionCapabilityDeny},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			resolver := &fakeResolver{pending: serverask.Pending{
				Capability: "memory.write",
				Value:      "space:global",
				Context:    "project /x",
				Reason:     "no matching grant",
			}}
			rec := &fakeRecorder{}
			h := New(resolver, rec)

			res := doRequest(t, h, `{"id":"abc-123","decision":"`+tc.decision+`"}`)

			if res.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
			}
			if rec.calls != 1 {
				t.Fatalf("RecordAudit called %d times, want 1", rec.calls)
			}
			if rec.action != tc.want {
				t.Errorf("action = %q, want %q", rec.action, tc.want)
			}
			if rec.target != "memory.write" {
				t.Errorf("target = %q, want the capability name", rec.target)
			}
			for key, want := range map[string]string{
				"value":   "space:global",
				"context": "project /x",
				"reason":  "no matching grant",
			} {
				if got, _ := rec.metadata[key].(string); got != want {
					t.Errorf("metadata[%q] = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestRespond_AuditFailureDoesNotUndoTheDecision(t *testing.T) {
	resolver := &fakeResolver{pending: serverask.Pending{Capability: "memory.read"}}
	rec := &fakeRecorder{returnEr: errors.New("database is locked")}
	h := New(resolver, rec)

	res := doRequest(t, h, `{"id":"abc-123","decision":"allow"}`)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d — the decision had already taken effect", res.Code, http.StatusNoContent)
	}
	if !resolver.called {
		t.Error("resolver was not called")
	}
}

func TestRespond_NotPendingIsNotAudited(t *testing.T) {
	resolver := &fakeResolver{returnEr: askgate.ErrNotPending}
	rec := &fakeRecorder{}
	h := New(resolver, rec)

	res := doRequest(t, h, `{"id":"gone","decision":"allow"}`)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNotFound)
	}
	if rec.calls != 0 {
		t.Errorf("RecordAudit called %d times, want 0 — nothing was decided", rec.calls)
	}
}
