package parser

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/scanner"
)

const (
	maxSessions  = 100
	maxReadBytes = 10 * 1024 * 1024 // 10 MB cap for ParseFullSession
)

// SessionInfo is the session-list entry returned by GET /api/sessions.
type SessionInfo struct {
	SessionID         string  `json:"sessionId"`
	ProjectPath       string  `json:"projectPath"`
	ProjectName       string  `json:"projectName"`
	LastModified      string  `json:"lastModified"` // ISO 8601
	Model             *string `json:"model"`
	FirstPrompt       *string `json:"firstPrompt"`
	LastResponse      *string `json:"lastResponse"`
	TotalInputTokens  int     `json:"totalInputTokens"`
	TotalOutputTokens int     `json:"totalOutputTokens"`
	CostEstimate      float64 `json:"costEstimate"`
	IsRunning         bool    `json:"isRunning"`
}

// OutputMessage is a single displayable message from a session transcript.
type OutputMessage struct {
	Role         string  `json:"role"` // assistant | tool_call | tool_result | human | task | subagent
	Content      string  `json:"content"`
	Timestamp    *string `json:"timestamp,omitempty"`
	ToolName     *string `json:"toolName,omitempty"`
	FilePath     *string `json:"filePath,omitempty"`
	TaskStatus   *string `json:"taskStatus,omitempty"`
	TaskID       *string `json:"taskId,omitempty"`
	SubagentType *string `json:"subagentType,omitempty"`
	Queued       bool    `json:"queued,omitempty"`
}

type jsonlFileEntry struct {
	sessionID         string
	filePath          string
	projectDirEncoded string
	mtime             time.Time
}

// GetSessions scans all known Claude config directories and returns the 100 most
// recently modified sessions enriched with token counts, model, and first/last content.
func GetSessions(ctx context.Context) ([]SessionInfo, error) {
	// Collect all .jsonl files
	seenSessions := make(map[string]bool) // deduplicate by sessionID
	var allFiles []jsonlFileEntry

	for _, configDir := range allClaudeConfigDirs() {
		projectsDir := filepath.Join(configDir, "projects")
		projectDirs, err := os.ReadDir(projectsDir)
		if err != nil {
			continue
		}

		for _, dirEntry := range projectDirs {
			if !dirEntry.IsDir() {
				continue
			}
			dirPath := filepath.Join(projectsDir, dirEntry.Name())
			files, err := os.ReadDir(dirPath)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				sessionID := strings.TrimSuffix(f.Name(), ".jsonl")
				if !uuidRE.MatchString(sessionID) || seenSessions[sessionID] {
					continue
				}
				seenSessions[sessionID] = true
				fullPath := filepath.Join(dirPath, f.Name())
				info, err := f.Info()
				if err != nil {
					continue
				}
				allFiles = append(allFiles, jsonlFileEntry{
					sessionID:         sessionID,
					filePath:          fullPath,
					projectDirEncoded: dirEntry.Name(),
					mtime:             info.ModTime(),
				})
			}
		}
	} // end configDir loop

	// Sort newest first, cap at maxSessions
	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].mtime.After(allFiles[j].mtime)
	})
	if len(allFiles) > maxSessions {
		allFiles = allFiles[:maxSessions]
	}

	// Determine running CWD set
	runningEncoded := make(map[string]bool)
	if procs, err := scanner.ScanProcesses(ctx); err == nil {
		for _, p := range procs {
			runningEncoded[EncodePath(p.CWD)] = true
		}
	}

	sessions := make([]SessionInfo, 0, len(allFiles))
	for _, entry := range allFiles {
		si := buildSessionInfo(entry, runningEncoded)
		sessions = append(sessions, si)
	}
	return sessions, nil
}

