# Desktop app (macOS)

A native macOS shell (`desktop/`, [wails](https://wails.io) v2) wraps the same dashboard as one binary — no separate server process, no sidecar. It starts the dashboard HTTP server in-process on `127.0.0.1:13120` and opens a native WKWebView window pointed at it, so it's the identical Vue SPA you get in a browser tab, just packaged as an app. Other platforms keep running `kontor serve` in a browser; the desktop shell is macOS-only.

Build and run it for a smoke test (requires macOS + Xcode command-line tools; no `wails` CLI needed for this):

```bash
task desktop:run
```

That builds the SPA, embeds it, and links the shell with the wails production tag and the `UniformTypeIdentifiers` framework (a plain `go build` omits both — see [CONTRIBUTING.md](../../CONTRIBUTING.md#desktop-shell-macos)), then launches the window.

An unsigned `.app`/`.dmg` build (`task desktop:dist` / `task desktop:dmg`) plus the full signing and
notarization steps are documented in [desktop-distribution.md](../desktop-distribution.md).
`task desktop:bundle` re-signs a rebuilt `.app` with a stable local identity so macOS privacy grants
survive rebuilds ([code-signing.md](../code-signing.md)). Opened from Finder, the app inherits no
shell `CLAUDE_CONFIG_DIR`; set `claude.configDir` in Settings to point it at a non-default Claude
config directory (applies after a restart).

## Window size and scaling

The layout is designed for a 1400×900 point window. A smaller window zooms the whole page out to fit
(WKWebView `pageZoom`, the same as Cmd-minus in a browser, so layout, `vh` units and clicks stay
consistent), down to 65%. The content area cannot shrink below the size that 65% still fits
(910×585 points), so nothing is cut off; a larger window keeps 100%. The constants live in
`desktop/window_darwin.go`.

macOS remembers the window's size and position between launches (`setFrameAutosaveName`, stored as
`NSWindow Frame KontorMainWindow` in the `com.lxwnk.kontor` defaults) and keeps a restored window on
the attached screens; the very first launch shrinks the default size into the screen's visible area. `defaults delete com.lxwnk.kontor "NSWindow Frame KontorMainWindow"` resets it.

## Manual smoke checklist

Real Mac:

1. The webview loads the dashboard (not a blank page) — it fetches the SPA and `/api/*` from `http://127.0.0.1:13120`, so the redirect landed on the loopback origin.
2. A mutating action (spawn an agent, create a task, answer a question) succeeds with no `403`.
3. No App Transport Security block appears in Console.app.
4. Shrinking the window scales the page down without cutting anything off; quitting and reopening restores the last size and position.
5. After entering and leaving fullscreen, the window still cannot shrink below the minimum size.
