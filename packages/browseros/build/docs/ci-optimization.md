# CI/CD Build Optimization

Optimizations applied to reduce Bharat Browser Chromium build time on CI from
~6 hours to an estimated ~2-2.5 hours (subsequent runs) / ~3.5-4 hours (first run).

## Changes

### 1. New CI-Optimized GN Flags Files

Three new files with aggressive CI optimizations (ccache, ThinLTO, disabled
unused features):

| File | Purpose |
| --- | --- |
| `build/config/gn/flags.linux.release.ci.gn` | Linux CI |
| `build/config/gn/flags.windows.release.ci.gn` | Windows CI |
| `build/config/gn/flags.macos.release.ci.gn` | macOS CI |

Key additions vs. the standard `release.gn` files:

| Flag | Effect | Est. Speedup |
| --- | --- | --- |
| `cc_wrapper = "ccache"` | Reuse compiled objects across runs | 30-50% (subseq.) |
| `use_thin_lto = true` | Faster linking vs. full LTO | 20-30% |
| `enable_nacl = false` | Skip NaCl compilation | marginal |
| `enable_remoting = false` | Skip remoting compilation | marginal |
| `enable_hangout_services_extension = false` | Skip Hangouts extension | marginal |

### 2. Updated Release GN Files

`flags.{linux,windows,macos}.release.gn` now include `use_thin_lto = true` so
all builds (local + CI) benefit from faster linking.

### 3. Updated Config YAMLs

`release.{linux,windows,macos.arm64}.ci.yaml` and
`release.macos.arm64.noupload.yaml` now reference `release.*.ci.gn` files.

### 4. GitHub Actions Workflows

**`nightly-release.yml`** (main unsigned CI):
- Install & configure ccache (Linux: apt, macOS: brew, Windows: choco)
- Cache `~/.ccache`, `~/Library/Caches/ccache`, `~/AppData/Local/ccache`
- Cache `depot_tools/` to avoid re-cloning
- Cache `src/third_party/` + `src/buildtools/` (gclient deps) keyed by
  Chromium version — skip `gclient sync` on cache hit
- Cache `src/out/` (build output) for ninja incremental detection
- Replace full `clean` module with targeted out/ removal + `git reset --hard`
  (preserves third_party for gclient cache)
- Reduce timeout from 780/720 to 420 min
- Report ccache stats after build

**`nightly-macos-build.yml`** (signed macOS nightly):
- Install & configure ccache via brew
- Cache ccache + gclient dependencies
- Replace clean module with targeted out/ removal
- Report ccache stats after build

### 5. Compile Module Enhancement

`build/modules/compile/standard.py`:
- `log_ccache_stats()` — logs ccache hit/miss/size at start and end of build
- `compute_ninja_jobs()` — computes optimal parallelism based on available RAM

## Estimated Performance

| Scenario | Before | After (est.) |
| --- | --- | --- |
| First CI run (no caches) | ~6 h | ~3.5-4 h |
| Subsequent runs (warm caches) | ~6 h | ~2-2.5 h |
| ccache hit rate (unchanged Chromium) | N/A | ~70-80% |
| ccache hit rate (patches only changed) | N/A | ~95%+ |

## Cache Keys

- `depot_tools`: keyed by OS + arch (rarely changes)
- `gclient`: keyed by OS + arch + Chromium version (changes only when pinned
  version bumps)
- `ccache`: keyed by OS + arch + Chromium version (survives patch changes)
- `build out`: keyed by OS + arch + Chromium version + commit SHA (most
  specific, falls back to nearest prior for partial incremental)

## Cache Budget

GitHub `actions/cache` has a 10 GB limit per repository per key.

| Cache | Est. Size | Notes |
| --- | --- | --- |
| ccache | ~2-3 GB | 3 GB max configured |
| depot_tools | ~200 MB | |
| gclient (third_party) | ~8-10 GB | Close to limit, high value |
| build out | ~5-8 GB | Compressed by actions/cache |

If combined size exceeds 10 GB, priority order: ccache > gclient > build out.
The workflow has separate cache keys per cache type so each is evicted
independently.
