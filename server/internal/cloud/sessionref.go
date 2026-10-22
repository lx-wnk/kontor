// Package cloud messages claude.ai cloud sessions through the claude CLI.
package cloud

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var (
	sessionIDPattern = regexp.MustCompile(`^(session|cse)_[A-Za-z0-9_-]{8,128}$`)

	// ErrInvalidSessionRef reports input that is neither a session ID nor a claude.ai/code URL.
	ErrInvalidSessionRef = errors.New("not a cloud session ID or https://claude.ai/code/<id> URL")
)

const (
	sessionURLHost       = "claude.ai"
	sessionURLPathPrefix = "/code/"
)

// ParseSessionRef returns the session ID from a bare ID or an
// https://claude.ai/code/<id> URL (optional trailing slash or query).
func ParseSessionRef(s string) (string, error) {
	if sessionIDPattern.MatchString(s) {
		return s, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", ErrInvalidSessionRef
	}
	// A bare "#" leaves Fragment empty, so the raw string is checked too.
	if u.Scheme != "https" || u.Host != sessionURLHost || u.User != nil ||
		u.Fragment != "" || strings.Contains(s, "#") || u.Opaque != "" {
		return "", ErrInvalidSessionRef
	}
	id, ok := strings.CutPrefix(strings.TrimSuffix(u.Path, "/"), sessionURLPathPrefix)
	if !ok || !sessionIDPattern.MatchString(id) {
		return "", ErrInvalidSessionRef
	}
	return id, nil
}
