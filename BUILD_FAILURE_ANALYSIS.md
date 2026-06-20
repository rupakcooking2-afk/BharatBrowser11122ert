# BUILD FAILURE ANALYSIS: `//chrome/browser/browseros:browseros`

## Root Cause

The GN target `//chrome/browser/browseros:browseros` was **never defined** in the BrowserOS BUILD.gn patch.

The `chrome/browser/BUILD.gn` patch (`chromium_patches/chrome/browser/BUILD.gn` line 26) adds `"//chrome/browser/browseros"` to the `static_library("browser")` deps list. This resolves to `//chrome/browser/browseros:browseros`.

However, the `chrome/browser/browseros/BUILD.gn.patch` only defined two groups:
- `group("browseros_server_resources")` — was empty, no deps
- `group("browseros_bundled_extensions")` — pointed to `//chrome/browser/browseros/bundled_extensions`

**No target named `"browseros"` existed**, causing GN to emit:
```
ERROR Unresolved dependencies.
//chrome/browser:browser needs //chrome/browser/browseros:browseros
```

## Files Involved

| File | Role |
|------|------|
| `chromium_patches/chrome/browser/BUILD.gn` (line 26) | Adds `"//chrome/browser/browseros"` dep to `static_library("browser")` |
| `chromium_patches/chrome/browser/browseros/BUILD.gn.patch` | **Missing `group("browseros")`** — the fix target |
| `chromium_patches/chrome/browser/browseros/server/BUILD.gn.patch` | Defines `browseros_server_resources` (group), `server` (source_set), etc. |
| `chromium_patches/chrome/browser/browseros/bundled_extensions/BUILD.gn` | Defines `bundled_extensions` (copy/bundle_data) |
| `chromium_patches/chrome/browser/browseros/core/BUILD.gn` | Defines `core`, `prefs`, `action_utils` source_sets |
| `chromium_patches/chrome/browser/browseros/metrics/BUILD.gn` | Defines `metrics` source_set |

## Fix Applied

Updated `BUILD.gn.patch` (16 lines → 22 lines) to add:

```gn
group("browseros") {
  public_deps = [
    ":browseros_server_resources",
    ":browseros_bundled_extensions",
  ]
}

group("browseros_server_resources") {
  public_deps = [ "//chrome/browser/browseros/server:browseros_server_resources" ]
}

group("browseros_bundled_extensions") {
  public_deps = [ "//chrome/browser/browseros/bundled_extensions" ]
}
```

This creates the `//chrome/browser/browseros:browseros` label that `//chrome/browser:browser` expects, and chains it through existing sub-targets.

## Dependency Chain (After Fix)

```
//chrome/browser:browser  (static_library)
  └─> //chrome/browser/browseros:browseros  ← NEW - previously missing
        ├─> :browseros_server_resources
        │     └─> //chrome/browser/browseros/server:browseros_server_resources
        │           ├─> action("validate_browseros_resources")  [deferred, TODO]
        │           ├─> source_set("server")
        │           ├─> bundle_data("browseros_resources_bundle")  [macOS]
        │           └─> copy("browseros_resources_copy")  [Win/Linux]
        └─> :browseros_bundled_extensions
              └─> //chrome/browser/browseros/bundled_extensions
                    ├─> copy("bundled_extensions")  [!is_mac]
                    └─> bundle_data("bundled_extensions")  [macOS]
```

## Verification

All 5 BUILD.gn patches validated with matching hunk counts:

| Patch File | Declared | Actual | Status |
|------------|----------|--------|--------|
| `browseros/BUILD.gn.patch` | 22 | 22 | ✓ |
| `browseros/bundled_extensions/BUILD.gn` | 32 | 32 | ✓ |
| `browseros/core/BUILD.gn` | 48 | 48 | ✓ |
| `browseros/metrics/BUILD.gn` | 39 | 39 | ✓ |
| `browseros/server/BUILD.gn.patch` | 131 | 131 | ✓ |

## Why Fix Works

`gn gen out/Default_x64` will succeed because:
1. `//chrome/browser/browseros:browseros` now resolves to `group("browseros")` in `browseros/BUILD.gn`
2. That group's deps chain to existing targets (`server:browseros_server_resources`, `bundled_extensions`)
3. All BrowserOS functionality is preserved — no features removed, no targets deleted
