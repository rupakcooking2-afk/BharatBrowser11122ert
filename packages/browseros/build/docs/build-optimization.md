# Build Optimization Reference

Comprehensive reference for every optimization applied to the Bharat Browser
Chromium build system.  This document covers the CI pipeline, caching
architecture, parallelism tuning, and troubleshooting.

---

## Architecture Overview

```
┌─ plan ─────────────────────────────────────────────────────┐
│  Resolve build matrix (linux-x64 / windows-x64 /            │
│  macos-arm64) and publish flag.                              │
└─────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─ build (per platform) ─────────────────────────────────────┐
│                                                              │
│  1. checkout + setup-uv         ~10s    always runs          │
│  2. cache restores (4 caches)    ~2-5m  parallel             │
│  3. setup_chromium checkout      ~0-5m  skipped if cached    │
│  4. gclient sync                 ~0-15m skipped if cached    │
│  5. install sccache/ccache       ~30s                        │
│  6. gn gen                       ~1-2m                       │
│  7. autoninja (RAM-capped -j)    ~2-15h ★ BOTTLENECK        │
│  8. package                      ~2-5m                       │
│  9. upload artifacts             ~5m                         │
│                                                              │
└─────────────────────────────────────────────────────────────┘
                           │
                           ▼
┌─ publish ───────────────────────────────────────────────────┐
│  Create/update nightly prerelease on GitHub.                  │
└─────────────────────────────────────────────────────────────┘
```

## Caching Architecture

### Cache 1: depot_tools

| Field | Value |
|---|---|
| Path | `$CHROMIUM_ROOT/depot_tools` |
| Key | `depot-tools-${{ runner.os }}-${{ runner.arch }}` |
| Invalidation | Never (keyed by runner type only) |
| Size | ~200 MB |
| Purpose | Avoid re-cloning gclient, gn, ninja |

### Cache 2: gclient output

| Field | Value |
|---|---|
| Path | `$CHROMIUM_ROOT/src/.git`, `$CHROMIUM_ROOT/src/third_party`, `$CHROMIUM_ROOT/src/buildtools` |
| Key | `gclient-${{ runner.os }}-${{ runner.arch }}-${{ version }}` |
| Invalidation | Chromium version bump only |
| Size | ~8-10 GB |
| Purpose | Avoid re-downloading third_party + buildtools on every run |

### Cache 3: Build output (ninja incremental)

| Field | Value |
|---|---|
| Path | `$CHROMIUM_ROOT/src/out` |
| Key | `build-out-${{ runner.os }}-${{ runner.arch }}-${{ version }}-${{ github.sha }}` |
| Restore keys | `build-out-${{ runner.os }}-${{ runner.arch }}-${{ version }}-` |
| Purpose | Ninja `.ninja_log` + deps survive between runs for incremental build detection |
| Size | ~5-8 GB |

### Cache 4: ccache

| Field | Value |
|---|---|
| Path | `~/.ccache`, `~/AppData/Local/ccache`, `~/Library/Caches/ccache` |
| Key | `ccache-${{ runner.os }}-${{ runner.arch }}-${{ version }}` |
| Cache size | 5 GB (`CCACHE_MAXSIZE: 5G`) |
| Compiler check | `content` (hash based, not mtime) |
| Sloppiness | `pch_defines,time_macros` |
| No hash dir | Enabled (paths are relative) |

## Distributed Cache: sccache

### How it works

`sccache` is a drop-in `ccache` replacement that supports a **shared
S3-compatible backend**.  All runners, branches, and PRs share a single
object cache hosted in Cloudflare R2.

- **Cache hit on any runner** = no recompilation, the object is downloaded
  from R2 (~1-2s).
- **Cache miss** = compiles locally, result uploaded to R2 for future runs.
- **Pre-seeded archive**: a full build on a powerful machine populates the
  cache.  Subsequent CI runs start at ~70-80 % hit rates.

