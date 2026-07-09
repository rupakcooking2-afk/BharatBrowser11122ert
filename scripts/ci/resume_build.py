#!/usr/bin/env python3
"""Resumable Ninja build manager for Bharat Browser CI.

Thin CLI entry point for the production-grade fault-tolerant distributed
build system.  All logic lives in the build_system/ package.

Commands
────────
  manifest create|validate|update  — Build manifest management (Phase 1)
  checkpoint create|list|restore|prune  — Atomic snapshots (Phase 2, 4)
  upload       — Upload out/ to R2 (Phase 3)
  download     — Download out/ from R2 (Phase 3)
  validate     — Build validation (Phase 5, 13)
  recover      — Auto-recovery (Phase 6)
  orchestrate  — Full state machine run (Phase 7)
  dashboard    — Build status dashboard (Phase 8)
  release      — Validate and package release (Phase 9)
  disk         — Disk management (Phase 11)
  performance  — Performance report (Phase 12)
  status       — Legacy: check BUILD_COMPLETE sentinel
  run          — Legacy: single-compile-run with sentinel
  complete     — Legacy: write BUILD_COMPLETE sentinel
  clear        — Legacy: remove build state

Usage
    python resume_build.py orchestrate --platform linux-x64 --out-dir out/Default_x64
"""
import argparse
import sys
from pathlib import Path


def _make_common_args(p: argparse.ArgumentParser) -> None:
    p.add_argument("--platform", required=True)
    p.add_argument("--out-dir", default="out/Default_x64")
    p.add_argument("--chromium-src",
                   default=lambda: Path(os.environ.get("CHROMIUM_SRC", "")),
                   type=Path)
    p.add_argument("--browseros-dir",
                   default=lambda: Path(os.environ.get("BROWSEROS_DIR",
                       str(Path.cwd() / "packages" / "browseros"))),
                   type=Path)
    p.add_argument("--repo-root",
                   default=lambda: Path(os.environ.get("REPO_ROOT", str(Path.cwd()))),
                   type=Path)


