# Release Pipeline — daimon + daimon-frontend

**Last verified**: 2026-04-19  
**Current version**: v0.4.0  
**Current frontend version**: v0.3.2

## Overview

The two repos follow a **tag-triggered release pipeline** with **strict ordering**: `daimon-frontend` MUST be released first because the `daimon` build explicitly downloads `frontend-dist.tar.gz` from the frontend's GitHub releases during the goreleaser `before` hook (see `.goreleaser.yml` line 5-6: `make frontend`). Both repos use **git tag pushes** to trigger automated workflows; no manual dispatch or additional steps needed. Goreleaser handles binary building, OS/arch matrix, and GitHub release creation for daimon. The frontend workflow uses Node/Vite to build `dist/`, packages it as a tarball, and uploads it to GitHub releases.

## Pre-release checklist

- [ ] **daimon-frontend is released first** with tag `v0.5.0` and `frontend-dist.tar.gz` is published at GitHub releases
- [ ] Verify frontend release is **publicly accessible** at `https://github.com/mmmarxdr/daimon-frontend/releases/download/v0.5.0/frontend-dist.tar.gz`
- [ ] Verify daimon's `.goreleaser.yml` `make frontend` hook will succeed (requires curl, tar, and valid FRONTEND_TAG)
- [ ] All commits intended for release are on `main` branch
- [ ] Local `git status` is clean

## Step-by-step — cutting v0.5.0

### Step 1: Release daimon-frontend

```bash
cd /home/marxdr/workspace/daimon-frontend
git tag v0.5.0
git push origin v0.5.0
```

**Behind the scenes**:
1. GitHub detects tag push matching `v*`
2. `.github/workflows/release.yml` runs:
   - `npm ci --legacy-peer-deps` (install dependencies)
   - `npm run build` (produces `dist/`)
   - `tar -czf frontend-dist.tar.gz -C dist .` (packages assets)
   - `softprops/action-gh-release` uploads tarball to releases
3. Artifact appears at: `https://github.com/mmmarxdr/daimon-frontend/releases/download/v0.5.0/frontend-dist.tar.gz`

**Wait** for the workflow to complete (check Actions tab). This typically takes 1-2 minutes.

### Step 2: Release daimon (after frontend is published)

```bash
cd /home/marxdr/workspace/daimon
git tag v0.5.0
git push origin v0.5.0
```

**Behind the scenes**:
1. GitHub detects tag push matching `v*`
2. `.github/workflows/release.yml` runs goreleaser:
   - **Before hook** (`make frontend`): downloads `frontend-dist.tar.gz` from `mmmarxdr/daimon-frontend` releases and extracts to `internal/web/static/assets/`
   - **Build step**: compiles Go binaries for [linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64]
   - **Archive step**: creates `daimon_0.5.0_{os}_{arch}.tar.gz` (or `.zip` for windows)
   - **Checksum step**: generates `checksums.txt` with SHA256 hashes
   - Uploads all artifacts to GitHub releases
3. Artifacts appear at: `https://github.com/mmmarxdr/daimon/releases/download/v0.5.0/daimon_0.5.0_*.{tar.gz,zip}` + `checksums.txt`

### Step 3: Verify install.sh works end-to-end

```bash
curl -fsSL https://raw.githubusercontent.com/mmmarxdr/daimon/main/install.sh | sh
daimon --version
```

**Expected output**: "v0.5.0" (the version will be compiled into the binary via goreleaser's ldflags; see `.goreleaser.yml` line 22: `-X main.version={{.Version}}`)

## Reference

### daimon-frontend

| Property | Value |
|----------|-------|
| **Repo** | `mmmarxdr/daimon-frontend` |
| **Release trigger** | Git tag push matching `v*` |
| **Workflow file** | `.github/workflows/release.yml` (lines 1-33) |
| **Tag convention** | `v*` (e.g., `v0.5.0`) |
| **Artifact** | `frontend-dist.tar.gz` at `releases/download/v{version}/frontend-dist.tar.gz` |
| **package.json version** | Hardcoded to `"version": "0.0.0"` (NOT used; git tag is source of truth) |
| **Build output** | Vite produces `dist/index.html` + `dist/assets/*` |
| **Packaging** | `tar -czf frontend-dist.tar.gz -C dist .` (line 26) |

### daimon

| Property | Value |
|----------|-------|
| **Repo** | `mmmarxdr/daimon` |
| **Release trigger** | Git tag push matching `v*` |
| **Workflow file** | `.github/workflows/release.yml` (lines 1-25) |
| **Tag convention** | `v*` (e.g., `v0.5.0`) |
| **goreleaser config** | `.goreleaser.yml` (lines 1-43) |
| **Before hook** | `make frontend` (downloads from daimon-frontend releases) |
| **Build matrix** | linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/{amd64,arm64} |
| **Archive format** | `tar.gz` (except windows: `zip`) |
| **Artifact naming** | `daimon_{version}_{os}_{arch}.{tar.gz\|zip}` |
| **Checksum** | `checksums.txt` with SHA256 |
| **Version injection** | Goreleaser passes `{{.Version}}` (from git tag, stripped of `v` prefix) to ldflags `-X main.version={{.Version}}` |

### install.sh flow

1. **OS/Arch detection** (lines 10-24):
   - `uname -s` → linux/darwin; rejects windows
   - `uname -m` → amd64/arm64

2. **Fetch latest version** (lines 26-31):
   - Calls GitHub API: `https://api.github.com/repos/mmmarxdr/daimon/releases/latest`
   - Extracts `tag_name` field (e.g., `v0.5.0`)
   - Strips `v` prefix: `0.5.0`

3. **Download and install** (lines 33-51):
   - Constructs URL: `https://github.com/mmmarxdr/daimon/releases/download/v0.5.0/daimon_0.5.0_linux_amd64.tar.gz`
   - Downloads to temp dir
   - Extracts binary `daimon`
   - Installs to `/usr/local/bin` (with sudo if needed)
   - Sets executable bit

4. **Success output**: prints version and quick-start hints

## Gotchas & known issues

1. **Frontend release must exist BEFORE daimon tag push**:
   - If you push daimon's tag before frontend's release is published, goreleaser's `make frontend` hook will fail with a 404 from GitHub.
   - **Mitigation**: Always wait for frontend's workflow to complete (visible in Actions tab) before pushing daimon's tag.

2. **Frontend asset extraction**: `make frontend` uses `tar -xz -C $(STATIC_DIR)` expecting the tarball to contain `index.html` and `assets/` at the root (not nested in a directory). This matches the daimon-frontend workflow's `tar -czf frontend-dist.tar.gz -C dist .` (the `-C dist .` changes into `dist/` before archiving, so the root of the archive has the files directly).

3. **Checksum verification not enforced by install.sh**: The installer does NOT verify SHA256 checksums from `checksums.txt`. Future enhancement: update install.sh to validate before installation.

4. **Windows support**: `install.sh` explicitly rejects Windows (line 15). Windows users must download binaries manually or use WSL.

5. **Version string in daimon**: The binary's version comes from goreleaser's template `{{.Version}}`, which is derived from the git tag. Ensure the tag is semver-compliant (e.g., `v0.5.0`, not `release-0.5.0`).

## Version history

- **v0.4.0** — 2024-12-XX (current; see commit `chore(release): rebrand build pipeline and docs to Daimon`)
  - First stable release after rebrand from previous project
- **v0.3.2** — earlier (daimon-frontend latest, daimon is at v0.4.0)
- **v0.3.1**, **v0.3.0**, **v0.2.0**, **v0.1.0** — pre-rebrand history

