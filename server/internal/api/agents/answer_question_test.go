package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int { return &i }

func TestEncodeAnswer_Single(t *testing.T) {
	tokens, err := EncodeAnswer(AnswerIntent{Mode: "single", Index: intPtr(0)})
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, tokens)
}

func TestEncodeAnswer_Multi(t *testing.T) {
	tokens, err := EncodeAnswer(AnswerIntent{Mode: "multi", Indices: []int{0, 2}})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "3", "\t", "\r"}, tokens)
}

func TestEncodeAnswer_Custom(t *testing.T) {
	tokens, err := EncodeAnswer(AnswerIntent{Mode: "custom", OptionCount: intPtr(3), Text: "foo"})
	require.NoError(t, err)
	assert.Equal(t, []string{"4", "foo", "\r"}, tokens)
}

func TestEncodeAnswer_Chat(t *testing.T) {
	tokens, err := EncodeAnswer(AnswerIntent{Mode: "chat", OptionCount: intPtr(3), Text: "bar"})
	require.NoError(t, err)
	assert.Equal(t, []string{"5", "bar", "\r"}, tokens)
}

func TestEncodeAnswer_RejectsMissingFields(t *testing.T) {
	_, err := EncodeAnswer(AnswerIntent{Mode: "single"})
	assert.Error(t, err)

	_, err = EncodeAnswer(AnswerIntent{Mode: "multi"})
	assert.Error(t, err)

	_, err = EncodeAnswer(AnswerIntent{Mode: "custom", OptionCount: intPtr(1)})
	assert.Error(t, err)

	_, err = EncodeAnswer(AnswerIntent{Mode: "chat", Text: "x"})
	assert.Error(t, err)

	_, err = EncodeAnswer(AnswerIntent{Mode: "bogus"})
	assert.Error(t, err)
}

func newAnswerRequest(t *testing.T, pid string, body any) *http.Request {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+pid+"/answer-question", bytes.NewReader(data))
	req.SetPathValue("pid", pid)
	return req
}

func TestAnswerQuestion_BadBody(t *testing.T) {
	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/123/answer-question", bytes.NewReader([]byte("not json")))
	req.SetPathValue("pid", "123")
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAnswerQuestion_BadMode(t *testing.T) {
	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, "123", map[string]any{"mode": "single"}) // missing index
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAnswerQuestion_InvalidPID(t *testing.T) {
	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, "not-a-number", map[string]any{"mode": "single", "index": 0})
	req.SetPathValue("pid", "not-a-number")
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAnswerQuestion_NoChannel_Returns409(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, "99999", map[string]any{"mode": "single", "index": 0})
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

// TestAnswerQuestion_SingleSelect_DeliversBareDigitOverPty verifies the full
// handler round trip for a single-select answer: the pty broker receives
// exactly the raw digit, no trailing CR (single-select submits instantly).
func TestAnswerQuestion_SingleSelect_DeliversBareDigitOverPty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	var gotBody string
	var gotToken string
	brokerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/keys" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		gotToken = r.Header.Get("Authorization")
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer brokerSrv.Close()
	port := brokerSrv.Listener.Addr().(*net.TCPAddr).Port

	pid := 33101
	writeDiscoveryFile(t, dir, fmt.Sprintf("%d.pty.json", pid), map[string]any{
		"port":      port,
		"token":     "pty-secret",
		"ptyInject": true,
	})

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, fmt.Sprintf("%d", pid), map[string]any{"mode": "single", "index": 0})
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&respBody))
	assert.Equal(t, "pty", respBody["transport"])
	assert.Equal(t, "Bearer pty-secret", gotToken)
	assert.Equal(t, "1", gotBody, "single-select must deliver a bare digit, no CR")
}

// TestAnswerQuestion_MultiSelect_DeliversDigitsTabEnterOverPty verifies a
// multi-select answer is delivered as the concatenated digit+Tab+Enter byte
// sequence, matching encodeAnswer's multi-mode tokens joined with no separator.
func TestAnswerQuestion_MultiSelect_DeliversDigitsTabEnterOverPty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	var gotBody string
	brokerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		gotBody = buf.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer brokerSrv.Close()
	port := brokerSrv.Listener.Addr().(*net.TCPAddr).Port

	pid := 33102
	writeDiscoveryFile(t, dir, fmt.Sprintf("%d.pty.json", pid), map[string]any{
		"port":      port,
		"token":     "pty-secret",
		"ptyInject": true,
	})

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, fmt.Sprintf("%d", pid), map[string]any{"mode": "multi", "indices": []int{0, 2}})
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "13\t\r", gotBody)
}

// TestAnswerQuestion_DeliversOverTmux verifies tmux delivery: each token is a
// separate send-keys invocation, with "\t"/"\r" translated to Tab/Enter and
// everything else sent literally.
func TestAnswerQuestion_DeliversOverTmux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	pid := 33103
	writeDiscoveryFile(t, dir, fmt.Sprintf("%d.json", pid), map[string]any{
		"port":       9999,
		"token":      "bridge-secret",
		"tmuxPane":   "%3",
		"tmuxSocket": "",
	})

	var calls [][]string
	origRun, origLook := tmuxRunner, tmuxLookPath
	t.Cleanup(func() { tmuxRunner = origRun; tmuxLookPath = origLook })
	tmuxLookPath = func() (string, error) { return "/usr/bin/tmux", nil }
	tmuxRunner = func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	h := NewAnswerQuestionHandler(m)

	req := newAnswerRequest(t, fmt.Sprintf("%d", pid), map[string]any{"mode": "multi", "indices": []int{0, 2}})
	rec := httptest.NewRecorder()
	h.AnswerQuestion(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var respBody map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&respBody))
	assert.Equal(t, "tmux", respBody["transport"])

	// tokens for multi{indices:[0,2]} are ["1","3","\t","\r"]: 4 separate
	// send-keys invocations, with Tab/Enter as named keys and digits literal.
	require.Len(t, calls, 4)
	assert.Equal(t, "-l", calls[0][len(calls[0])-3], "digit '1' sent literally")
	assert.Equal(t, "1", calls[0][len(calls[0])-1])
	assert.Equal(t, "3", calls[1][len(calls[1])-1])
	assert.Equal(t, "Tab", calls[2][len(calls[2])-1])
	assert.Equal(t, "Enter", calls[3][len(calls[3])-1])
}
