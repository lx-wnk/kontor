package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// AnswerIntent mirrors src/utils/answerKeys.ts AnswerIntent: the caller's
// intended answer to a detected AskUserQuestion modal, before it is encoded
// into raw keystroke tokens. Index/OptionCount are pointers so an absent
// field (JSON key omitted) is distinguishable from an explicit 0.
type AnswerIntent struct {
	Mode        string `json:"mode"`
	Index       *int   `json:"index,omitempty"`
	Indices     []int  `json:"indices,omitempty"`
	OptionCount *int   `json:"optionCount,omitempty"`
	Text        string `json:"text,omitempty"`
}

// EncodeAnswer ports src/utils/answerKeys.ts encodeAnswer(): it translates an
// AnswerIntent into the raw keystroke token sequence the TUI question
// selector expects. Index/Indices are zero-based option positions — the
// emitted digit is position+1, the number shown on screen. Single-select
// emits only the digit (no Enter; the selector submits instantly on that
// keypress). Tokens are plain strings: digits, literal text, "\t" (Tab), or
// "\r" (Enter).
func EncodeAnswer(intent AnswerIntent) ([]string, error) {
	switch intent.Mode {
	case "single":
		if intent.Index == nil {
			return nil, fmt.Errorf("mode single requires index")
		}
		return []string{strconv.Itoa(*intent.Index + 1)}, nil
	case "multi":
		if len(intent.Indices) == 0 {
			return nil, fmt.Errorf("mode multi requires a non-empty indices")
		}
		tokens := make([]string, 0, len(intent.Indices)+2)
		for _, i := range intent.Indices {
			tokens = append(tokens, strconv.Itoa(i+1))
		}
		return append(tokens, "\t", "\r"), nil
	case "custom":
		if intent.OptionCount == nil {
			return nil, fmt.Errorf("mode custom requires optionCount")
		}
		if intent.Text == "" {
			return nil, fmt.Errorf("mode custom requires text")
		}
		return []string{strconv.Itoa(*intent.OptionCount + 1), intent.Text, "\r"}, nil
	case "chat":
		if intent.OptionCount == nil {
			return nil, fmt.Errorf("mode chat requires optionCount")
		}
		if intent.Text == "" {
			return nil, fmt.Errorf("mode chat requires text")
		}
		return []string{strconv.Itoa(*intent.OptionCount + 2), intent.Text, "\r"}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q", intent.Mode)
	}
}

// ErrNoAnswerChannel indicates the session identified by pid has neither a
// tmux pane nor a pty-broker channel to deliver answer keystrokes to.
var ErrNoAnswerChannel = errors.New("no answer channel available for this session")

// AnswerQuestionHandler handles POST /api/agents/{pid}/answer-question: it
// answers a detected AskUserQuestion modal by driving the session's TUI
// selector with the proven keystroke sequence for the given intent.
type AnswerQuestionHandler struct {
	manager *SpawnManager
}

// NewAnswerQuestionHandler creates an AnswerQuestionHandler backed by manager.
func NewAnswerQuestionHandler(manager *SpawnManager) *AnswerQuestionHandler {
	return &AnswerQuestionHandler{manager: manager}
}

// AnswerQuestion handles POST /api/agents/{pid}/answer-question. The request
// body is an AnswerIntent; on success it responds 200 with the transport used
// ("pty" or "tmux"). 400 on an invalid pid, unparsable body, or an intent that
// EncodeAnswer rejects. 409 when the session has no live answer channel.
func (h *AnswerQuestionHandler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		apierr.JSONError(w, http.StatusBadRequest, "invalid pid")
		return
	}

	var intent AnswerIntent
	if err := json.NewDecoder(r.Body).Decode(&intent); err != nil {
		apierr.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	tokens, err := EncodeAnswer(intent)
	if err != nil {
		apierr.JSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	transport, err := h.manager.SendAnswerKeys(r.Context(), pid, tokens)
	if err != nil {
		if errors.Is(err, ErrNoAnswerChannel) {
			apierr.JSONError(w, http.StatusConflict, err.Error())
			return
		}
		apierr.JSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	apierr.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "transport": transport})
}
