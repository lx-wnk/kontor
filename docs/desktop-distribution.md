# Distributing the macOS desktop shell

How to package `desktop/` (the wails v2 shell, see [CONTRIBUTING.md](../CONTRIBUTING.md#desktop-shell-macos))
into a `.app` bundle and `.dmg` for distribution, and how to sign + notarize that artifact so
Gatekeeper accepts it without a warning.

This is a separate, later step from `task desktop:run` / `task build:desktop`, which produce a bare
Mach-O binary for local dev smoke-testing only — not something you'd hand to another Mac.

## Prerequisites

| Tool | Install | Needed for |
|------|---------|-------------|
| Xcode command-line tools | `xcode-select --install` | both dev build and packaging |
| wails CLI v2.13.0 | `go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0` | `task desktop:dist` |
| `hdiutil` | pre-installed on macOS | `task desktop:dmg` |

Run `wails doctor` after installing the CLI to confirm your toolchain is ready.

## Building the unsigned artifact

```sh
export PATH="$PATH:$(go env GOPATH)/bin"   # so `wails` is on PATH
task desktop:dist   # -> bin/Kontor.app
task desktop:dmg    # -> bin/Kontor.dmg (chains desktop:dist)
```

`desktop:dist` runs `wails build` (reading `desktop/wails.json` and `desktop/build/darwin/Info.plist`)
with the same `-tags production` and `-ldflags "-extldflags '-framework UniformTypeIdentifiers'"`
that `build:desktop` already uses for the raw-binary smoke build, so both paths link identically.
`desktop:dmg` packages the resulting bundle into a plain read-only DMG via `hdiutil create` — no
`create-dmg` or other third-party tool required.

Both artifacts are **unsigned**. That's expected at this stage — see below.

## The Gatekeeper caveat (unsigned artifact)

Anyone who downloads `Kontor.app`/`.dmg` before it's signed and notarized (see next
section) will hit Gatekeeper's "cannot be opened because the developer cannot be verified" dialog.
The workaround for a locally-built or trusted unsigned app:

1. Right-click (or Control-click) `Kontor.app` in Finder.
2. Choose **Open**.
3. Confirm **Open** in the dialog that appears.

This bypasses Gatekeeper for that one launch and whitelists the app for future launches. It is not
a substitute for real signing when distributing to people who aren't already trusting you.

## Signing + notarization (operator step, deferred)

This step requires a **paid Apple Developer Program membership** ($99/year) for a Developer ID
Application certificate — it is not something that can be scripted or automated without that
account, so it isn't wired into `task desktop:dist`/`desktop:dmg`. Once you have the cert
installed in your keychain:

### 1. Sign the app with the hardened runtime

```sh
codesign --deep --force --options runtime \
  --entitlements desktop/build/darwin/entitlements.plist \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  "bin/Kontor.app"
```

`desktop/build/darwin/entitlements.plist` grants the minimum a wails/WKWebView app needs under the
hardened runtime (JIT, unsigned executable memory, loopback network client+server for the
in-process dashboard server). `--deep` re-signs any embedded frameworks; `--options runtime` is
what makes the binary eligible for notarization.

If a future notarized build fails Gatekeeper's library validation for a specific dependency (a
codesign or notarization error mentioning library validation), add
`com.apple.security.cs.disable-library-validation` back to the entitlements file then — it's
deliberately left out by default since it widens what unsigned code the process can load.

Verify the signature:

```sh
codesign --verify --deep --strict --verbose=2 "bin/Kontor.app"
spctl --assess --type execute --verbose "bin/Kontor.app"
```

### 2. Re-package the signed app into a DMG

```sh
rm -f "bin/Kontor.dmg"
hdiutil create -volname "Kontor" -srcfolder "bin/Kontor.app" \
  -ov -format UDZO "bin/Kontor.dmg"
codesign --sign "Developer ID Application: Your Name (TEAMID)" "bin/Kontor.dmg"
```

### 3. Submit for notarization

Requires an app-specific password or API key set up for `notarytool` (see
[Apple's notarytool guide](https://developer.apple.com/documentation/security/notarizing_macos_software_before_distribution)).

```sh
xcrun notarytool submit "bin/Kontor.dmg" \
  --apple-id "you@example.com" \
  --team-id "TEAMID" \
  --password "@keychain:AC_PASSWORD" \
  --wait
```

`--wait` blocks until Apple's notary service returns `Accepted` or `Invalid`. On `Invalid`, pull
the failure log with `xcrun notarytool log <submission-id> --apple-id ... --team-id ... --password ...`.

### 4. Staple the notarization ticket

```sh
xcrun stapler staple "bin/Kontor.dmg"
xcrun stapler validate "bin/Kontor.dmg"
```

Stapling embeds the notarization ticket in the DMG itself, so Gatekeeper can verify it offline
(no network round-trip to Apple at install time on the end user's Mac). After this, the DMG opens
with no Gatekeeper warning on any Mac.

## Files involved

- `desktop/wails.json` — wails v2 project config (bundle name, output filename, build tags)
- `desktop/build/darwin/Info.plist` — `Info.plist` template rendered into the bundle by `wails build`
- `desktop/build/darwin/entitlements.plist` — hardened-runtime entitlements for the signing step above
- `Taskfile.yml` — `desktop:dist` / `desktop:dmg` tasks
