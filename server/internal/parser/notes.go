package parser

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
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

var (
	shellDefaultRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:-([^}]*)\}`)
	shellSegmentRe = regexp.MustCompile(`&&|\|\||[;|\n]`)
	vaultURLRe     = regexp.MustCompile("/vault/([^\\s\"'`?#\\\\]+)")
	writeMethodRe  = regexp.MustCompile(`(?:-X|--request)\s*['"]?(?:PUT|POST|PATCH)\b`)
	shellAssignRe  = regexp.MustCompile(`(^|[\s;&|(])([A-Za-z_][A-Za-z0-9_]*)=(?:"([^"]*)"|'([^']*)'|([^\s;&|"']*))`)
	shellVarRe     = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)
)

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

// Only ${NAME:-default} is knowable without the agent's environment; any other variable drops the URL.
func curlNoteTouches(command string) []NoteTouch {
	if !strings.Contains(command, "/vault/") {
		return nil
	}
	expanded := shellDefaultRe.ReplaceAllString(command, "$1")
	expanded = strings.ReplaceAll(expanded, "\\\n", " ")
	expanded = expandShellAssignments(expanded)
	var out []NoteTouch
	for _, segment := range shellSegmentRe.Split(expanded, -1) {
		kind := sdk.NoteTouchKindRead
		if writeMethodRe.MatchString(segment) {
			kind = sdk.NoteTouchKindWrite
		}
		for _, match := range vaultURLRe.FindAllStringSubmatch(segment, -1) {
			notePath, err := url.PathUnescape(match[1])
			if err != nil || strings.Contains(notePath, "$") || !isNotePath(notePath) {
				continue
			}
			out = append(out, NoteTouch{Path: notePath, Kind: kind})
		}
	}
	return out
}

// Assignments are dropped so a URL counts where it is used, not where it is named.
func expandShellAssignments(command string) string {
	vars := map[string]string{}
	substitute := func(s string) string {
		return shellVarRe.ReplaceAllStringFunc(s, func(ref string) string {
			m := shellVarRe.FindStringSubmatch(ref)
			if v, ok := vars[m[1]+m[2]]; ok {
				return v
			}
			return ref
		})
	}
	var b strings.Builder
	last := 0
	for _, loc := range shellAssignRe.FindAllStringSubmatchIndex(command, -1) {
		b.WriteString(substitute(command[last:loc[3]]))
		name := command[loc[4]:loc[5]]
		switch {
		case loc[6] >= 0:
			vars[name] = substitute(command[loc[6]:loc[7]])
		case loc[8] >= 0:
			vars[name] = command[loc[8]:loc[9]]
		default:
			vars[name] = substitute(command[loc[10]:loc[11]])
		}
		last = loc[1]
	}
	b.WriteString(substitute(command[last:]))
	return b.String()
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
