package parser_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAPIErrorEntry creates a JSONL assistant entry with optional isApiErrorMessage / apiErrorStatus fields.
func buildAPIErrorEntry(t *testing.T, text string, isAPIError bool, apiStatus int) string {
	t.Helper()
	contentBlock := map[string]any{"type": "text", "text": text}
	contentBytes, err := json.Marshal([]any{contentBlock})
	require.NoError(t, err)

	msg := map[string]any{
		"role":    "assistant",
		"model":   "claude-sonnet-4-6",
		"content": json.RawMessage(contentBytes),
		"usage":   map[string]int{"input_tokens": 10, "output_tokens": 5},
	}
	msgBytes, err := json.Marshal(msg)
	require.NoError(t, err)

	entry := map[string]any{
		"type":    "assistant",
		"message": json.RawMessage(msgBytes),
	}
	if isAPIError {
		entry["isApiErrorMessage"] = true
		entry["apiErrorStatus"] = apiStatus
	}
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(entryBytes)
}

func writeErrorStateSession(t *testing.T, assistantLine string) string {
	t.Helper()
	userLine := `{"type":"user","message":{"role":"user","content":"go"}}`
	f, err := os.CreateTemp(t.TempDir(), "session*.jsonl")
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join([]string{userLine, assistantLine}, "\n") + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// (a) Benign text mentioning "authentication" must NOT trigger an error state.
func TestErrorState_BenignAuthMentionNoFlag(t *testing.T) {
	line := buildAPIErrorEntry(t, "Does this look right? Then I'll spec it and plan it. I'll handle authentication next.", false, 0)
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Empty(t, d.ErrorState, "benign text with 'authentication' must not set ErrorState")
}

// (b) isApiErrorMessage:true + status 401 → ErrorStateAuthFailed.
func TestErrorState_APIError401(t *testing.T) {
	line := buildAPIErrorEntry(t, "Unauthorized", true, 401)
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateAuthFailed, d.ErrorState)
}

// (c) isApiErrorMessage:true + status 429 + quota text → ErrorStateQuotaExhausted.
func TestErrorState_APIError429Quota(t *testing.T) {
	line := buildAPIErrorEntry(t, "You have exceeded your usage limit for this month.", true, 429)
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateQuotaExhausted, d.ErrorState)
}

// (d) isApiErrorMessage:true + status 429 + plain text → ErrorStateRateLimited.
func TestErrorState_APIError429RateLimit(t *testing.T) {
	line := buildAPIErrorEntry(t, "Please slow down.", true, 429)
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateRateLimited, d.ErrorState)
}

// The CLI's own synthetic auth error carries no apiErrorStatus and a text the
// keyword fallback does not match; only the structured error field names it.
func TestErrorState_SyntheticLoginExpired(t *testing.T) {
	line := `{"type":"assistant","error":"authentication_failed","isApiErrorMessage":true,` +
		`"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Login expired · Please run /login"}]}}`
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateAuthFailed, d.ErrorState)
}

// The last assistant entry decides: a normal reply after /login clears the
// auth error, or the dashboard keeps asking for a login that already happened.
func TestErrorState_ClearedByLaterReply(t *testing.T) {
	failed := `{"type":"assistant","error":"authentication_failed","isApiErrorMessage":true,` +
		`"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Login expired · Please run /login"}]}}`
	path := writeErrorStateSession(t, failed+"\n"+
		`{"type":"user","message":{"role":"user","content":"go on"}}`+"\n"+
		buildAPIErrorEntry(t, "Done.", false, 0))
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Empty(t, d.ErrorState)
}

func TestErrorState_OAuthRefreshFailure(t *testing.T) {
	line := `{"type":"assistant","error":"authentication_failed","isApiErrorMessage":true,` +
		`"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Failed to authenticate: OAuth session expired and could not be refreshed"}]}}`
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateAuthFailed, d.ErrorState)
}

func TestErrorState_OrgNotAllowed(t *testing.T) {
	line := `{"type":"assistant","error":"oauth_org_not_allowed","isApiErrorMessage":true,` +
		`"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"Your organization does not allow this login"}]}}`
	path := writeErrorStateSession(t, line)
	d, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, sdk.ErrorStateAuthFailed, d.ErrorState)
}