### Configuration

```yaml
env:
  SCCACHE_ENDPOINT: ${{ secrets.R2_ACCOUNT_ID }}.r2.cloudflarestorage.com
  SCCACHE_BUCKET: ${{ secrets.R2_BUCKET }}
  SCCACHE_REGION: auto
  SCCACHE_IDLE_TIMEOUT: "0"
  SCCACHE_CACHE_SIZE: 10G
  # Compiler wrappers (set in build step):
  CC_WRAPPER: sccache
  CXX_WRAPPER: sccache
```

### sccache vs ccache

| Feature | ccache | sccache |
|---|---|---|
| Local cache | ✓ | ✓ |
| Distributed (S3/R2) | ✗ | ✓ |
| Cross-runner sharing | ✗ | ✓ |
| Cross-branch sharing | ✗ | ✓ |
| macOS compatible | ✓ | ✓ |
| Windows compatible | ✓ | ✓ |
| Compiler wrapper | CCACHE_PREFIX | CC_WRAPPER/CXX_WRAPPER |

### Seed Cache Workflow

To pre-seed the sccache for a new Chromium version:

1. Run a full build on a powerful machine (e.g., WarpBuild 32-vCPU).
2. The sccache server uploads each compiled object to R2.
3. Subsequent CI runs on any runner download on cache hit (most objects).

No explicit seed step is needed — sccache fills automatically on first build.
To accelerate the first build, use:

```
sccache --start-server
sccache --upload-archive /path/to/prebuilt.tar.zst
```

## Ninja Parallelism

### Algorithm

```
1. Check BROWSEROS_NINJA_JOBS env var              → if > 0, use it
2. Query total physical RAM                          → if unknown, autoninja default
3. Compute jobs = RAM_GB / GB_PER_JOB:
   - Windows: GB_PER_JOB = 4   (no overcommit)
   - Linux/macOS: GB_PER_JOB = 2 (overcommit OK)
4. Cap at cpu_count()                                → cannot exceed physical cores
5. Enforce minimum 1 job
```

### Expected values on GitHub-hosted runners

| Runner | vCPU | RAM | GB/job | -j value | Steps/min (est.) |
|---|---|---|---|---|---|
| ubuntu-latest | 2 | 7 GB | 2 | 3 | ~90 |
| windows-latest | 2 | 7 GB | 4 | 1 | ~60 |
| macos-latest | 3 | 14 GB | 2 | 3 | ~90 |

### Override

```bash
export BROWSEROS_NINJA_JOBS=8   # force 8 parallel jobs
```

## Workflow Split Strategy

The pipeline is split into three logical jobs:

1. **plan** — resolve matrix (fast, ~1s)
2. **build** (matrix) — checkout → cache → compile → package → upload
3. **publish** — create GitHub release (conditional)

Compilation cannot be split across multiple GitHub jobs because Ninja has
no cross-host coordination.  However, the single build job is designed to
maximize throughput on whichever runner it runs on.

## Chromium Checkout Optimization

### Smart skip logic

`setup_chromium.py --step checkout` checks whether the pinned tag already
exists in the local git repository.  When `src/.git` is restored from cache
(Cache 2), the tag is already present and the fetch is skipped entirely.

### gclient sync skip

When Cache 2 (`src/third_party` + `src/buildtools`) is a cache hit,
`gclient sync` is skipped.  This saves 5-15 minutes per run.

Patch re-application is still needed every run (patches are applied on top
of the pristine Chromium checkout).  This takes ~1 minute.

## Incremental Build Detection

The build output cache (Cache 3) preserves Ninja's `.ninja_log` and
dependency files across runs.  Ninja uses these to:

- Detect which source files changed since the last build
- Recompile only what changed + reverse dependencies
- Skip unchanged translation units (instant ccache/sccache hit)

Without this cache, every run would be a full rebuild from scratch.

## Platform-Specific Optimizations

### Windows

