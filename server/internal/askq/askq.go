// Package askq detects an AskUserQuestion modal from the visible rows of a
// terminal buffer.
//
// This is a hand-maintained parity port of src/utils/askQuestionScreen.ts.
// The TS and Go implementations are kept in parity by hand (no shared module
// across the TS/Go language boundary). The fixtures under testdata/ are copies
// of src/utils/__tests__/fixtures/*.txt kept byte-identical by hand; when
// changing detection logic or a fixture here, mirror the change in
// src/utils/askQuestionScreen.ts (and its test fixtures), and vice versa.
package askq

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lx-wnk/kontor/sdk"
)

// DetectedOption and DetectedQuestion are the public wire types for a parsed
// AskUserQuestion modal, defined in sdk since they cross the server/client
// boundary. Aliased here so existing callers within this package keep working
// unqualified.
type (
	DetectedOption   = sdk.DetectedOption
	DetectedQuestion = sdk.DetectedQuestion
)

const (
	typeSomethingLabel = "type something"
	chatAboutLabel     = "chat about this"
	submitLabel        = "submit"
	cancelLabel        = "cancel"
)

var (
	trailingPunctRe = regexp.MustCompile(`[\s.]+$`)

	// The box-drawing block (U+2500-U+257F) covers rounded corners, straight edges, and dashes.
	borderOnlyRe  = regexp.MustCompile(`^[\s\x{2500}-\x{257F}=+-]*$`)
	leadingBoxRe  = regexp.MustCompile(`^\s*[│║┃┆┇┊┋]`)
	trailingBoxRe = regexp.MustCompile(`[│║┃┆┇┊┋]\s*$`)
	// A modal's right border can bleed into a content line when the row is not
	// exactly border-width (e.g. "Which animal?────────╯"). A trailing RUN of
	// box-drawing glyphs is always chrome — ASCII hyphens are outside the range,
	// so a label ending in "-" survives.
	trailingBoxRunRe = regexp.MustCompile(`[\s\x{2500}-\x{257F}]+$`)
	numberedRowRe    = regexp.MustCompile(`^❯?\s*(\d+)\.\s+(\S.*)$`)
	checkboxRe       = regexp.MustCompile(`(?i)^\[[ x✔✓]\]\s*`)
	trailingCRRe     = regexp.MustCompile(`\r$`)
	toggleHintRe     = regexp.MustCompile(`(?i)toggle|space to`)
)

// The meta-row copy drifts between Claude Code releases (e.g. v2.1.205 renders
// "Type something." with a trailing period, v2.1.197 rendered "type something").
// Match on a normalized prefix - lower-cased, trailing punctuation stripped - so
// a cosmetic copy tweak does not silently disable question detection.
func normalizeLabel(label string) string {
	return trailingPunctRe.ReplaceAllString(strings.ToLower(label), "")
}

func metaLabelMatches(label, meta string) bool {
	return strings.HasPrefix(normalizeLabel(label), meta)
}

