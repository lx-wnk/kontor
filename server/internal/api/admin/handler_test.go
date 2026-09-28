package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/admin"
)

type fakeValidator struct{ err error }

func (f fakeValidator) Validate(context.Context) error { return f.err }

// fakeRestarter is the Restarter fake shared by every test in this package.
// Reexec/Exit are never expected to fire from the handler — the actual
// re-exec happens later, in the run loop, once the restart signal (trigger)
// has been consumed.
type fakeRestarter struct {
	reexec, exit, built int
	buildOutput         []byte
	buildErr            error
}

func (f *fakeRestarter) Reexec() error { f.reexec++; return nil }
func (f *fakeRestarter) Exit()         { f.exit++ }
func (f *fakeRestarter) Build(context.Context) ([]byte, error) {
	f.built++
	return f.buildOutput, f.buildErr
}

func mount(h *admin.Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func TestRestartReturns202OnSuccess(t *testing.T) {
	triggered := make(chan struct{}, 1)
	h := admin.New(fakeValidator{}, "reexec", &fakeRestarter{}, func() { triggered <- struct{}{} })
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))

	require.Equal(t, http.StatusAccepted, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "restarting", body["status"])
	require.Equal(t, "reexec", body["mode"])
	select {
	case <-triggered:
	default:
		t.Fatal("expected restart trigger to fire")
	}
}

func TestRestartReturns409WhenValidatorFails(t *testing.T) {
	h := admin.New(fakeValidator{err: errors.New("auth would lock out")}, "reexec", &fakeRestarter{}, func() { t.Fatal("must not trigger") })
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/restart", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
}

// TestRestartRebuild covers the optional {"rebuild": true} body: absent (or
// malformed, or explicitly false) must reproduce today's behaviour
// byte-for-byte; true must build before signalling restart, and a build
// failure must return 500 with the build output and never signal restart at
// all — the case that actually matters, since it is what keeps the old
// binary serving instead of not existing.
func TestRestartRebuild(t *testing.T) {
	cases := []struct {
		name        string
		body        []byte
		buildOutput []byte
		buildErr    error
		wantStatus  int
		wantBuilt   int
		wantTrigger bool
	}{
		{
			name:        "absent body restarts without building",
			body:        nil,
			wantStatus:  http.StatusAccepted,
			wantBuilt:   0,
			wantTrigger: true,
		},
		{
			name:        "malformed body restarts without building",
			body:        []byte("{not json"),
			wantStatus:  http.StatusAccepted,
			wantBuilt:   0,
			wantTrigger: true,
		},
		{
			name:        "rebuild false restarts without building",
			body:        []byte(`{"rebuild": false}`),
			wantStatus:  http.StatusAccepted,
			wantBuilt:   0,
			wantTrigger: true,
		},
		{
			name:        "rebuild true and build succeeds restarts",
			body:        []byte(`{"rebuild": true}`),
			buildOutput: []byte("go: building\n"),
			wantStatus:  http.StatusAccepted,
			wantBuilt:   1,
			wantTrigger: true,
		},
		{
			name:        "rebuild true and build fails returns 500 without restarting",
			body:        []byte(`{"rebuild": true}`),
			buildOutput: []byte("./main.go:12: undefined: foo\n"),
			buildErr:    errors.New("exit status 1"),
			wantStatus:  http.StatusInternalServerError,
			wantBuilt:   1,
			wantTrigger: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restarter := &fakeRestarter{buildOutput: tc.buildOutput, buildErr: tc.buildErr}
			triggered := make(chan struct{}, 1)
			h := admin.New(fakeValidator{}, "reexec", restarter, func() { triggered <- struct{}{} })

			var body *bytes.Reader
			if tc.body == nil {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader(tc.body)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/admin/restart", body)
			rec := httptest.NewRecorder()
			mount(h).ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantBuilt, restarter.built)
			require.Equal(t, 0, restarter.reexec, "handler must never call Reexec directly")

			select {
			case <-triggered:
				require.True(t, tc.wantTrigger, "trigger fired but was not expected")
			default:
				require.False(t, tc.wantTrigger, "expected restart trigger to fire")
			}

			if tc.wantStatus == http.StatusInternalServerError {
				var respBody map[string]string
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
				require.NotEmpty(t, respBody["error"])
				require.Contains(t, respBody["output"], "undefined: foo")
			}
		})
	}
}
