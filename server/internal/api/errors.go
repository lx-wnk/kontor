// Package api provides HTTP handler utilities and middleware.
package api

import (
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// Sentinel errors — use errors.Is() to check.
var (
	ErrNotFound   = apierr.ErrNotFound
	ErrConflict   = apierr.ErrConflict
	ErrBadRequest = apierr.ErrBadRequest
	ErrForbidden  = apierr.ErrForbidden
)

// AppError carries an explicit HTTP status code.
// Use when a handler needs precise control over the response status.
type AppError = apierr.AppError

// NewAppError creates an AppError with the given status and message.
var NewAppError = apierr.NewAppError

// HandlerFunc is an HTTP handler that returns an error instead of writing it directly.
// Wrap with ErrorMiddleware to map errors to HTTP responses.
type HandlerFunc = apierr.HandlerFunc

// ErrorMiddleware wraps a HandlerFunc and maps errors to HTTP responses.
// This is the single place where domain errors become HTTP status codes.
var ErrorMiddleware = apierr.ErrorMiddleware
