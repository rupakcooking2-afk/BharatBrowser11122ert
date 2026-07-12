#!/usr/bin/env python3
"""Resumable Ninja build manager for Bharat Browser CI.

Thin CLI entry point for the production-grade fault-tolerant distributed
build system using GitHub Releases for checkpoint storage.

Commands
--------
  manifest create|validate  — Build manifest management (Phase 1)
  checkpoint create|restore — Atomic snapshots via GitHub Releases (Phase 2, 4)
  validate                  — Build validation (Phase 5, 13)
  recover                   — Auto-recovery (Phase 6)
  orchestrate               — Full state machine run (Phase 7)
  dashboard                 — Build status dashboard (Phase 8)
  release                   — Validate and package release (Phase 9)
  disk                      — Disk management (Phase 11)
  performance               — Performance report (Phase 12)
  clear                     — Remove build state + checkpoint release

Usage
    python resume_build.py orchestrate --platform linux-x64 --out-dir out/Default_x64
"""
import argparse
import os
import sys
from pathlib import Path

# Ensure the repository root is on sys.path so that
# "from scripts.ci.build_system.xxx import YYY" resolves correctly
# when resume_build.py is invoked as a script.
_repo_root = Path(__file__).resolve().parents[2]
if str(_repo_root) not in sys.path:
    sys.path.insert(0, str(_repo_root))


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
    m = BuildManifest(args.platform, args.out_dir)
    if args.manifest_sub == "create":
        m.create(
            chromium_src=args.chromium_src,
            browseros_dir=args.browseros_dir,
            repo_root=args.repo_root,
        )
        m.save(args.chromium_src / args.out_dir / "build_state.json")
        print("Manifest created")
        return 0
    elif args.manifest_sub == "validate":
        manifest_path = args.chromium_src / args.out_dir / "build_state.json"
        try:
            m = BuildManifest.load(manifest_path)
            mismatches = m.validate_environment(
                chromium_src=args.chromium_src,
                browseros_dir=args.browseros_dir,
                repo_root=args.repo_root,
            )
            if mismatches:
                print("Manifest mismatches:")
                for mm in mismatches:
                    print(f"  [FAIL] {mm}")
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
        seq, ok = cm.create_checkpoint(time.time())
        print(f"Checkpoint {seq} created (ok={ok})")
        return 0 if ok else 1
    elif args.checkpoint_sub == "restore":
        ok = cm.restore_state()
        print("Checkpoint restored" if ok else "Restore failed")
        return 0 if ok else 1
    return 0


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


def cmd_validate(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.validator import BuildValidator, validate_environment, aggregate_results
    from scripts.ci.build_system.manifest import BuildManifest
    manifest_path = args.chromium_src / args.out_dir / "build_state.json"
    try:
        m = BuildManifest.load(manifest_path)
    except Exception as e:
        print(f"Cannot load manifest: {e}")
        return 1
    v = BuildValidator(args.chromium_src, args.out_dir, args.platform)
    results = []
    results.append(v.validate_manifest(manifest_path))
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
            print(f"  [FAIL] {f}")
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


def cmd_clear(args: argparse.Namespace) -> int:
    from scripts.ci.build_system.checkpoint import CheckpointManager
    import shutil
    cm = CheckpointManager(args.platform, args.out_dir, args.chromium_src)
    cm.clear_all()
    out_path = args.chromium_src / args.out_dir
    if out_path.is_dir():
        shutil.rmtree(str(out_path))
    print(f"Cleared build state for {args.platform}")
    return 0


def main() -> int:
    import argparse
    import time  # noqa: F401 — used in cmd_checkpoint
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="command", required=True)

    # Phase 1
    mp = sub.add_parser("manifest")
    _make_common_args(mp)
    mp.add_argument("manifest_sub", choices=("create", "validate"))

    # Phase 2, 4
    cp = sub.add_parser("checkpoint")
    _make_common_args(cp)
    cp.add_argument("checkpoint_sub", choices=("create", "restore"))

    # Phase 5, 13
    vp = sub.add_parser("validate")
    _make_common_args(vp)

    # Phase 6
    rcp = sub.add_parser("recover")
    _make_common_args(rcp)

    # Phase 7
    op = sub.add_parser("orchestrate")
    _make_common_args(op)
    op.add_argument("--start-state", default="PREPARING",
                    choices=["IDLE", "PREPARING", "COMPILING",
                             "VERIFYING", "PACKAGING", "RELEASING",
                             "COMPLETE", "FAILED", "RECOVERING"])

    # Phase 8
    ddp = sub.add_parser("dashboard")
    _make_common_args(ddp)

    # Phase 9
    rlp = sub.add_parser("release")
    _make_common_args(rlp)

    # Phase 11
    dkp = sub.add_parser("disk")
    _make_common_args(dkp)
    dkp.add_argument("disk_sub", choices=("status", "clean"))

    # Phase 12
    pfp = sub.add_parser("performance")
    _make_common_args(pfp)

    # Utility
    clp = sub.add_parser("clear")
    _make_common_args(clp)

    args = p.parse_args()
    _resolve_path_args(args)

    dispatch = {
        "manifest": cmd_manifest,
        "checkpoint": cmd_checkpoint,
        "validate": cmd_validate,
        "recover": cmd_recover,
        "orchestrate": cmd_orchestrate,
        "dashboard": cmd_dashboard,
        "release": cmd_release,
        "disk": cmd_disk,
        "performance": cmd_performance,
        "clear": cmd_clear,
    }
    return dispatch[args.command](args)


def _resolve_path_args(args: argparse.Namespace) -> None:
    # Evaluate any callable defaults that argparse stored instead of calling
    for attr in ("chromium_src", "browseros_dir", "repo_root"):
        val = getattr(args, attr, None)
        if callable(val):
            setattr(args, attr, val())

    if not args.chromium_src or str(args.chromium_src) == ".":
        args.chromium_src = Path(os.environ.get(
            "CHROMIUM_SRC",
            Path.cwd().parent / "chromium" / "src",
        )).resolve()
    else:
        args.chromium_src = Path(args.chromium_src).resolve()

    if not args.browseros_dir or str(args.browseros_dir) == ".":
        args.browseros_dir = Path(os.environ.get(
            "BROWSEROS_DIR",
            Path.cwd() / "packages" / "browseros",
        )).resolve()
    else:
        args.browseros_dir = Path(args.browseros_dir).resolve()

    if not args.repo_root or str(args.repo_root) == ".":
        args.repo_root = Path(os.environ.get(
            "REPO_ROOT",
            Path.cwd(),
        )).resolve()
    else:
        args.repo_root = Path(args.repo_root).resolve()


if __name__ == "__main__":
    sys.exit(main())
