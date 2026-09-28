package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The workspace layout rules, mirrored in src/features/workspace/layout.ts.
// Change both together: the settings API accepts a PATCH from any loopback
// caller, so a rule only the browser applies is not a rule.
const (
	workspaceColumns  = 12
	workspaceMaxPages = 50
	workspaceMaxTiles = 100
	workspaceMaxRow   = 500
	workspaceMaxTitle = 80
	workspaceZentrale = "zentrale"
	// The legitimate maximum is ~600 KB (50 pages x 100 tiles); this caps the
	// raw value before it is decoded.
	workspaceMaxRawBytes = 1 << 20
)

var (
	workspacePageID   = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	workspaceWidgetID = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
)

type workspaceTile struct {
	Widget  string `json:"widget"`
	Col     int    `json:"col"`
	Row     int    `json:"row"`
	ColSpan int    `json:"colSpan"`
	RowSpan int    `json:"rowSpan"`
}

type workspacePage struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Tiles []workspaceTile `json:"tiles"`
}

type workspaceLayout struct {
	Version int             `json:"version"`
	Pages   []workspacePage `json:"pages"`
}

func validWorkspaceLayout(raw string) error {
	if len(raw) > workspaceMaxRawBytes {
		return fmt.Errorf("workspace.layout: value exceeds %d bytes", workspaceMaxRawBytes)
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var l workspaceLayout
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return fmt.Errorf("workspace.layout: not a layout: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("workspace.layout: trailing data after the layout")
	}
	if l.Version != 1 {
		return fmt.Errorf("workspace.layout: version must be 1")
	}
	if len(l.Pages) == 0 || len(l.Pages) > workspaceMaxPages {
		return fmt.Errorf("workspace.layout: between 1 and %d pages", workspaceMaxPages)
	}
	seen := make(map[string]bool, len(l.Pages))
	for _, p := range l.Pages {
		if err := validWorkspacePage(p, seen); err != nil {
			return fmt.Errorf("workspace.layout: %w", err)
		}
	}
	if !seen[workspaceZentrale] {
		return fmt.Errorf("workspace.layout: the %s page is missing", workspaceZentrale)
	}
	return nil
}

func validWorkspacePage(p workspacePage, seen map[string]bool) error {
	if !workspacePageID.MatchString(p.ID) {
		return fmt.Errorf("page id %q: lowercase letters, digits and dashes only", p.ID)
	}
	if seen[p.ID] {
		return fmt.Errorf("page %s appears twice", p.ID)
	}
	seen[p.ID] = true
	if n := utf8.RuneCountInString(strings.TrimSpace(p.Title)); n == 0 || n > workspaceMaxTitle {
		return fmt.Errorf("page %s: title needs 1 to %d characters", p.ID, workspaceMaxTitle)
	}
	if p.Tiles == nil || len(p.Tiles) > workspaceMaxTiles {
		return fmt.Errorf("page %s: holds at most %d tiles", p.ID, workspaceMaxTiles)
	}
	for i, t := range p.Tiles {
		if err := validWorkspaceTile(t); err != nil {
			return fmt.Errorf("page %s tile %d: %w", p.ID, i, err)
		}
		for j := range i {
			if p.Tiles[j].Widget == t.Widget {
				return fmt.Errorf("page %s places %s twice", p.ID, t.Widget)
			}
			if workspaceTilesOverlap(t, p.Tiles[j]) {
				return fmt.Errorf("page %s: tile %d overlaps tile %d", p.ID, i, j)
			}
		}
	}
	return nil
}

func validWorkspaceTile(t workspaceTile) error {
	switch {
	case !workspaceWidgetID.MatchString(t.Widget):
		return fmt.Errorf("widget id %q is not valid", t.Widget)
	case t.ColSpan < 1 || t.RowSpan < 1:
		return fmt.Errorf("spans must be at least 1")
	case t.Col < 1 || t.Col > workspaceColumns || t.ColSpan > workspaceColumns-t.Col+1:
		return fmt.Errorf("must stay within %d columns", workspaceColumns)
	case t.Row < 1 || t.Row > workspaceMaxRow || t.RowSpan > workspaceMaxRow-t.Row+1:
		return fmt.Errorf("rows must lie between 1 and %d", workspaceMaxRow)
	}
	return nil
}

func workspaceTilesOverlap(a, b workspaceTile) bool {
	return a.Col < b.Col+b.ColSpan && b.Col < a.Col+a.ColSpan &&
		a.Row < b.Row+b.RowSpan && b.Row < a.Row+a.RowSpan
}
