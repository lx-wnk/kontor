package parser

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"mvdan.cc/sh/v3/syntax"
)

// NoteWindow is how far back an agent's note touches are kept.
const NoteWindow = 10 * time.Minute

// maxNoteTouches caps one session's touches.
const maxNoteTouches = 50

// NoteTouch is a vault note an agent's tool call read or wrote. Path is
// relative to the whole vault, exactly as the call named it.
type NoteTouch struct {
	Path string
	Kind sdk.NoteTouchKind
	At   time.Time
}

const obsidianMCPPrefix = "mcp__obsidian__obsidian_"

var mcpNoteKinds = map[string]sdk.NoteTouchKind{
	"read_note":   sdk.NoteTouchKindRead,
	"create_note": sdk.NoteTouchKindWrite,
	"edit_note":   sdk.NoteTouchKindWrite,
}

var vaultURLRe = regexp.MustCompile("/vault/([^\\s\"'`?#\\\\]+)")

func noteTouchesOf(m Message) []NoteTouch {
	if m.Role != "assistant" || len(m.Content) == 0 || m.Content[0] != '[' {
		return nil
	}
	if !bytes.Contains(m.Content, []byte("/vault/")) && !bytes.Contains(m.Content, []byte(obsidianMCPPrefix)) {
		return nil
	}
	var blocks []toolUseBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []NoteTouch
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		for _, t := range toolNoteTouches(b.Name, b.Input) {
			t.At = m.Timestamp
			out = append(out, t)
		}
	}
	return out
}

func toolNoteTouches(name string, input json.RawMessage) []NoteTouch {
	if op, isMCP := strings.CutPrefix(name, obsidianMCPPrefix); isMCP {
		kind, known := mcpNoteKinds[op]
		var in struct {
			Path string `json:"path"`
		}
		if !known || json.Unmarshal(input, &in) != nil || !isNotePath(in.Path) {
			return nil
		}
		return []NoteTouch{{Path: in.Path, Kind: kind}}
	}
	if name != "Bash" {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	return curlNoteTouches(in.Command)
}

// curlNoteTouches reads the vault notes the command's curl calls name. Words
// resolve against the command's own earlier assignments; only ${NAME:-default}
// is knowable without the agent's environment, so any other variable drops
// the URL. A command the shell parser rejects yields no touches.
func curlNoteTouches(command string) []NoteTouch {
	if !strings.Contains(command, "/vault/") {
		return nil
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return nil
	}
	vars := map[string]string{}
	var out []NoteTouch
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.DeclClause:
			assignAll(vars, n.Args)
		case *syntax.CallExpr:
			// Prefix assignments of a command only reach that command's environment.
			if len(n.Args) == 0 {
				assignAll(vars, n.Assigns)
			} else if path.Base(resolveWord(vars, n.Args[0])) == "curl" {
				out = append(out, curlCallTouches(vars, n.Args[1:])...)
			}
		}
		return true
	})
	return out
}

func assignAll(vars map[string]string, assigns []*syntax.Assign) {
	for _, a := range assigns {
		if a.Name != nil && !a.Naked && !a.Append && a.Index == nil && a.Array == nil {
			vars[a.Name.Value] = resolveWord(vars, a.Value)
		}
	}
}

func curlCallTouches(vars map[string]string, words []*syntax.Word) []NoteTouch {
	args := make([]string, len(words))
	for i, w := range words {
		args[i] = resolveWord(vars, w)
	}
	kind := sdk.NoteTouchKindRead
	for i, arg := range args {
		method, isMethod := strings.CutPrefix(arg, "-X")
		if arg == "--request" {
			isMethod = true
		}
		if isMethod && (method == "" || arg == "--request") && i+1 < len(args) {
			method = args[i+1]
		}
		if isMethod && (method == "PUT" || method == "POST" || method == "PATCH") {
			kind = sdk.NoteTouchKindWrite
		}
	}
	var out []NoteTouch
	for _, arg := range args {
		for _, match := range vaultURLRe.FindAllStringSubmatch(arg, -1) {
			notePath, err := url.PathUnescape(match[1])
			if err != nil || strings.Contains(notePath, "$") || !isNotePath(notePath) {
				continue
			}
			out = append(out, NoteTouch{Path: notePath, Kind: kind})
		}
	}
	return out
}

// resolveWord renders anything it cannot know as "$", which drops a note path containing it.
func resolveWord(vars map[string]string, w *syntax.Word) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	resolveParts(&b, vars, w.Parts)
	return b.String()
}

func resolveParts(b *strings.Builder, vars map[string]string, parts []syntax.WordPart) {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			resolveParts(b, vars, p.Parts)
		case *syntax.ParamExp:
			b.WriteString(resolveParam(vars, p))
		default:
			b.WriteString("$")
		}
	}
}

func resolveParam(vars map[string]string, p *syntax.ParamExp) string {
	if p.Param == nil || p.Excl || p.Length || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Names != 0 {
		return "$"
	}
	value, known := vars[p.Param.Value]
	if p.Exp == nil {
		if known {
			return value
		}
		return "$"
	}
	switch p.Exp.Op {
	case syntax.DefaultUnset, syntax.DefaultUnsetOrNull, syntax.AssignUnset, syntax.AssignUnsetOrNull:
		if known && value != "" {
			return value
		}
		return resolveWord(vars, p.Exp.Word)
	}
	return "$"
}

// maxNotePathLen keeps a truncation from ever pointing at the wrong note.
const maxNotePathLen = 512

func isNotePath(p string) bool { return len(p) <= maxNotePathLen && strings.HasSuffix(p, ".md") }

// mergeNoteTouches folds add into touches: one entry per (path, kind) at its
// newest time, nothing older than NoteWindow before now, newest first, capped.
func mergeNoteTouches(touches, add []NoteTouch, now time.Time) []NoteTouch {
	type key struct {
		path string
		kind sdk.NoteTouchKind
	}
	newest := make(map[key]time.Time, len(touches)+len(add))
	for _, list := range [][]NoteTouch{touches, add} {
		for _, t := range list {
			k := key{t.Path, t.Kind}
			if at, seen := newest[k]; !seen || t.At.After(at) {
				newest[k] = t.At
			}
		}
	}
	cutoff := now.Add(-NoteWindow)
	out := make([]NoteTouch, 0, len(newest))
	for k, at := range newest {
		if at.Before(cutoff) {
			continue
		}
		out = append(out, NoteTouch{Path: k.path, Kind: k.kind, At: at})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	if len(out) > maxNoteTouches {
		out = out[:maxNoteTouches]
	}
	return out
}
