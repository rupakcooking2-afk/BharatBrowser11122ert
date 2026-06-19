# browseros (Go build tool)

Standalone Go port of the Python build system in `packages/browseros/build/`.
Compiles to a single static binary per platform — no Python, uv, or
depot-tools-python needed to run it. External build tools (git, gn,
autoninja, gclient, codesign, notarytool, CodeSignTool, pkg-dmg,
appimagetool, dpkg-deb, gh, python3 for the universalizer) stay external;
this binary orchestrates them.

## Status

Full CLI surface ported with flag/semantics parity:

| Command | Notes |
| --- | --- |
| `browseros build` | CONFIG (`--config`), MODULES (`--modules`), and phase-flag modes; all 21 pipeline modules |
| `browseros dev` | `extract commit/patch/range`, `apply all/feature/patch/force/changed`, `feature …`, `annotate` |
| `browseros release` | `--list/--appcast/--publish/--download` + `github create` |
| `browseros ota` | `server release/release-appcast/list-platforms`, `test-signing` |
| `browseros upload` | `lima`, `bun`, `codex`, `claude-code` |

The Python tool remains side-by-side (`uv run browseros …`) until the Go
tool is validated in release CI; both read the **same** files —
`build/config/*.yaml` (incl. the `!env` tag), `CHROMIUM_VERSION`,
`resources/BROWSEROS_VERSION`, `build/config/BROWSEROS_BUILD_OFFSET`,
`BASE_COMMIT`, `build/features.yaml` — so they cannot drift on inputs.

Not ported (still Python, by design): `build/scripts/bump_version.py`,
`bump_server_version.py`, icon generation, and
`build/modules/package/universalizer_patched.py` (invoked as a subprocess
by `universal_build`).

## Build

```sh
make build          # ./browseros for the host platform
make install        # install to GOBIN (codesigns on macOS)
make test           # go test ./...
make dist           # static binaries: darwin/{arm64,amd64}, linux/{amd64,arm64}, windows/amd64
```

## Run

The binary must run from inside a BrowserOS repo checkout (configs,
patches, and resources live there), or with `BROWSEROS_ROOT` pointing at
`packages/browseros`. Examples:

```sh
browseros build --config build/config/release.macos.arm64.yaml --chromium-src ~/chromium/src
browseros build --chromium-src ~/chromium/src --build --sign --package --arch arm64 --build-type release
browseros dev --chromium-src ~/chromium/src extract commit HEAD --base $(cat BASE_COMMIT)
browseros dev --chromium-src ~/chromium/src apply force
browseros release --list
```

R2 access uses `R2_ACCOUNT_ID` / `R2_ACCESS_KEY_ID` / `R2_SECRET_ACCESS_KEY`
(a `.env` at `packages/browseros/.env` or the repo root is loaded
automatically, matching the Python tool).