func toContentLine(rawRow string) (string, bool) {
	row := trailingCRRe.ReplaceAllString(rawRow, "")
	if borderOnlyRe.MatchString(row) {
		return "", false
	}

	inner := row
	if m := leadingBoxRe.FindString(inner); m != "" {
		inner = inner[len(m):]
	}
	if m := trailingBoxRe.FindString(inner); m != "" {
		inner = inner[:len(inner)-len(m)]
	}

	trimmed := strings.TrimSpace(trailingBoxRunRe.ReplaceAllString(inner, ""))
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

type parsedRow struct {
	text        string
	hasNum      bool
	num         int
	label       string
	hasCheckbox bool
}

type numberedEntry struct {
	row parsedRow
	idx int
}

func parseNumberedRow(text string) parsedRow {
	m := numberedRowRe.FindStringSubmatch(text)
	if m == nil {
		return parsedRow{text: text}
	}

	num, _ := strconv.Atoi(m[1])
	remainder := m[2]

	var label string
	hasCheckbox := false
	if cb := checkboxRe.FindString(remainder); cb != "" {
		hasCheckbox = true
		label = strings.TrimSpace(remainder[len(cb):])
	} else {
		label = strings.TrimSpace(remainder)
	}

	return parsedRow{text: text, hasNum: true, num: num, label: label, hasCheckbox: hasCheckbox}
}

// parseRows reduces raw terminal rows to the content lines that carry meaning
// (borders and blank rows dropped), each already parsed for a leading "N." row
// number, label and checkbox.
func parseRows(rows []string) []parsedRow {
	contentLines := make([]parsedRow, 0, len(rows))
	for _, raw := range rows {
		line, ok := toContentLine(raw)
		if !ok {
			continue
		}
		contentLines = append(contentLines, parseNumberedRow(line))
	}
	return contentLines
}

func numberedEntries(contentLines []parsedRow) []numberedEntry {
	numbered := make([]numberedEntry, 0, len(contentLines))
	for idx, row := range contentLines {
		if row.hasNum {
			numbered = append(numbered, numberedEntry{row: row, idx: idx})
		}
	}
	return numbered
}

// questionFromPreamble picks the prompt line: the LAST preamble line ending in
// "?" (scanning backwards, so a recap of earlier questions above the real
// prompt does not win), falling back to the last preamble line.
func questionFromPreamble(preamble []string) string {
	for i := len(preamble) - 1; i >= 0; i-- {
		if strings.HasSuffix(preamble[i], "?") {
			return preamble[i]
		}
	}
	if len(preamble) > 0 {
		return preamble[len(preamble)-1]
	}
	return ""
}

// decideMultiSelect: PRIMARY signal is a checkbox on EVERY option row. Only
// when there is zero checkbox evidence do we fall back to the true footer
// (lines after the last numbered row) for a "toggle"/"space to" hint. Header,
// question, and description lines are never scanned for it - that would let a
// question like "Would you like to toggle X?" flip the mode.
func decideMultiSelect(optionEntries []numberedEntry, contentLines []parsedRow) bool {
	anyCheckbox := false
	for _, e := range optionEntries {
		if e.row.hasCheckbox {
			anyCheckbox = true
			break
		}
	}
	if anyCheckbox {
		for _, e := range optionEntries {
			if !e.row.hasCheckbox {
				return false
			}
		}
		return true
	}

	lastNumberedIdx := -1
	for idx, row := range contentLines {
		if row.hasNum {
			lastNumberedIdx = idx
		}
	}

	for _, l := range contentLines[lastNumberedIdx+1:] {
		if !l.hasNum && toggleHintRe.MatchString(l.text) {
			return true
		}
	}
	return false
}

// DetectQuestion detects an AskUserQuestion modal from the visible rows of a
// terminal buffer.
//
// Keys off structural signals present in the real TUI render rather than
// exact spacing/border glyphs: a contiguous numbered option block followed by
// BOTH UI-injected meta-rows. Requiring both meta-rows - adjacent and
// index-continuous - is what separates a real modal from an ordinary
// numbered list in terminal output.
func DetectQuestion(rows []string) *sdk.DetectedQuestion {
	contentLines := parseRows(rows)
	numbered := numberedEntries(contentLines)

	typeRowIdx, chatRowIdx := -1, -1
	for i, e := range numbered {
		if typeRowIdx == -1 && metaLabelMatches(e.row.label, typeSomethingLabel) {
			typeRowIdx = i
		}
		if chatRowIdx == -1 && metaLabelMatches(e.row.label, chatAboutLabel) {
			chatRowIdx = i
		}
	}
	if typeRowIdx == -1 || chatRowIdx == -1 {
		return nil
	}

	typeSomethingIndex := numbered[typeRowIdx].row.num
	chatAboutIndex := numbered[chatRowIdx].row.num

	optionEntries := make([]numberedEntry, 0, len(numbered))
	for i, e := range numbered {
		if i == typeRowIdx || i == chatRowIdx {
			continue
		}
		if e.row.num < typeSomethingIndex {
			optionEntries = append(optionEntries, e)
		}
	}

	// ENFORCED invariant: real options numbered contiguously 1..n, meta-rows at n+1 / n+2.
	// A numbered-looking description line desyncs this - reject rather than emit garbage.
	contiguous := true
	for i, e := range optionEntries {
		if e.row.num != i+1 {
			contiguous = false
			break
		}
	}
	gateOk := len(optionEntries) >= 1 &&
		contiguous &&
		typeSomethingIndex == len(optionEntries)+1 &&
		chatAboutIndex == typeSomethingIndex+1
	if !gateOk {
		return nil
	}

	options := make([]sdk.DetectedOption, 0, len(optionEntries))
	for _, e := range optionEntries {
		option := sdk.DetectedOption{Index: e.row.num, Label: e.row.label}
		if next := e.idx + 1; next < len(contentLines) && !contentLines[next].hasNum {
			option.Description = contentLines[next].text
		}
		options = append(options, option)
	}

	preambleEnd := optionEntries[0].idx
	preamble := make([]string, 0, preambleEnd)
	for _, l := range contentLines[:preambleEnd] {
		preamble = append(preamble, l.text)
	}

	header := ""
	if len(preamble) > 0 {
		header = preamble[0]
	}

	question := questionFromPreamble(preamble)
	if question == "" {
		question = header
	}

	return &sdk.DetectedQuestion{
		Header:             header,
		Question:           question,
		MultiSelect:        decideMultiSelect(optionEntries, contentLines),
		Options:            options,
		TypeSomethingIndex: typeSomethingIndex,
		ChatAboutIndex:     chatAboutIndex,
	}
}

// DetectConfirmScreen detects the AskUserQuestion review/submit screen - the
// screen a multi-question flow lands on once every question is answered:
//
//	Review your answers
//	  ... recap of the given answers ...
//	Ready to submit your answers?
//	❯ 1. Submit answers
//	  2. Cancel
//
// It has NO meta-rows, so DetectQuestion rejects it by design; without this
// second detector the dashboard goes blind exactly when the flow needs one last
// keypress, and a multi-question round can never be completed from the UI.
//
// Gate: the last two numbered rows are adjacent, numbered 1 and 2, labelled
// Submit/Cancel, with no meta-row anywhere on screen. The Submit/Cancel label pair is the
// signal - deliberately NOT the surrounding copy ("Review your answers",
// "Ready to submit your answers?"), which drifts between Claude Code releases
// and would silently disable detection again the next time it is reworded.
func DetectConfirmScreen(rows []string) *sdk.DetectedConfirm {
	contentLines := parseRows(rows)
	numbered := numberedEntries(contentLines)

	if len(numbered) < 2 {
		return nil
	}
	// A meta-row anywhere means this is a question modal, not the confirm screen.
	for _, e := range numbered {
		if metaLabelMatches(e.row.label, typeSomethingLabel) || metaLabelMatches(e.row.label, chatAboutLabel) {
			return nil
		}
	}

	// Match the LAST two numbered rows rather than requiring exactly two on the
	// whole screen: unrelated numbered output can still be in the viewport above
	// the modal, and a strict count would silently disable detection. Adjacency
	// (no content line between them) is what keeps the pair a real option block.
	submit, cancel := numbered[len(numbered)-2], numbered[len(numbered)-1]
	if cancel.idx != submit.idx+1 {
		return nil
	}
	if submit.row.num != 1 || cancel.row.num != 2 {
		return nil
	}
	if !strings.HasPrefix(normalizeLabel(submit.row.label), submitLabel) ||
		normalizeLabel(cancel.row.label) != cancelLabel {
		return nil
	}

	preamble := make([]string, 0, submit.idx)
	for _, l := range contentLines[:submit.idx] {
		preamble = append(preamble, l.text)
	}
	if len(preamble) == 0 {
		return nil
	}

	return &sdk.DetectedConfirm{
		Question: questionFromPreamble(preamble),
		Options: []sdk.DetectedOption{
			{Index: submit.row.num, Label: submit.row.label},
			{Index: cancel.row.num, Label: cancel.row.label},
		},
	}
}

// DetectScreen runs both detectors over one set of rows and returns whichever
// AskUserQuestion screen is open, or nil when neither is. Callers on the scan
// hot path should use this rather than calling the detectors separately, so a
// single capture serves both.
func DetectScreen(rows []string) *sdk.PendingScreen {
	if q := DetectQuestion(rows); q != nil {
		return &sdk.PendingScreen{Question: q}
	}
	if c := DetectConfirmScreen(rows); c != nil {
		return &sdk.PendingScreen{Confirm: c}
	}
	return nil
}
