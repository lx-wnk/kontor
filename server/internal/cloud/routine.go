package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	RoutineBetaHeader = "experimental-cc-routine-2026-04-01"
	AnthropicVersion  = "2023-06-01"

	routineTimeout = 20 * time.Second
	// maxFireErrorBody bounds the response body copied into a FireError.
	maxFireErrorBody = 4096
	maxFireResponse  = 1 << 20
	redactedToken    = "[redacted]"
)

var (
	fireURLPattern = regexp.MustCompile(`^https://api\.anthropic\.com/v1/claude_code/routines/trig_[A-Za-z0-9]+/fire$`)

	ErrInvalidFireURL = errors.New("not a routine fire URL: https://api.anthropic.com/v1/claude_code/routines/trig_<id>/fire")
)

// FireError reports a non-2xx answer from the fire endpoint.
type FireError struct {
	Status int
	Body   string
}

func (e *FireError) Error() string {
	return fmt.Sprintf("routine fire failed with HTTP %d", e.Status)
}

type FireResult struct {
	SessionID string
	URL       string
}

// RoutineClient fires routines. A zero HTTP uses a client with a 20s timeout.
type RoutineClient struct {
	HTTP *http.Client
}

func ValidateFireURL(s string) error {
	if !fireURLPattern.MatchString(s) {
		return ErrInvalidFireURL
	}
	return nil
}

// Fire posts to fireURL with the routine token. The token goes nowhere else:
// the URL is validated first and redirects are never followed.
func (c RoutineClient) Fire(ctx context.Context, fireURL, token, text string) (FireResult, error) {
	if err := ValidateFireURL(fireURL); err != nil {
		return FireResult{}, err
	}
	if token == "" {
		return FireResult{}, errors.New("routine token is empty")
	}

	var payload []byte
	if text != "" {
		var err error
		if payload, err = json.Marshal(map[string]string{"text": text}); err != nil {
			return FireResult{}, fmt.Errorf("encode routine text: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fireURL, bytes.NewReader(payload))
	if err != nil {
		return FireResult{}, errors.New("build routine request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", RoutineBetaHeader)
	req.Header.Set("anthropic-version", AnthropicVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client().Do(req)
	if err != nil {
		// url.Error carries only the validated URL; the cause is dropped to keep the token out of the chain.
		return FireResult{}, errors.New("routine fire request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return FireResult{}, &FireError{Status: resp.StatusCode, Body: readErrorBody(resp.Body, token)}
	}

	var decoded struct {
		SessionID  string `json:"claude_code_session_id"`
		SessionURL string `json:"claude_code_session_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxFireResponse)).Decode(&decoded); err != nil {
		return FireResult{}, errors.New("routine fire response is not valid JSON")
	}
	if decoded.SessionID == "" {
		return FireResult{}, errors.New("routine fire response has no session id")
	}
	return FireResult{SessionID: decoded.SessionID, URL: decoded.SessionURL}, nil
}

func (c RoutineClient) client() *http.Client {
	hc := http.Client{Timeout: routineTimeout}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

// readErrorBody reads past the cap by the token's length so a token echoed at
// the cut is redacted whole before the body is truncated.
func readErrorBody(r io.Reader, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(r, int64(maxFireErrorBody+len(token))))
	body := strings.ReplaceAll(string(raw), token, redactedToken)
	if len(body) > maxFireErrorBody {
		body = body[:maxFireErrorBody]
	}
	return body
}
