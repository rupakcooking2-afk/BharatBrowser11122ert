# Resumable Distributed Build System

Production-grade fault-tolerant distributed Chromium build system for Bharat
Browser CI. Survives GitHub Actions 6-hour timeouts through automatic
checkpointing, manifest validation, and intelligent recovery.

**Package:** `scripts/ci/build_system/` (12 modules)  
**CLI entry:** `scripts/ci/resume_build.py`  
**Workflow:** `.github/workflows/nightly-build.yml`  
**Storage:** Cloudflare R2 under `build-resume/{platform}/`

---

## Build Architecture

### State Machine

The system is a deterministic state machine. On each invocation the
orchestrator reads `build_state.json`, loads the current state, runs the
corresponding handler, and transitions to the next state.

```
IDLE ──► PREPARING ──► DOWNLOADING ──► PATCHING ──► CONFIGURING ──►
    COMPILING ──► CHECKPOINTING ──► VERIFYING ──► PACKAGING ──►
    RELEASING ──► COMPLETE

    FAILED ◄── (any handler on exception)
    RECOVERING ──► COMPILING  (on transient failure)
```

States in `scripts/ci/build_system/orchestrator.py:30`:

| State | Purpose |
|---|---|
| `IDLE` | Entry point; transitions immediately to `PREPARING` |
| `PREPARING` | Populates `build_state.json` with environment snapshot |
| `DOWNLOADING` | Syncs build artifacts from R2 (`aws s3 sync`) |
| `PATCHING` | Applies `patches/*.patch` via `git am` |
| `CONFIGURING` | Runs `gn gen` to regenerate Ninja files |
| `COMPILING` | Runs `autoninja`; creates periodic checkpoints |
| `CHECKPOINTING` | Final checkpoint after compilation |
| `VERIFYING` | Validates build artifacts with `BuildValidator` |
| `PACKAGING` | Release validation; marks packaging complete |
| `RELEASING` | Marks release complete |
| `COMPLETE` | Terminal success state |
| `FAILED` | Terminal failure state |
| `RECOVERING` | Runs `auto_repair` then returns to `COMPILING` |

### Component Map

| Module | File | Responsibility |
|---|---|---|
| `orchestrator.py` | 377 lines | State machine, workflow orchestration |
| `checkpoint.py` | 543 lines | Rolling checkpoint create/restore/prune |
| `manifest.py` | 417 lines | Build manifest read/write/validate |
| `recovery.py` | 705 lines | Corruption detection and auto-repair |
| `validator.py` | 632 lines | Build integrity validation |
| `uploader.py` | 318 lines | R2 upload/download with multipart support |
| `security.py` | 168 lines | SHA-256 checksums, HMAC signing |
| `retry.py` | 414 lines | Exponential backoff with transience detection |
| `disk_manager.py` | 338 lines | Disk usage monitoring and cleanup |
| `performance.py` | 290 lines | Compile speed, cache ratio, overhead tracking |
| `release_validator.py` | 405 lines | Pre-release artifact validation |
| `dashboard.py` | 119 lines | JSON status payload for CI dashboards |

### GitHub Workflow Integration

The CI workflow (`nightly-build.yml`) maps coarse states to job steps while
the Python orchestrator manages fine-grained sub-states:

```yaml
steps:
  # YAML-level states
  - name: State: PREPARING — resolve paths
  - name: State: DOWNLOADING — ensure Chromium checkout
  - name: State: PATCHING/CONFIGURING — apply patches and gn gen
  - name: State: COMPILING — orchestrate resumable build     # delegates to resume_build.py
  - name: State: PACKAGING — Windows installer
  - name: State: RELEASING — upload artifacts
```

