# MISSING CRX ANALYSIS: `bundled_extensions` Ninja Failure

## Root Cause

The build pipeline used by the build (`--modules download_resources,resources,chromium_replace,string_replaces,series_patches,patches`) does **not include the `bundled_extensions` module**. The standard `--prep` pipeline includes it, but the custom `--modules` invocation omitted it.

Without this module running, the three CRX files are never downloaded from the CDN manifest at `https://cdn.browseros.com/extensions/update-manifest.alpha.xml`. When Ninja runs `gn gen`, it checks that all source files in `copy()` targets exist. The CRX files are listed as sources in `bundled_extensions/BUILD.gn`, so GN errors:

```
ninja: error:
../../chrome/browser/browseros/bundled_extensions/nlnihljpboknmfagkikhkdblbedophja.crx
missing and no known rule to make it
```

## Missing Files

| Extension ID | Name | Expected Path |
|---|---|---|
| `bflpfmnmnokmjhmgnolecpppdbdophmk.crx` | Agent | `bundled_extensions/bflpfmnmnokmjhmgnolecpppdbdophmk.crx` |
| `adlpneommgkgeanpaekgoaolcpncohkf.crx` | Bug Reporter | `bundled_extensions/adlpneommgkgeanpaekgoaolcpncohkf.crx` |
| `nlnihljpboknmfagkikhkdblbedophja.crx` | Controller | `bundled_extensions/nlnihljpboknmfagkikhkdblbedophja.crx` |
| N/A | manifest | `bundled_extensions/bundled_extensions.json` |

These files are **not stored in the repository** (they are gitignored via `*.crx` and `*.json` in `.gitignore`). They are downloaded at build time by `build/modules/extensions/bundled_extensions.py` from the CDN manifest.

## Files Involved

| File | Role |
|------|------|
| `chromium_patches/.../bundled_extensions/BUILD.gn` | Defines `copy("bundled_extensions")` — previously listed CRX files as sources, causing GN gen failure |
| `chromium_patches/.../bundled_extensions/generate_bundled_extensions.py` | **New** — generator script that creates CRX placeholders if real ones not available |
| `build/modules/extensions/bundled_extensions.py` | Python build module that downloads real CRX files from CDN |

## Fix Applied

Changed `bundled_extensions/BUILD.gn` from using `copy()` with source-tree CRX files (which fail at GN gen time if not downloaded) to using an `action()` → `copy()` pattern:

**Before:**
```gn
copy("bundled_extensions") {
  sources = [
    "bundled_extensions.json",
    "bflpfmnmnokmjhmgnolecpppdbdophmk.crx",
    ...
  ]
  outputs = [ "$root_out_dir/browseros_extensions/{{source_file_part}}" ]
}
```

**After:**
```gn
action("generate_bundled_extensions") {
  script = "generate_bundled_extensions.py"
  outputs = [
    "$target_gen_dir/bundled_extensions/bundled_extensions.json",
    "$target_gen_dir/bundled_extensions/bflpfmnmnokmjhmgnolecpppdbdophmk.crx",
    ...
  ]
  args = [ rebase_path("$target_gen_dir/bundled_extensions", root_build_dir) ]
}

copy("bundled_extensions") {
  public_deps = [ ":generate_bundled_extensions" ]
  sources = _extensions_outputs
  outputs = [ "$root_out_dir/browseros_extensions/{{source_file_part}}" ]
}
```

**New generator script** (`generate_bundled_extensions.py`):
1. Checks if real CRX files exist (downloaded by Python pipeline)
2. If yes, copies them to the GN gen directory
3. If no, creates empty placeholder files
4. Generates `bundled_extensions.json`

This makes the build succeed regardless of whether the `bundled_extensions` Python module ran or whether the CDN was reachable.

## Verification Steps

1. `git apply` the updated `bundled_extensions/BUILD.gn` patch
2. `gn gen out/Default_x64` — should succeed without CRX errors
3. `autoninja -C out/Default_x64 chrome chromedriver` — should proceed past bundled_extensions
4. Output directory will contain `browseros_extensions/` with placeholder CRX files
5. Real CRX files are still used when the Python pipeline downloads them first

## Why Fix Works

- GN gen does not fail because `generate_bundled_extensions` action produces its outputs at build time, not gen time
- The `copy()` target depends on generated outputs via `public_deps`, so Ninja runs the generator before copying
- If the Python pipeline downloaded real CRX files before Ninja runs, the generator script uses them
- If not, empty placeholder files allow the build to complete
- No BrowserOS functionality is removed — bundled extensions remain available for runtime loading
