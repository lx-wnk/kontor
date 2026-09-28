package kontorsession_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/kontorsession"
)

type fakeSessions struct {
	pid      int
	running  bool
	startErr error
	started  []string
	ended    []string
}

func (f *fakeSessions) Current(context.Context) (int, bool, error) { return f.pid, f.running, nil }
func (f *fakeSessions) Start(_ context.Context, p string) (int, bool, error) {
	if f.startErr != nil {
		return 0, false, f.startErr
	}
	if f.running {
		return f.pid, false, nil
	}
	f.started = append(f.started, p)
	f.pid, f.running = 7, true
	return 7, true, nil
}
func (f *fakeSessions) Renew(_ context.Context, p string) (int, error) {
	f.started = append(f.started, "renew:"+p)
	f.pid, f.running = 8, true
	return 8, nil
}
func (f *fakeSessions) End(_ context.Context, reason string) error {
	f.ended = append(f.ended, reason)
	f.running = false
	return nil
}

func do(f *fakeSessions, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	kontorsession.New(f).Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestGet_NoSessionIsNull(t *testing.T) {
	rec := do(&fakeSessions{}, http.MethodGet, "/api/kontor-session", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":null}`, rec.Body.String())
}

func TestPost_StartsWithThePrompt(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"hallo"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":7,"started":true}`, rec.Body.String())
	require.Equal(t, []string{"hallo"}, f.started)
	require.JSONEq(t, `{"pid":7}`, do(f, http.MethodGet, "/api/kontor-session", "").Body.String())
}

func TestPost_RunningSessionIsReturnedUnchanged(t *testing.T) {
	f := &fakeSessions{}
	require.Equal(t, http.StatusOK, do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"a"}`).Code)

	rec := do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"b"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":7,"started":false}`, rec.Body.String())
	require.Equal(t, []string{"a"}, f.started)
}

func TestPost_BlankPromptIsRejected(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"  "}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, f.started)
}

func TestPost_StartFailureCarriesTheMessage(t *testing.T) {
	rec := do(&fakeSessions{startErr: errors.New("mint failed")}, http.MethodPost, "/api/kontor-session", `{"prompt":"x"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "mint failed")
}

func TestRenew_ReplacesTheSession(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session/renew", `{"prompt":"neu"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":8,"started":true}`, rec.Body.String())
}

func TestRenew_AcceptsABlankPrompt(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session/renew", `{"prompt":""}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"renew:"}, f.started)
}

func TestDelete_EndsAndIsIdempotent(t *testing.T) {
	f := &fakeSessions{}
	require.Equal(t, http.StatusNoContent, do(f, http.MethodDelete, "/api/kontor-session", "").Code)
	require.Equal(t, []string{"ended by operator"}, f.ended)
}