func buildSessionInfo(entry jsonlFileEntry, runningEncoded map[string]bool) SessionInfo {
	meta := loadSessionMeta(entry.sessionID)

	var model *string
	var projectPath string
	var lastResponse *string

	// Head-read for model + cwd; tail-read for last assistant response.
	// cwd is written on every JSONL entry, but the first `model` only
	// appears on the first assistant turn — which can sit past the 8KB
	// head window on sessions that open with large attachments/prompts.
	// So capture cwd independently of model (do NOT gate cwd on model),
	// otherwise a deep-model session falls back to the encoded dir path
	// and loses its real project name + cost.
	if headRaw, err := headRead(entry.filePath); err == nil {
		m, cwd := extractHeadInfoRaw(headRaw)
		if m != "" {
			model = &m
		}
		if cwd != "" {
			projectPath = cwd
		}
	}
	if projectPath == "" {
		projectPath = entry.projectDirEncoded
	}

	if tailRaw, err := TailRead(entry.filePath); err == nil {
		if resp := extractLastAssistantText(tailRaw); resp != "" {
			scrubbed := scrubSecrets(resp)
			lastResponse = &scrubbed
		}
		// The model is stable within a session; if the head window missed
		// it (assistant turn beyond 8KB), recover it from the tail so cost
		// estimation and model-based grouping still work.
		if model == nil {
			if m, _ := extractHeadInfoRaw(tailRaw); m != "" {
				model = &m
			}
		}
	}

	var inputTokens, outputTokens int
	var firstPrompt *string
	if meta != nil {
		inputTokens = meta.InputTokens
		outputTokens = meta.OutputTokens
		if meta.FirstPrompt != "" {
			scrubbed := scrubSecrets(meta.FirstPrompt)
			firstPrompt = &scrubbed
		}
	}

	var costEstimate float64
	if model != nil {
		costEstimate = EstimateCost(sdk.TokenUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		}, *model)
	}

	return SessionInfo{
		SessionID:         entry.sessionID,
		ProjectPath:       projectPath,
		ProjectName:       filepath.Base(projectPath),
		LastModified:      entry.mtime.UTC().Format(time.RFC3339),
		Model:             model,
		FirstPrompt:       firstPrompt,
		LastResponse:      lastResponse,
		TotalInputTokens:  inputTokens,
		TotalOutputTokens: outputTokens,
		CostEstimate:      costEstimate,
		IsRunning:         runningEncoded[entry.projectDirEncoded],
	}
}

// headRead reads up to 8KB from the start of a file.
func headRead(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := io.ReadAtLeast(f, buf, 1)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// headEntry is the minimal JSONL structure for head-reads (model + cwd).
type headEntry struct {
	Type    string `json:"type"`
	CWD     string `json:"cwd"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

func extractHeadInfoRaw(raw string) (model, cwd string) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e headEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if model == "" && e.Message.Model != "" {
			model = e.Message.Model
		}
		if cwd == "" && e.CWD != "" {
			cwd = e.CWD
		}
		if model != "" && cwd != "" {
			break
		}
	}
	return
}

// extractLastAssistantText returns the last non-empty assistant text block
// from a raw JSONL string, truncated to 1000 chars.
func extractLastAssistantText(raw string) string {
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type msgBody struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type entry struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}

	var last string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil || (e.Type != "assistant" && e.Type != "message") {
			continue
		}
		var msg msgBody
		if err := json.Unmarshal(e.Message, &msg); err != nil || msg.Role != "assistant" {
			continue
		}
		var blocks []contentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		if text := strings.TrimSpace(sb.String()); text != "" {
			last = text
		}
	}
	if len(last) > 1000 {
		last = last[:1000]
	}
	return last
}

// locateTranscript finds the JSONL of a session across all candidate config
// dirs: <projects>/<encoded>/<id>.jsonl for a main session, and
// <projects>/<encoded>/<parent>/subagents/<id>.jsonl for a subagent, whose id is
// its own file name (see merger.buildSubagents). Callers validate the id against
// uuidRE first, so no caller-supplied text ever reaches a path segment.
func locateTranscript(sessionID string) string {
	name := sessionID + ".jsonl"
	for _, configDir := range allClaudeConfigDirs() {
		projectsDir := filepath.Join(configDir, "projects")
		projects, err := os.ReadDir(projectsDir)
		if err != nil {
			continue
		}
		for _, p := range projects {
			if !p.IsDir() {
				continue
			}
			projectDir := filepath.Join(projectsDir, p.Name())
			if candidate := filepath.Join(projectDir, name); fileExists(candidate) {
				return candidate
			}
			// Subagent transcripts sit one level down, under the parent session's
			// own directory. Reading it is cheap: these directories hold a handful
			// of entries, and the scan stops at the first hit.
			parents, err := os.ReadDir(projectDir)
			if err != nil {
				continue
			}
			for _, parent := range parents {
				if !parent.IsDir() {
					continue
				}
				candidate := filepath.Join(projectDir, parent.Name(), "subagents", name)
				if fileExists(candidate) {
					return candidate
				}
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ParseFullSession reads an entire session JSONL (capped at 10 MB) and returns
// all messages in display order. If lastOnly is true, only the last assistant
// message is returned. Searches all candidate Claude config directories.
func ParseFullSession(sessionID string, lastOnly bool) ([]OutputMessage, error) {
	if !uuidRE.MatchString(sessionID) {
		return nil, nil
	}

	sessionPath := locateTranscript(sessionID)
	if sessionPath == "" {
		return nil, nil
	}

	raw, err := readSessionRaw(sessionPath, lastOnly)
	if err != nil || raw == "" {
		return nil, err
	}
	return parseOutputMessages(raw, lastOnly), nil
}

func readSessionRaw(path string, lastOnly bool) (string, error) {
	if lastOnly {
		return TailRead(path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() <= maxReadBytes {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	// Large file: read last maxReadBytes
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Seek(-maxReadBytes, io.SeekEnd); err != nil {
		return "", err
	}
	buf := make([]byte, maxReadBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && n == 0 {
		return "", err
	}
	return string(buf[:n]), nil
}

// rawEntry is the top-level structure of a JSONL session log line.
type rawEntry struct {
	Type       string          `json:"type"`
	Timestamp  string          `json:"timestamp"`
	Message    json.RawMessage `json:"message"`
	Result     json.RawMessage `json:"result"`
	Attachment json.RawMessage `json:"attachment"`
	CWD        string          `json:"cwd"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Input     json.RawMessage `json:"input"`
}

type taskCreateInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}

