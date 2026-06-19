# Building Bharat Browser for Free

This document explains how to build Bharat Browser without any paid services
(WarpBuild, code signing certificates, Cloudflare R2).

## What Changed

### Replaced Paid Infrastructure

| Service | Replacement |
|---|---|
| WarpBuild runners (32-core Linux, 32-core Windows, M4 Mac) | GitHub-hosted runners (`ubuntu-latest`, `windows-latest`, `macos-latest`) |
| WarpBuilds/cache (unlimited size) | Removed — `actions/cache` 10 GB limit is too small for Chromium. Each build does a full `gclient sync` (~20-30 min). |
| Cloudflare R2 (server binary bundles, artifact upload) | Made optional — modules skip gracefully if `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/`R2_SECRET_ACCESS_KEY` secrets are not set. |
| SSL.com code signing (Windows) | Removed — `sign_windows` replaced by unsigned `mini_installer` module. |
| Apple Developer code signing + notarization (macOS) | Made optional — `sign_macos` skips if no signing env vars set. |
| Sparkle Ed25519 update signing | Made optional — `sparkle_sign` skips if `SPARKLE_PRIVATE_KEY` not set. |
| Self-hosted macOS builder | Replaced with `macos-latest` runner. |

### Workflow Changes

- **nightly-release.yml** — Replaced WarpBuild runners with GitHub-hosted; removed
  `queue-watchdog` job; removed all cache steps; R2 secrets are passed
  unconditionally (GitHub sets them to empty string when missing, which the build
  modules handle as "not configured").

- **nightly-macos-build.yml** — Replaced self-hosted runner with `macos-latest`;
  removed `BROWSEROS_REPO_PATH`/`BROWSEROS_CHROMIUM_SRC` vars; uses standard
  checkout; R2/release_server default to `false`.

### Module Changes

- `storage/download.py` — `validate()` no longer requires R2. With no R2 config,
  the module logs a warning and skips. Without `boto3`, it also skips.
- `storage/upload.py` — `validate()` accepts any config. `execute()` already
  skipped gracefully when R2 is not configured.
- `sign/sparkle.py` — `validate()` accepts any config. `execute()` skips if
  `SPARKLE_PRIVATE_KEY` is not set.
- `sign/macos.py` — `validate()` no longer requires signing env vars. `execute()`
  skips if signing env not configured.

## Free Build Limitations

1. **Disk space** — GitHub-hosted runners may not have enough free space
   for a full Chromium checkout + build. If builds fail with "no space left",
   consider:
   - Using a [self-hosted runner](https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/about-self-hosted-runners)
   - Reducing build components via GN args
   - Adding [actions/cache](https://github.com/actions/cache) for partial caching

2. **No code signing** — Windows builds will show SmartScreen warnings.
   macOS builds will show Gatekeeper warnings. Users must Ctrl-click or
   right-click "Open" to run. To enable signing, set the required env vars.

3. **No auto-update signing** — Sparkle/WinSparkle updates will not be
   Ed25519-signed. Users can still download and install manually.

4. **No server binary bundles** — Without R2 credentials, the
   `download_resources` module skips. BrowserOS Server binaries are not
   downloaded, which may affect some features.

5. **No Cloudflare R2 upload** — Artifacts are published via GitHub
   Actions `upload-artifact` instead of being pushed to R2/CDN.

## Setting Up a Fork

1. Fork the repository on GitHub
2. Go to Settings → Actions → General → **Allow all actions and reusable workflows**
3. Optional: Add the following repository secrets if you want R2 upload:
   - `R2_ACCOUNT_ID`
   - `R2_ACCESS_KEY_ID`
   - `R2_SECRET_ACCESS_KEY`
   - `R2_BUCKET`
4. Run the **Nightly Release Build** workflow manually, or push to `main`
   to trigger scheduled builds