- `symbol_level=0` — no debug info, greatly reduces link time and PDB I/O
- `DEPOT_TOOLS_WIN_TOOLCHAIN=0` — use system VS, not downloaded toolchain
- No overcommit → conservative 4 GB/job parallelism model
- `WINSPARKLE` — auto-update framework
- Mini_installer built via `autoninja setup mini_installer`

### Linux

- `use_sysroot=true` — pinned Debian sysroot for reproducible builds
- `install-build-deps.sh` — system dependencies
- `use_thin_lto=true` — faster than full LTO
- `ffmpeg_branding="Chrome"`, `proprietary_codecs=true`
- AppImage + .deb packaging

### macOS

- `use_system_xcode=true` — use pre-installed Xcode toolchain
- `enable_sparkle=true` — Sparkle auto-update framework
- `enable_platform_hevc=true` — HEVC support
- `.dmg` packaging

## Bottleneck Analysis

| Phase | Current time | Theoretical minimum | Bottleneck |
|---|---|---|---|
| Cache restore | 2-5 min | 30s | GitHub artifact download speed |
| Chromium checkout | 0-5 min | 0 min (cached) | Network (git fetch 200 MB) |
| gclient sync | 0-15 min | 0 min (cached) | Network (third_party download) |
| Patch application | ~1 min | ~1 min | I/O (git apply) |
| gn gen | 1-2 min | 30s | CPU (parsing all BUILD.gn) |
| **Compilation** | **2-15 h** | **2-15 h** | **CPU (GitHub 2-core runner)** |
| Packaging | 2-5 min | 2-5 min | I/O (appimagetool, dpkg) |
| Upload | 5 min | 5 min | Network (artifact upload) |

### The real bottleneck

GitHub-hosted runners have **2 virtual CPUs** (3 on macOS).  Chromium's
57,046 Ninja steps cannot be meaningfully accelerated beyond what
ccache/sccache provide on this hardware.  sccache provides ~70-80% hit
rates on subsequent builds, reducing effective compilations to ~5,000-10,000
steps per run — which still takes 60-120 minutes on a 2-core machine.

For faster builds, use self-hosted runners with more cores:

| Runner type | vCPU | Est. cold build | Est. warm build | Cost |
|---|---|---|---|---|
| GitHub hosted | 2-3 | 15+ h (timeout) | 2-3 h | Included |
| WarpBuild 16-core | 16 | ~2 h | 30-45 min | ~$0.50/h |
| AWS EC2 c7i.4xlarge | 16 | ~1.5 h | 25-35 min | ~$0.70/h |
| WarpBuild 32-core | 32 | ~50 min | 15-20 min | ~$1.00/h |

## Maintenance

### Cache invalidation

- **depot_tools**: never invalidated (keyed by runner type)
- **gclient**: invalidate only when Chromium version changes
- **build output**: per-commit SHA key with restore-key fallback
- **ccache**: invalidate when Chromium version changes or compiler changes

### Chrome version bump

1. Update `packages/browseros/CHROMIUM_VERSION`
2. Update `packages/browseros/BASE_COMMIT` (pinned commit hash)
3. Regenerate patches against new version
4. The CI gclient cache will miss → fresh `gclient sync`

### Troubleshooting

**Build fails with "out of memory"**:
Reduce `GB_PER_COMPILE_JOB` or set `BROWSEROS_NINJA_JOBS` lower.

**sccache not hitting**:
Check that `CC_WRAPPER=sccache` and `CXX_WRAPPER=sccache` are set in the
build step environment.  Verify R2 secrets are configured.

**Cache too large for GitHub limit (10 GB)**:
The gclient cache is the largest.  If it exceeds 10 GB, split it:
third_party in one cache, buildtools in another, .git in a third.

**Slow gclient sync**:
Enable `--no-history --shallow` flags (already configured).
If third_party cache is too large, exclude unused deps via
`gclient custom_deps` in `.gclient`.
