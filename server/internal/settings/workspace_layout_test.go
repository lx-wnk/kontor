package settings

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const zentraleOnly = `{"version":1,"pages":[{"id":"zentrale","title":"Zentrale","tiles":[` +
	`{"widget":"agents","col":1,"row":1,"colSpan":3,"rowSpan":3},` +
	`{"widget":"obsidian__recent","col":4,"row":1,"colSpan":6,"rowSpan":2}]}]}`

func TestWorkspaceLayout_RegisteredAsLiveString(t *testing.T) {
	d, ok := Lookup("workspace.layout")
	require.True(t, ok, "workspace.layout is not in the registry")
	require.Equal(t, TypeString, d.Type)
	require.Equal(t, ApplyLive, d.Apply)
	require.Equal(t, "", d.Default)
}

func TestWorkspaceLayout_Validation(t *testing.T) {
	d, _ := Lookup("workspace.layout")

	// Empty means "the built-in layout".
	require.NoError(t, d.Validate(""))
	// Whitespace-only counts as empty too, mirroring parseLayout in layout.ts.
	require.NoError(t, d.Validate("   "))
	// An unknown widget id is kept: a deactivated module must not cost the layout.
	require.NoError(t, d.Validate(zentraleOnly))

	// The settings API accepts a PATCH from anything on loopback, so the rules
	// the browser applies must hold here too.
	bad := map[string]string{
		"not json":         `{`,
		"wrong version":    strings.Replace(zentraleOnly, `"version":1`, `"version":2`, 1),
		"unknown field":    strings.Replace(zentraleOnly, `"version":1`, `"version":1,"extra":true`, 1),
		"overlap":          strings.Replace(zentraleOnly, `"col":4,"row":1`, `"col":3,"row":2`, 1),
		"past column 12":   strings.Replace(zentraleOnly, `"col":4,"row":1,"colSpan":6`, `"col":8,"row":1,"colSpan":6`, 1),
		"row below one":    strings.Replace(zentraleOnly, `"col":4,"row":1`, `"col":4,"row":0`, 1),
		"span below one":   strings.Replace(zentraleOnly, `"colSpan":6,"rowSpan":2`, `"colSpan":6,"rowSpan":0`, 1),
		"bad widget id":    strings.Replace(zentraleOnly, `obsidian__recent`, `Bad Widget`, 1),
		"no zentrale":      strings.Replace(zentraleOnly, `"id":"zentrale"`, `"id":"morning"`, 1),
		"duplicate page":   strings.Replace(zentraleOnly, `]}]}`, `]},{"id":"zentrale","title":"Again","tiles":[]}]}`, 1),
		"empty title":      strings.Replace(zentraleOnly, `"title":"Zentrale"`, `"title":"  "`, 1),
		"uppercase pageid": strings.Replace(zentraleOnly, `]}]}`, `]},{"id":"Morning","title":"M","tiles":[]}]}`, 1),
		"title too long":   strings.Replace(zentraleOnly, `"title":"Zentrale"`, `"title":"`+strings.Repeat("a", 81)+`"`, 1),
		"0 pages":          `{"version":1,"pages":[]}`,
		"51 pages":         rawWithPages(51),
		"101 tiles":        rawWithTiles(101),
		"row ends at 501":  strings.Replace(zentraleOnly, `"col":4,"row":1,"colSpan":6,"rowSpan":2`, `"col":4,"row":500,"colSpan":6,"rowSpan":2`, 1),
		"colSpan overflow": strings.Replace(zentraleOnly, `"colSpan":6`, `"colSpan":9223372036854775807`, 1),
		"trailing garbage": zentraleOnly + " garbage",
		// Leading whitespace keeps this valid JSON: only the size cap, not a
		// parse error, must reject it.
		"over 1 MiB":       strings.Repeat(" ", workspaceMaxRawBytes) + zentraleOnly,
		"duplicate widget": strings.Replace(zentraleOnly, `"widget":"obsidian__recent"`, `"widget":"agents"`, 1),
		"tiles null":       `{"version":1,"pages":[{"id":"zentrale","title":"Zentrale","tiles":null}]}`,
	}
	for name, raw := range bad {
		require.Error(t, d.Validate(raw), name)
	}
}

func rawWithPages(n int) string {
	pages := make([]string, n)
	pages[0] = `{"id":"zentrale","title":"Zentrale","tiles":[]}`
	for i := 1; i < n; i++ {
		pages[i] = fmt.Sprintf(`{"id":"p%d","title":"P","tiles":[]}`, i)
	}
	return `{"version":1,"pages":[` + strings.Join(pages, ",") + `]}`
}

// Distinct, non-overlapping tiles — one cell each, laid out row by row across
// 12 columns — so only the tile-count cap rejects the layout, not the
// duplicate-widget or overlap rule a repeated tile would trip instead.
func rawWithTiles(n int) string {
	tiles := make([]string, n)
	for i := range tiles {
		col := i%12 + 1
		row := i/12 + 1
		tiles[i] = fmt.Sprintf(`{"widget":"w%d","col":%d,"row":%d,"colSpan":1,"rowSpan":1}`, i, col, row)
	}
	return `{"version":1,"pages":[{"id":"zentrale","title":"Zentrale","tiles":[` + strings.Join(tiles, ",") + `]}]}`
}

// Title length counts Unicode code points (utf8.RuneCountInString), matching
// src/features/workspace/layout.ts's [...title].length. Counting UTF-16 units
// on the client instead would let a PATCH here store a title the browser then
// refuses to read, locking editing.
func TestWorkspaceLayout_TitleCountsCodePoints(t *testing.T) {
	d, _ := Lookup("workspace.layout")

	emojiTitle := strings.Repeat("\U0001F600", 41)
	withEmojiTitle := strings.Replace(zentraleOnly, `"title":"Zentrale"`, `"title":"`+emojiTitle+`"`, 1)
	require.NoError(t, d.Validate(withEmojiTitle))
}
