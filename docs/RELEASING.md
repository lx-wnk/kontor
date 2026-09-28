# Releasing Kontor

This document is for repository maintainers. End users: see [docs/guides/install.md](guides/install.md).

## Version scheme

[Semantic Versioning](https://semver.org): `vMAJOR.MINOR.PATCH`. The first public release is `v0.1.0`.
Use a `v` prefix on all tags (`v0.1.0`, not `0.1.0`).

## One-time setup (per repo / secrets)

These steps are done once and do not repeat per release.

### 1. Create the Homebrew tap repo

Create a public GitHub repository named `homebrew-tap` under the `lx-wnk` org:
`https://github.com/lx-wnk/homebrew-tap`

GoReleaser pushes the generated Homebrew **cask** (`Casks/kontor.rb`) to this repo. It must
be public for `brew install lx-wnk/tap/kontor` to work. Homebrew Cask is macOS-only — Linux
users install via the binary one-liner or Docker (see install guide).

### 2. Create a `HOMEBREW_TAP_TOKEN` secret

The cask push needs a Personal Access Token (PAT) with write access to `lx-wnk/homebrew-tap`.

1. Create a PAT at **GitHub → Settings → Developer settings → Personal access tokens (classic)**.
   Scopes: `repo` (full repo access, allows push to `homebrew-tap`).
2. Add it as a repository secret in `Kontor`:
   **Settings → Secrets and variables → Actions → New repository secret**
   Name: `HOMEBREW_TAP_TOKEN`

### 3. Verify GHCR is enabled

`ghcr.io/lx-wnk/kontor` images are pushed using the `GITHUB_TOKEN` (no extra secret needed)
because `.github/workflows/release.yml` has `permissions.packages: write` and a `docker/login-action`
step that logs in to GHCR.

After the first image is pushed, the package visibility may default to private. Set it to public at:
`https://github.com/lx-wnk?tab=packages` → `kontor` → **Package settings → Change visibility → Public**.

## Release steps (per release)

1. **Update CHANGELOG.md** — rename `## [Unreleased]` to `## [X.Y.Z] — YYYY-MM-DD`; add a new blank
   `## [Unreleased]` above it. GoReleaser derives the GitHub release notes from git commit history
   (the `changelog` block in `.goreleaser.yml`); the `CHANGELOG.md` file is the human-readable record.

2. **Merge to main and push** — the release commit must be on `main`.

3. **Tag and push:**
   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

4. **Watch the Release workflow** at `https://github.com/lx-wnk/kontor/actions`.
   It runs GoReleaser, which:
   - Builds four binaries (darwin/linux × amd64/arm64)
   - Creates four `.tar.gz` archives + `checksums.txt` → GitHub release assets
   - Pushes the Homebrew cask to `lx-wnk/homebrew-tap`
   - Builds and pushes the multi-arch Docker image `ghcr.io/lx-wnk/kontor:{version}` and
     `:latest` (a single manifest covering `linux/amd64` + `linux/arm64`)

5. **Verify:**
   - GitHub release page shows four archives, `checksums.txt`, and the `install.sh` one-liner works
   - `brew install lx-wnk/tap/kontor` succeeds on macOS (allow a few minutes for the tap to propagate)
   - `docker pull ghcr.io/lx-wnk/kontor:latest` succeeds

## Dry-run before tagging (recommended)

Run the snapshot locally before pushing a tag to catch config problems:

```sh
# Install goreleaser if not present
go install github.com/goreleaser/goreleaser/v2@latest
# or: brew install goreleaser/tap/goreleaser

# Validate config (fast, no build)
goreleaser check

# Dry-run WITHOUT Docker (no daemon needed) — builds binaries, archives, checksums, cask
goreleaser release --snapshot --clean --skip=docker

# Full dry-run WITH Docker images (requires a running Docker daemon), no publish
goreleaser release --snapshot --clean
```

The snapshot artifacts land in `dist/`: four archives, `checksums.txt`, and the generated cask under
`dist/homebrew/Casks/`. In snapshot mode the per-arch Docker images are built but not pushed and the
multi-arch manifest is only assembled on a real (push) release. Check that all four archives and
`checksums.txt` are present before pushing the tag.

> The snapshot rebuilds the Vue SPA into `server/frontend/dist`, which overwrites the committed
> `.gitkeep`. Restore it before committing anything: `git checkout -- server/frontend/dist/.gitkeep`.

## Rollback

- Remove the tag: `git push origin :v0.1.0` and `git tag -d v0.1.0`
- Delete the GitHub release (draft or published)
- The Homebrew cask must be reverted manually in `lx-wnk/homebrew-tap`
- Docker images can be deleted from GHCR package settings

## Security reminder

`kontor serve` binds to `127.0.0.1` only. The Docker run command in all docs publishes
with `-p 127.0.0.1:13120:13120` — never omit the loopback host on the published port. Inside the
container the server is told to bind `0.0.0.0` (`-e DASHBOARD_HOST=0.0.0.0 -e DASHBOARD_REMOTES_ENABLED=true`)
so the published loopback port can reach it; this stays private precisely because the host-side
publish is pinned to `127.0.0.1:`. Never pair those env flags with a `-p 0.0.0.0:…` publish.