type taskUpdateInput struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type agentInput struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
}

type toolInput struct {
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
}

type queuedAttachment struct {
	Type   string          `json:"type"`
	Prompt json.RawMessage `json:"prompt"`
}

// outputAccumulator collects OutputMessages while tracking TaskCreate indices
// for back-resolution when a tool_result carries the real task ID.
type outputAccumulator struct {
	messages          []OutputMessage
	taskCreateIndices map[string]int    // tool_use_id → index in messages
	taskSubjects      map[string]string // tool_use_id → subject
}

func newOutputAccumulator() *outputAccumulator {
	return &outputAccumulator{
		taskCreateIndices: make(map[string]int),
		taskSubjects:      make(map[string]string),
	}
}

func (a *outputAccumulator) handleAttachment(e rawEntry, ts *string) {
	var att queuedAttachment
	if err := json.Unmarshal(e.Attachment, &att); err == nil && att.Type == "queued_command" {
		for _, text := range extractTextFromPrompt(att.Prompt) {
			a.messages = append(a.messages, OutputMessage{Role: "human", Content: text, Timestamp: ts, Queued: true})
		}
	}
}

func (a *outputAccumulator) handleResult(e rawEntry, ts *string) {
	var resultStr string
	if err := json.Unmarshal(e.Result, &resultStr); err != nil {
		resultStr = string(e.Result)
	}
	if len(resultStr) > 1000 {
		resultStr = resultStr[:1000]
	}
	a.messages = append(a.messages, OutputMessage{Role: "tool_result", Content: resultStr, Timestamp: ts})
}

func (a *outputAccumulator) handleUserMessage(msg rawMessage, ts *string) {
	for _, text := range extractTextFromContent(msg.Content) {
		if isSystemXMLContent(text) {
			continue
		}
		a.messages = append(a.messages, OutputMessage{Role: "human", Content: text, Timestamp: ts})
	}
	var blocks []rawBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" || len(b.Content) == 0 {
			continue
		}
		var content string
		if err := json.Unmarshal(b.Content, &content); err != nil {
			content = string(b.Content)
		}
		if len(content) > 1000 {
			content = content[:1000]
		}
		// Resolve TaskCreate → real ID
		if b.ToolUseID != "" {
			if idx, ok := a.taskCreateIndices[b.ToolUseID]; ok {
				if realID := strings.TrimSpace(content); realID != "" {
					a.messages[idx].TaskID = &realID
					if subj, ok2 := a.taskSubjects[b.ToolUseID]; ok2 {
						a.taskSubjects[realID] = subj
					}
				}
			}
		}
		a.messages = append(a.messages, OutputMessage{Role: "tool_result", Content: content, Timestamp: ts})
	}
}