The `COMPILING` step runs `resume_build.py orchestrate` which handles
checkpointing, recovery, and timeout within a single GitHub Actions step.
If the step times out (GitHub's 6-hour limit), the next scheduled run
re-enters `COMPILING`, detects a checkpoint, restores it, and continues.

### R2 Object Layout

```
build-resume/{platform}/
  latest.txt                    # Atomic pointer to current checkpoint
  build_state.json              # Build manifest (also stored locally)
  checkpoint_001.tar.gz         # Rolling checkpoint snapshots
  checkpoint_002.tar.gz
  ...
  BUILD_COMPLETE                # Legacy sentinel (backward compat)
```

### Backward Compatibility

Four legacy commands remain functional:

- `status` — checks `BUILD_COMPLETE` sentinel in R2
- `run` — synchronous `orchestrate` wrapper
- `complete` — writes `BUILD_COMPLETE` sentinel
- `clear` — deletes all R2 state for a platform

---

## Checkpoint Lifecycle

### Trigger Conditions

Checkpoints are created in `CheckpointManager.should_checkpoint()` when
either threshold is crossed:

- **Time-based:** 20 minutes since last checkpoint
  (`CHECKPOINT_INTERVAL_MINUTES = 20`)
- **Target-based:** 1000 Ninja targets completed since last checkpoint
  (`CHECKPOINT_INTERVAL_TARGETS = 1000`)

### Creation (`create_checkpoint`)

1. Determine next sequence number (max existing + 1)
2. Build a `tar.gz` archive containing:
   - `.ninja_log` — Ninja build log with timing data
   - `.ninja_deps` — Dependency graph
   - `args.gn` — GN build arguments
   - `build.ninja` — Generated Ninja build file
   - `build_state.json` — Build manifest
   - All other files in the build output directory
3. Compute local SHA-256 checksum of the tarball
4. Upload to R2: `build-resume/{platform}/checkpoint_{NNN}.tar.gz`
5. Re-download and verify checksum (upload integrity check)
6. Atomically write sequence number to `latest.txt` in R2

### Retention

`prune_old_checkpoints()` keeps the 5 most recent checkpoints
(`CHECKPOINT_RETENTION = 5`) and deletes older ones. `latest.txt` and
the latest valid checkpoint are never deleted.

### Listing

`list_checkpoints()` queries R2 via `aws s3api list-objects` with prefix
`build-resume/{platform}/checkpoint_` and returns sorted metadata (seq,
key, size, timestamp).

---

## Resume Mechanism

### Entry Point

```bash
python scripts/ci/resume_build.py \
  --platform linux-x64 \
  --out-dir out/Default_x64 \
  orchestrate
```

### Resume Flow

1. `WorkflowOrchestrator.__init__()` loads or creates `build_state.json`
2. `run()` dispatches to the handler for the current state
3. `handle_compiling()` checks `get_latest_checkpoint()`:
   - If a checkpoint exists → `restore_latest()` downloads and extracts it
   - If no checkpoint → starts fresh compilation
4. Ninja is launched via `subprocess.Popen` with `-k 0` (keep going)
5. A polling loop monitors progress:
   - Reads `.ninja_log` every 10 seconds via `read_ninja_stats()`
   - Checks `should_checkpoint()` against elapsed time and target count
   - Creates checkpoints in the background during compilation
   - Checks `should_stop()` against the 6-hour timeout

### Manifest Validation on Resume

Before allowing a resume, `BuildManifest.load()` verifies:

- **Checksum integrity** — SHA-256 of all fields (excluding `checksum`)
- **Environment match** — `validate_environment()` compares stored vs
  current: chromium version, browseros commit, repo commit, platform,
  architecture, Python version, compiler version

Any mismatch triggers `ChecksumError` or returns a non-empty mismatch list.
The caller (orchestrator or recovery) responds by forcing a clean build.

### Timeout Handling

`WORKFLOW_TIMEOUT_HOURS = 6` — when elapsed time exceeds this, the
polling loop terminates Ninja, transitions to `FAILED`, and returns.
The workflow step exits non-zero. GitHub Actions marks the step as failed
but the job continues due to `fail-fast: false`.

On the next scheduled run, the workflow re-enters `COMPILING`, finds the
checkpoint in R2, restores it, and resumes.

---

## Manifest Format

`build_state.json` is stored both locally (`out/Default_{arch}/`) and
remotely (`build-resume/{platform}/build_state.json`). It is validated
by SHA-256 checksum on every load.

```json
{
  "chromium_version": "130.0.6723.58",
  "browseros_commit": "a1b2c3d4...",
  "bharat_browser_commit": "e5f6g7h8...",
  "repository_commit_sha": "e5f6g7h8...",
  "gn_args_hash": "sha256-of-args.gn",
  "build_gn_hash": "sha256-of-build.ninja",
  "patch_hash": "sha256-of-all-patch-files",
  "python_version": "3.12.0 (default, ...)",
  "compiler_version": "clang version 18.0.0 (...)",
  "platform": "linux",
  "architecture": "x64",
  "toolchain_version": "clang version 18.0.0 (...)",
  "build_directory": "out/Default_x64",
  "completed_targets": 0,
  "estimated_total_targets": 57046,
  "last_successful_upload": "",
  "last_successful_checkpoint": "",
  "build_complete": false,
  "packaging_complete": false,
  "release_complete": false,
  "checksum": "sha256-of-all-other-fields",
  "timestamp": "2025-01-15T10:30:00+00:00",
  "workflow_state": "IDLE"
}
```

### Field Reference

| Field | Type | Description |
|---|---|---|
| `chromium_version` | string | `MAJOR.MINOR.BUILD.PATCH` from `chrome/VERSION` |
| `browseros_commit` | string | Git SHA of `packages/browseros` |
| `bharat_browser_commit` | string | Git SHA of repo root |
| `repository_commit_sha` | string | Same as bharat_browser_commit |
| `gn_args_hash` | string | SHA-256 of `args.gn` content |
| `build_gn_hash` | string | SHA-256 of `build.ninja` content |
| `patch_hash` | string | SHA-256 of all `.patch`/`.diff` files (sorted) |
| `python_version` | string | `sys.version` output |
| `compiler_version` | string | First line of `cc --version` (or `cl.exe` on Windows) |
| `platform` | string | `sys.platform` value |
| `architecture` | string | Normalised: `x64`, `x86`, `arm64`, `arm` |
| `toolchain_version` | string | Same as compiler_version |
| `build_directory` | string | Relative build output path |
| `completed_targets` | int | Ninja targets completed |
| `estimated_total_targets` | int | Total build targets (from `build.ninja` or fallback 57046) |
| `last_successful_upload` | string | ISO timestamp of last R2 upload |
| `last_successful_checkpoint` | string | ISO timestamp of last checkpoint |
| `build_complete` | bool | Ninja exited with code 0 |
| `packaging_complete` | bool | Packaging step finished |
| `release_complete` | bool | Release step finished |
| `checksum` | string | SHA-256 of all other fields (self-protection) |
| `timestamp` | string | ISO 8601 UTC timestamp |
| `workflow_state` | string | Current state machine state |

### Checksum Mechanism

`_compute_checksum()` serialises all fields **except** `checksum` to a
deterministic JSON string (keys sorted, `sort_keys=True`) and computes
SHA-256. On `load()`, the stored checksum is compared against the
recomputed value. Mismatch raises `ChecksumError`.

---

## Recovery Flow

### Entry Points

1. **Automatic:** `handle_failed()` in orchestrator calls
   `recovery_mgr.attempt_recovery()`
2. **Explicit:** `resume_build.py recover` CLI command
3. **Inline:** `auto_repair()` convenience function called from
   `handle_recovering()`

### Detection

`RecoveryManager.detect_issues()` scans seven categories:

| # | Component | Issue Types | Severity |
|---|---|---|---|
| 1 | Manifest (`build_state.json`) | missing, corrupt | critical |
| 2 | Checkpoint (R2) | missing, corrupt | warning/error |
| 3 | `.ninja_log` | missing | error |
| 4 | `.ninja_deps` | missing | error |
| 5 | `build.ninja` | missing | critical |
| 6 | `args.gn` | missing | critical |
| 7 | Disk space | disk_space (< 5 GB free) | error |
| 8 | Symlinks | interrupted (stale) | warning |

### Recovery Actions

| Issue | Handler | Action |
|---|---|---|
| Missing/corrupt manifest | `_recover_manifest()` | Create new manifest from current environment |
| Missing/corrupt checkpoint | `_recover_checkpoint()` | Walk back N-1, N-2... restore newest valid |
| Missing `.ninja_log`/`.ninja_deps` | `_recover_ninja_state()` | Force `gn gen` regeneration |
| Missing `build.ninja`/`args.gn` | `force_reconfigure()` | Run `gn gen` |
| Low disk space | `_recover_disk_space()` | Delete temp files, stale caches, old checkpoints |
| Stale symlinks | `_recover_symlinks()` | Remove broken symlinks |
| Partial uploads | `_recover_partial_upload()` | List and complete multipart uploads |

### Outcome

`RecoveryResult` has three states:

- **recovered** — all issues fixed
- **partial** — some issues fixed, some remain
- **failed** — nothing could be fixed; manifest set to `FAILED`

### Checkpoint Chain Repair

`repair_checkpoint_chain()` downloads every checkpoint, verifies gzip
magic bytes (`\x1f\x8b`), deletes corrupt ones, and updates `latest.txt`
to point at the newest valid checkpoint.

---

## Failure Scenarios

### 1. GitHub Actions Timeout (6-hour limit)

**Symptom:** Job hits `timeout-minutes: 420`. Step marked as failure.

**Handling:** Ninja was already terminated by `should_stop()` at ~6 hours.
Checkpoint exists in R2. Next scheduled run re-enters `COMPILING`,
restores latest checkpoint, resumes compilation.

**Resume overhead:** ~2 minutes (download + verify checkpoint).

### 2. Runner Preemption / OOM Kill

**Symptom:** Process killed by SIGKILL (exit code 137) or SIGTERM (143).

**Handling:** Last checkpoint in R2 is intact. Runner restart re-enters
`COMPILING`, `restore_latest()` recovers state. Ninja's `-k 0` flag
ensures partial targets are rebuilt.

**Risk:** If killed during checkpoint creation, that checkpoint may be
partial. Recovery walks back to previous valid checkpoint.

### 3. Corrupt Checkpoint Tarball

**Symptom:** `ChecksumError` on upload verification, or gzip magic byte
check fails on download.

**Handling:** `create_checkpoint()` aborts and logs error — does not
update `latest.txt`. Next checkpoint attempt starts fresh.
`restore_checkpoint()` returns `False`; caller falls back to previous
checkpoint.

### 4. Environment Drift

**Symptom:** `validate_environment()` returns mismatches — different
compiler version, Python version, or git commit.

**Handling:** Manifest validation fails → clean build. The mismatch list
is logged for diagnostics. Recovery manager creates a new manifest.

### 5. R2 Unavailable

**Symptom:** `aws s3api` commands fail with connection errors.

**Handling:** `UploadManager._run_aws()` raises
`CalledProcessError`. Callers in checkpoint and recovery paths catch
exceptions, log warnings, and continue. The build proceeds without
checkpointing. On next run, if R2 is back, the last successful
checkpoint is used.

### 6. `gn gen` Failure

**Symptom:** `handle_configuring()` exits non-zero.

**Handling:** Transitions to `FAILED`. Recovery's `force_reconfigure()`
retries. On repeated failure, unrecoverable — requires manual
intervention.

### 7. Disk Space Exhaustion

**Symptom:** Ninja fails to write object files; `detect_issues()` finds
< 5 GB free.

**Handling:** `_recover_disk_space()` calls
`DiskManager.enforce_disk_quota()` which deletes temp files, old
ccache caches (> 30 GB), and reduces checkpoint retention to 3. If
still insufficient, recovery sets state to `partial`.

### 8. Ninja Compiler Error (Exit Code 1)

**Symptom:** Ninja exits 1 with compilation errors in stderr.

**Handling:** `_is_transient_failure(1)` returns `False` for compiler
errors (detected via "error:" in stderr). Transitions to `FAILED`.
No retry — requires source fix.

### 9. Multipart Upload Abandoned

**Symptom:** Abandoned upload parts accumulate in R2.

**Handling:** `_recover_partial_upload()` lists multipart uploads
and attempts completion. Orphaned parts are eventually cleaned.

---

## Performance Expectations

### Compile Times

| Platform | Fresh Build (est.) | Resume Build (est.) | Notes |
|---|---|---|---|
| Linux x64 | 90–180 min | 60–150 min | Fastest runner |
| Windows x64 | 180–360 min | 120–300 min | Slower filesystem |
| macOS arm64 | 120–240 min | 80–200 min | M-series efficiency |

Actual times depend on runner spec, ccache hit rate, and number of
checkpoint intervals survived.

### Resume Overhead

| Operation | Typical Time | Notes |
|---|---|---|
| Download + extract checkpoint | 30–120 s | Depends on build dir size and bandwidth |
| Manifest load + validation | < 1 s | Local file, SHA-256 verify |
| Environment validation | 2–5 s | Git hashes + compiler probes |
| **Total resume overhead** | **~2 min** | Negligible vs compile time |

### Checkpoint Overhead

| Metric | Value |
|---|---|
| Trigger interval | 20 min OR 1000 targets |
| Tarball creation | 5–30 s (depends on I/O) |
| Upload + verify | 10–60 s (depends on network) |
| Overhead ratio | < 2% of compile time |
| Storage per checkpoint | 50–500 MB (compressed) |
| Retention | 5 checkpoints |

### Cache Efficiency

ccache is configured with:
- `CCACHE_MAXSIZE: 5G`
- `CCACHE_COMPILERCHECK: content`
- `CCACHE_NOHASHDIR: true`
- `CCACHE_SLOPPINESS: pch_defines,time_macros`

Expected hit rate after first full build: 60–85% on resume.

---

## Maintenance Guide

### CLI Reference

```bash
# Full state-machine orchestration (primary entry point)
python resume_build.py --platform linux-x64 orchestrate

# Build manifest management
python resume_build.py --platform linux-x64 manifest create
python resume_build.py --platform linux-x64 manifest validate

# Checkpoint operations
python resume_build.py --platform linux-x64 checkpoint list
python resume_build.py --platform linux-x64 checkpoint prune

# Recovery
python resume_build.py --platform linux-x64 recover

# Validation
python resume_build.py --platform linux-x64 validate

# Performance report
python resume_build.py --platform linux-x64 performance

# Disk management
python resume_build.py --platform linux-x64 disk status
python resume_build.py --platform linux-x64 disk clean

# Dashboard
python resume_build.py --platform linux-x64 dashboard

# Legacy
python resume_build.py --platform linux-x64 status
python resume_build.py --platform linux-x64 clear
```

### Adding a New Platform

1. Add a matrix entry in `nightly-build.yml` `plan` job with platform,
   arch, runner, config, and timeout
2. Ensure R2 secrets (`R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`,
   `R2_SECRET_ACCESS_KEY`, `R2_BUCKET`) are set in GitHub Actions secrets
3. The platform string convention is `{os}-{arch}` (e.g. `linux-x64`,
   `windows-x64`, `macos-arm64`)

### Changing Checkpoint Parameters

Edit constants in `checkpoint.py`:

```python
CHECKPOINT_INTERVAL_MINUTES = 20   # Time threshold (minutes)
CHECKPOINT_INTERVAL_TARGETS = 1000 # Target count threshold
CHECKPOINT_RETENTION = 5           # Number of checkpoints to keep
```

### Troubleshooting

**Build fails to resume:** Check R2 connectivity (`aws s3 ls` with
proper env vars). Verify `latest.txt` exists under
`build-resume/{platform}/`. Run `resume_build.py recover` to attempt
auto-repair.

**Checkpoint creation hangs:** Check disk space and R2 write
permissions. Large build directories may slow tarball creation — the
`/tmp` directory needs sufficient space.

**Manifest checksum errors:** The environment has changed since the
manifest was created. Run `manifest create` to regenerate. Common
causes: Chromium version bump, compiler update, Python version change.

**Legacy commands still in use:** The `status`, `run`, `complete`, and
`clear` commands work with the `BUILD_COMPLETE` sentinel and do not
interact with the checkpoint system. They are maintained for backward
compatibility only.

### Logging

All modules log through Python's `logging` module under the
`scripts.ci.build_system` hierarchy. Set `LOG_LEVEL=DEBUG` in the
workflow environment for detailed diagnostics.

### Testing

Tests are not yet implemented for this system. When adding tests, cover:

- State machine transitions (every valid path)
- Checkpoint create/restore with mock R2
- Manifest checksum validation (tamper detection)
- Recovery handler dispatch for each issue type
- Retry transient vs permanent failure classification
