# Code Signing

The macOS desktop shell (`bin/Kontor.app`) needs a stable code-signing
identity so TCC (Transparency, Consent, and Control) grants persist across
rebuilds. An ad-hoc signature (`codesign -s -`) generates a fresh cdhash
every time, and macOS keys its accessibility and full-disk-access grants to
that hash — so every rebuild resets them and the app hangs on the first
`fsnotify` or `lsof` call that needs a TCC entitlement.

## Creating a self-signed certificate

1. Open **Keychain Access** → menu **Keychain Access → Certificate
   Assistant → Create a Certificate…**
2. Name: pick something recognisable (e.g. `Kontor Dev`).
3. Certificate Type: **Code Signing**.
4. Leave "Let me override defaults" unchecked → **Create**.
5. Open the new certificate → **Trust** → **Code Signing: Always Trust**.
   A self-signed certificate is untrusted until then, and
   `security find-identity -v` (which the bundle task checks against) lists
   only valid identities.
6. The name you chose is your `KONTOR_SIGN_IDENTITY`.

## Using an Apple Development certificate

If enrolled in the Apple Developer Program, any certificate whose
`security find-identity -v -p codesigning` output includes
`Apple Development: …` works. Use the full common-name string as
`KONTOR_SIGN_IDENTITY`.

## Setting the identity

Export it in your shell profile or `.envrc`:

```sh
export KONTOR_SIGN_IDENTITY='Kontor Dev'
```

## Listing available identities

```sh
security find-identity -v -p codesigning
```

## Verifying a signed bundle

```sh
# Must show an identifier-based designated requirement, not a cdhash:
codesign -d -r- bin/Kontor.app

# Must exit 0:
codesign --verify --deep --strict bin/Kontor.app
```

## How `task desktop:bundle` uses it

The bundle task (`Taskfile.yml → desktop:bundle`) swaps a fresh build into an
existing `bin/Kontor.app` (create it once with `task desktop:dist`). Before it
builds anything it checks that the bundle exists and that
`KONTOR_SIGN_IDENTITY` is set and present in the keychain; if a check fails
it prints an error pointing here and leaves the bundle untouched.