func (a *outputAccumulator) handleToolUse(b rawBlock, ts *string) {
	if b.Name == "" {
		return
	}
	switch b.Name {
	case "TaskCreate":
		var inp taskCreateInput
		_ = json.Unmarshal(b.Input, &inp)
		subject := inp.Subject
		if subject == "" {
			subject = inp.Description
		}
		if subject == "" {
			subject = "Task"
		}
		a.taskCreateIndices[b.ID] = len(a.messages)
		a.taskSubjects[b.ID] = subject
		taskStatus := "pending"
		taskID := b.ID
		a.messages = append(a.messages, OutputMessage{
			Role:       "task",
			Content:    subject,
			Timestamp:  ts,
			TaskStatus: &taskStatus,
			TaskID:     &taskID,
		})

	case "TaskUpdate":
		var inp taskUpdateInput
		_ = json.Unmarshal(b.Input, &inp)
		subject := a.taskSubjects[inp.TaskID]
		if subject == "" {
			subject = "Task"
		}
		status := inp.Status
		if status == "" {
			status = "in_progress"
		}
		realID := inp.TaskID
		a.messages = append(a.messages, OutputMessage{
			Role:       "task",
			Content:    subject,
			Timestamp:  ts,
			TaskStatus: &status,
			TaskID:     &realID,
		})

	case "Agent":
		var inp agentInput
		_ = json.Unmarshal(b.Input, &inp)
		content := inp.Description
		if content == "" {
			content = "Sub-agent"
		}
		subType := inp.SubagentType
		if subType == "" {
			subType = "general"
		}
		a.messages = append(a.messages, OutputMessage{
			Role:         "subagent",
			Content:      content,
			Timestamp:    ts,
			SubagentType: &subType,
		})

	default:
		var inp toolInput
		_ = json.Unmarshal(b.Input, &inp)
		var fp *string
		if inp.FilePath != "" {
			fp = &inp.FilePath
		} else if inp.Path != "" {
			fp = &inp.Path
		}
		toolName := b.Name
		a.messages = append(a.messages, OutputMessage{
			Role:      "tool_call",
			Content:   b.Name,
			Timestamp: ts,
			ToolName:  &toolName,
			FilePath:  fp,
		})
	}
}

func (a *outputAccumulator) handleAssistantMessage(msg rawMessage, ts *string) {
	var blocks []rawBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if text := strings.TrimSpace(b.Text); text != "" {
				a.messages = append(a.messages, OutputMessage{Role: "assistant", Content: text, Timestamp: ts})
			}
		case "tool_use":
			a.handleToolUse(b, ts)
		}
	}
}

func parseOutputMessages(raw string, lastOnly bool) []OutputMessage {
	acc := newOutputAccumulator()

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e rawEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		ts := strPtr(e.Timestamp)

		if e.Type == "attachment" {
			acc.handleAttachment(e, ts)
			continue
		}
		if e.Type == "result" && len(e.Result) > 0 {
			acc.handleResult(e, ts)
			continue
		}
		if len(e.Message) == 0 {
			continue
		}
		var msg rawMessage
		if err := json.Unmarshal(e.Message, &msg); err != nil {
			continue
		}
		// User messages (new format: type=="user"; legacy format: type=="message" with role=="user")
		if (e.Type == "user" || (e.Type == "message" && msg.Role == "user")) && msg.Role == "user" {
			acc.handleUserMessage(msg, ts)
			continue
		}
		// Assistant messages (new format: type=="assistant"; legacy format: type=="message" with role=="assistant")
		if e.Type == "assistant" || (e.Type == "message" && msg.Role == "assistant") {
			acc.handleAssistantMessage(msg, ts)
		}
	}

	if lastOnly {
		for i := len(acc.messages) - 1; i >= 0; i-- {
			if acc.messages[i].Role == "assistant" {
				return acc.messages[i : i+1]
			}
		}
		return nil
	}
	return acc.messages
}

// extractTextFromContent extracts plain text from a content field (string or block array).
func extractTextFromContent(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// Try string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t := strings.TrimSpace(s); t != "" {
			return []string{t}
		}
		return nil
	}
	// Try block array
	var blocks []rawBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

// extractTextFromPrompt extracts text parts from a queued_command prompt.
func extractTextFromPrompt(raw json.RawMessage) []string {
	return extractTextFromContent(raw)
}

// isSystemXMLContent reports whether text is an internal Claude Code protocol
// message that should not be shown in the transcript. These appear as user-role
// JSONL entries and include slash-command envelopes and local-command caveats.
func isSystemXMLContent(text string) bool {
	return strings.HasPrefix(text, "<command-name>") ||
		strings.HasPrefix(text, "<local-command-caveat>") ||
		strings.HasPrefix(text, "<command-message>") ||
		strings.HasPrefix(text, "<function_calls>")
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
