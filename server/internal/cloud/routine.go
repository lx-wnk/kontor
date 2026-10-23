package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	RoutineBetaHeader = "experimental-cc-routine-2026-04-01"
	AnthropicVersion  = "2023-06-01"

	routineTimeout   = 20 * time.Second
	maxFireErrorBody = 4096
	maxFireResponse  = 1 << 20
	redactedToken    = "[redacted]"
	// minLeakWindow is the shortest token fragment a FireError body may not contain.
	minLeakWindow = 8
)

var (
	fireURLPattern = regexp.MustCompile(`^https://api\.anthropic\.com/v1/claude_code/routines/trig_[A-Za-z0-9]+/fire$`)

	ErrUnconfirmed    = errors.New("the routine may have started a run")
	ErrInvalidFireURL = errors.New("not a routine fire URL: https://api.anthropic.com/v1/claude_code/routines/trig_<id>/fire")
)

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

// RoutineClient fires routines with a timeout of at most 20s, also when HTTP sets none or a longer one.
type RoutineClient struct {
	HTTP *http.Client
}

func ValidateFireURL(s string) error {
	if !fireURLPattern.MatchString(s) {
		return ErrInvalidFireURL
	}
	return nil
}

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
	// GotConn runs in Do's goroutine before any request byte leaves; WroteRequest can fire after Do returns (HTTP/2).
	var connected atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
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
		if connected.Load() {
			return FireResult{}, fmt.Errorf("%w: %w", classifyTransportError(err), ErrUnconfirmed)
		}
		return FireResult{}, classifyTransportError(err)
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
		return FireResult{}, fmt.Errorf("routine fire response is not valid JSON: %w", ErrUnconfirmed)
	}
	if decoded.SessionID == "" {
		return FireResult{}, fmt.Errorf("routine fire response has no session id: %w", ErrUnconfirmed)
	}
	return FireResult{SessionID: decoded.SessionID, URL: decoded.SessionURL}, nil
}

func (c RoutineClient) client() *http.Client {
	var hc http.Client
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	if hc.Timeout == 0 || hc.Timeout > routineTimeout {
		hc.Timeout = routineTimeout
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

// classifyTransportError keeps the cause kind, never the cause text: a transport error may echo request details.
func classifyTransportError(err error) error {
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("routine fire canceled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Errorf("routine fire timed out: %w: %w", context.DeadlineExceeded, os.ErrDeadlineExceeded)
	default:
		return errors.New("routine fire request failed")
	}
}

func readErrorBody(r io.Reader, token string) string {
	raw, _ := io.ReadAll(io.LimitReader(r, maxFireErrorBody))
	return capRedactedBody(redactToken(string(raw), token))
}

// capRedactedBody cuts body to maxFireErrorBody bytes; redaction can grow it past
// the read cap. A rune or marker the cut would split is dropped whole.
func capRedactedBody(body string) string {
	if len(body) <= maxFireErrorBody {
		return body
	}
	cut := maxFireErrorBody
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	body = body[:cut]
	for n := len(redactedToken) - 1; n > 0; n-- {
		if strings.HasSuffix(body, redactedToken[:n]) {
			return body[:len(body)-n]
		}
	}
	return body
}

// redactToken masks every minLeakWindow-byte window of body that occurs in the
// token, plus a token prefix left dangling at the end by the read cap.
func redactToken(body, token string) string {
	window := min(minLeakWindow, len(token))
	masked := make([]bool, len(body))
	for i := 0; i+window <= len(body); i++ {
		if strings.Contains(token, body[i:i+window]) {
			for j := i; j < i+window; j++ {
				masked[j] = true
			}
		}
	}
	for n := min(len(token)-1, len(body)); n > 0; n-- {
		if strings.HasSuffix(body, token[:n]) {
			for j := len(body) - n; j < len(body); j++ {
				masked[j] = true
			}
			break
		}
	}

	var out strings.Builder
	for i := 0; i < len(body); i++ {
		if !masked[i] {
			out.WriteByte(body[i])
		} else if i == 0 || !masked[i-1] {
			out.WriteString(redactedToken)
		}
	}
	return out.String()
}