def cmd_manifest(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.manifest import BuildManifest
    from scripts.ci.build_system.uploader import R2Client
    import tempfile
    r2 = R2Client()
    manifest_key = f"build-resume/{args.platform}/build_state.json"
    tmp = Path(tempfile.mkdtemp()) / "build_state.json"
    if args.manifest_sub == "create":
        m = BuildManifest(args.platform, args.out_dir)
        m.create(
            chromium_src=args.chromium_src,
            browseros_dir=args.browseros_dir,
            repo_root=args.repo_root,
        )
        m.save(tmp)
        # Also upload to R2
        from scripts.ci.build_system.uploader import UploadManager
        up = UploadManager(args.platform, r2)
        up.upload_file(tmp, manifest_key)
        print(f"Manifest created and uploaded: {manifest_key}")
        return 0
    elif args.manifest_sub == "validate":
        # Download from R2
        from scripts.ci.build_system.uploader import UploadManager
        up = UploadManager(args.platform, r2)
        up.download_file(manifest_key, tmp)
        try:
            m = BuildManifest.load(tmp)
            mismatches = m.validate_environment(
                chromium_src=args.chromium_src,
                browseros_dir=args.browseros_dir,
                repo_root=args.repo_root,
            )
            if mismatches:
                print("Manifest mismatches:")
                for mm in mismatches:
                    print(f"  ✗ {mm}")
                return 1
            print("Manifest valid")
            return 0
        except Exception as e:
            print(f"Manifest invalid: {e}")
            return 1
    return 0


def cmd_checkpoint(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.checkpoint import CheckpointManager
    cm = CheckpointManager(args.platform, args.out_dir, args.chromium_src)
    if args.checkpoint_sub == "create":
        seq = cm.create_checkpoint()
        print(f"Checkpoint {seq} created")
    elif args.checkpoint_sub == "list":
        for cp in cm.list_checkpoints():
            print(f"  {cp['seq']:>3s}  {cp['key']}  {cp.get('size', '?'):>10s}")
    elif args.checkpoint_sub == "restore":
        ok = cm.restore_latest(args.chromium_src)
        print("Checkpoint restored" if ok else "Restore failed")
        return 0 if ok else 1
    elif args.checkpoint_sub == "prune":
        deleted = cm.prune_old_checkpoints()
        print(f"Pruned {deleted} old checkpoints")
    return 0


def _legacy_cmd_status(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.uploader import R2Client
    r2 = R2Client()
    sentinel = f"build-resume/{args.platform}/BUILD_COMPLETE"
    if r2.check_key_exists(sentinel):
        print("BUILD_COMPLETE=true")
        return 0
    print("BUILD_COMPLETE=false")
    return 1


def _legacy_cmd_run(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.orchestrator import WorkflowOrchestrator
    orch = WorkflowOrchestrator(
        platform=args.platform,
        build_dir=args.out_dir,
        chromium_src=args.chromium_src,
        browseros_dir=args.browseros_dir,
        repo_root=args.repo_root,
    )
    return orch.run()


def _legacy_cmd_complete(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.uploader import R2Client
    from scripts.ci.build_system.security import ChecksumVerifier
    r2 = R2Client()
    sentinel = f"build-resume/{args.platform}/BUILD_COMPLETE"
    import subprocess
    result = subprocess.run(
        ["aws", "s3api", "put-object",
         "--bucket", r2.bucket,
         "--key", sentinel,
         "--body", "/dev/null"],
        env=r2._env(),
    )
    return result.returncode


def cmd_upload(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.uploader import UploadManager
    up = UploadManager(args.platform)
    r2_prefix = f"build-resume/{args.platform}/out"
    ok = up.sync_incremental(args.chromium_src / args.out_dir, r2_prefix)
    print("Upload OK" if ok else "Upload FAILED")
    return 0 if ok else 1


def cmd_download(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.uploader import UploadManager
    up = UploadManager(args.platform)
    r2_prefix = f"build-resume/{args.platform}/out"
    local_dir = args.chromium_src / args.out_dir
    local_dir.mkdir(parents=True, exist_ok=True)
    ok = up.sync_incremental(local_dir, r2_prefix)
    print("Download OK" if ok else "Download FAILED")
    return 0 if ok else 1


def cmd_validate(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.validator import BuildValidator, validate_environment, aggregate_results
    from scripts.ci.build_system.manifest import BuildManifest
    from scripts.ci.build_system.uploader import R2Client, UploadManager
    import tempfile
    import os
    r2 = R2Client()
    up = UploadManager(args.platform, r2)
    manifest_key = f"build-resume/{args.platform}/build_state.json"
    tmp = Path(tempfile.mkdtemp()) / "build_state.json"
    up.download_file(manifest_key, tmp)
    try:
        m = BuildManifest.load(tmp)
    except Exception as e:
        print(f"Cannot load manifest: {e}")
        return 1
    v = BuildValidator(args.chromium_src, args.out_dir, args.platform)
    results = []
    results.append(v.validate_manifest(tmp))
    results.append(v.validate_gn_args(
        args.chromium_src / args.out_dir / "args.gn",
        m.get("gn_args_hash", ""),
    ))
    results.append(v.validate_build_ninja(
        args.chromium_src / args.out_dir / "build.ninja",
        m.get("build_gn_hash", ""),
    ))
    results.append(v.validate_ninja_log(
        args.chromium_src / args.out_dir / ".ninja_log",
    ))
    results.append(v.validate_toolchain())
    env_result = validate_environment(m, args.chromium_src, args.browseros_dir, args.repo_root)
    results.append(env_result)
    combined = aggregate_results(results)
    if combined.passed:
        print("All validations passed")
        return 0
    for f in combined.failures:
        print(f"  [{f.severity}] {f.component}: {f.message}")
    return 1


def cmd_recover(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.recovery import auto_repair
    result = auto_repair(
        build_dir=args.chromium_src / args.out_dir,
        platform=args.platform,
    )
    print(f"Recovery: state={result.state}, "
          f"found={result.issues_found}, "
          f"recovered={result.issues_recovered}")
    return 0 if result.success else 1


def cmd_orchestrate(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.orchestrator import WorkflowOrchestrator
    orch = WorkflowOrchestrator(
        platform=args.platform,
        build_dir=args.out_dir,
        chromium_src=args.chromium_src,
        browseros_dir=args.browseros_dir,
        repo_root=args.repo_root,
        start_state=getattr(args, 'start_state', 'PREPARING'),
    )
    return orch.run()


def cmd_dashboard(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.dashboard import BuildDashboard
    db = BuildDashboard(
        build_dir=args.out_dir,
        platform=args.platform,
    )
    status = db.generate()
    path = args.chromium_src / args.out_dir / "build_status.json"
    db.save(path)
    print(f"Dashboard written to {path}")
    return 0


def cmd_release(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.release_validator import ReleaseValidator
    v = ReleaseValidator(args.chromium_src, args.out_dir, args.platform)
    result = v.validate_all()
    if result.passed:
        print("All release validations passed")
    else:
        for f in result.failures:
            print(f"  ✗ {f}")
    return 0 if result.passed else 1


def cmd_disk(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.disk_manager import DiskManager
    dm = DiskManager(args.chromium_src, args.out_dir, args.platform)
    if args.disk_sub == "status":
        usage = dm.disk_usage()
        build_size = dm.build_directory_size()
        print(f"Disk: {usage['free_gb']:.1f} GB free / {usage['total_gb']:.1f} GB total")
        print(f"Build dir: {dm.format_bytes(build_size)}")
        cache = dm.cache_sizes()
        print(f"Cache: {dm.format_bytes(cache.get('ccache_bytes', 0))} (ccache)")
        warning = dm.disk_warning()
        if warning:
            print(f"WARNING: {warning}")
    elif args.disk_sub == "clean":
        freed = 0
        freed += dm.temp_file_cleanup()
        freed += dm.clean_stale_caches().get("freed_bytes", 0)
        print(f"Cleaned {dm.format_bytes(freed)}")
    return 0


def cmd_performance(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.performance import PerformanceTracker, collect_system_stats, parse_ccache_stats
    stats = collect_system_stats()
    print(f"System stats: CPU {stats.get('cpu_percent', 'N/A')}%, "
          f"Memory {stats.get('memory_mb', 'N/A')} MB, "
          f"Disk {stats.get('disk_gb', 'N/A')} GB")
    ccache = parse_ccache_stats()
    if ccache:
        print(f"ccache: hit_rate={ccache.get('hit_rate', 'N/A')}%, "
              f"size={ccache.get('cache_size', 'N/A')}")
    return 0


def main() -> int:
    import os
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="command", required=True)

    # ── Phase 1: Manifest ──────────────────────────────────────
    mp = sub.add_parser("manifest")
    _make_common_args(mp)
    mp.add_argument("manifest_sub", choices=("create", "validate"))

    # ── Phase 2, 4: Checkpoint ─────────────────────────────────
    cp = sub.add_parser("checkpoint")
    _make_common_args(cp)
    cp.add_argument("checkpoint_sub", choices=("create", "list", "restore", "prune"))

    # ── Legacy commands (backward compat) ─────────────────────
    sp = sub.add_parser("status")
    _make_common_args(sp)

    rp = sub.add_parser("run")
    _make_common_args(rp)

    comp = sub.add_parser("complete")
    _make_common_args(comp)

    clp = sub.add_parser("clear")
    _make_common_args(clp)

    # ── Phase 3: Upload/Download ───────────────────────────────
    upp = sub.add_parser("upload")
    _make_common_args(upp)

    dlp = sub.add_parser("download")
    _make_common_args(dlp)

    # ── Phase 5, 13: Validate ───────────────────────────────────
    vp = sub.add_parser("validate")
    _make_common_args(vp)

    # ── Phase 6: Recovery ───────────────────────────────────────
    rcp = sub.add_parser("recover")
    _make_common_args(rcp)

    # ── Phase 7: Orchestrate ────────────────────────────────────
    op = sub.add_parser("orchestrate")
    _make_common_args(op)
    op.add_argument("--start-state", default="PREPARING",
                    choices=["IDLE", "PREPARING", "COMPILING",
                             "VERIFYING", "PACKAGING", "RELEASING",
                             "COMPLETE", "FAILED", "RECOVERING"],
                    help="Override initial workflow state (YAML sets COMPILING)")

    # ── Phase 8: Dashboard ──────────────────────────────────────
    ddp = sub.add_parser("dashboard")
    _make_common_args(ddp)

    # ── Phase 9: Release ─────────────────────────────────────────
    rlp = sub.add_parser("release")
    _make_common_args(rlp)

    # ── Phase 11: Disk ──────────────────────────────────────────
    dkp = sub.add_parser("disk")
    _make_common_args(dkp)
    dkp.add_argument("disk_sub", choices=("status", "clean"))

    # ── Phase 12: Performance ───────────────────────────────────
    pfp = sub.add_parser("performance")
    _make_common_args(pfp)

    args = p.parse_args()
    _resolve_path_args(args)

    dispatch = {
        "manifest": cmd_manifest,
        "checkpoint": cmd_checkpoint,
        "status": _legacy_cmd_status,
        "run": _legacy_cmd_run,
        "complete": _legacy_cmd_complete,
        "clear": _legacy_cmd_clear,
        "upload": cmd_upload,
        "download": cmd_download,
        "validate": cmd_validate,
        "recover": cmd_recover,
        "orchestrate": cmd_orchestrate,
        "dashboard": cmd_dashboard,
        "release": cmd_release,
        "disk": cmd_disk,
        "performance": cmd_performance,
    }
    return dispatch[args.command](args)


def _resolve_path_args(args: argparse.Namespace) -> None:
    import os
    # chromium_src
    if not args.chromium_src or str(args.chromium_src) == ".":
        args.chromium_src = Path(os.environ.get(
            "CHROMIUM_SRC",
            Path.cwd().parent / "chromium" / "src",
        ))
    # browseros_dir
    if not args.browseros_dir or str(args.browseros_dir) == ".":
        args.browseros_dir = Path(os.environ.get(
            "BROWSEROS_DIR",
            Path.cwd() / "packages" / "browseros",
        ))
    # repo_root
    if not args.repo_root or str(args.repo_root) == ".":
        args.repo_root = Path(os.environ.get(
            "REPO_ROOT",
            Path.cwd(),
        ))


def _legacy_cmd_clear(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.uploader import R2Client
    r2 = R2Client()
    prefix = f"build-resume/{args.platform}/"
    import subprocess
    result = subprocess.run(
        ["aws", "s3", "rm", f"s3://{r2.bucket}/{prefix}", "--recursive"],
        env=r2._env(),
    )
    print(f"Cleared build state for {args.platform}")
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())