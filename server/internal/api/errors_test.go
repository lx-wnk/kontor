package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api"
	"github.com/stretchr/testify/require"
)

func TestErrorMiddleware_NotFound(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return api.ErrNotFound
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestErrorMiddleware_AppError(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return api.NewAppError(http.StatusTeapot, "I am a teapot")
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusTeapot, rec.Code)
}

func TestErrorMiddleware_Unknown(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return errors.New("boom")
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestErrorMiddleware_NoError(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(http.StatusOK)
		return nil
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestErrorMiddleware_Conflict(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return api.ErrConflict
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestErrorMiddleware_BadRequest(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return api.ErrBadRequest
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestErrorMiddleware_Forbidden(t *testing.T) {
	handler := api.ErrorMiddleware(func(w http.ResponseWriter, r *http.Request) error {
		return api.ErrForbidden
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
