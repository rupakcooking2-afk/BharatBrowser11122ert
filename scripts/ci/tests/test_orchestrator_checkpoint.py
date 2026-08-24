"""Tests for the orchestrator's checkpoint-failure semantics:

* checkpoint persistence failures must NEVER cancel a healthy compilation
* failures are reported with an explicit WARNING
* after repeated failures checkpointing is disabled and the build must not
  claim resumability (checkpoints_saved() -> False)
"""
import logging

import pytest

from scripts.ci.build_system.orchestrator import (
    MAX_CHECKPOINT_RETRIES,
    WorkflowOrchestrator,
)


@pytest.fixture
def orch(tmp_path, monkeypatch):
    monkeypatch.setenv("GITHUB_REPOSITORY", "acme/bharat")
    src = tmp_path / "src"
    (src / "out" / "Default_x64").mkdir(parents=True)
    o = WorkflowOrchestrator(
        platform="windows-x64",
        build_dir="out/Default_x64",
        chromium_src=src,
        browseros_dir=tmp_path / "pkg",
        repo_root=tmp_path,
        start_state="COMPILING",
    )
    o._total_targets_hint = 44506
    return o


def _always_trigger(orch):
    orch.checkpoint_mgr.should_checkpoint = lambda *a, **k: True


class TestCheckpointFailureSemantics:
    def test_fresh_build_counts_as_healthy(self, orch):
        assert orch.checkpoints_saved() is True

    def test_failed_checkpoint_does_not_raise_and_advances_counters(
        self, orch, caplog
    ):
        _always_trigger(orch)
        orch.checkpoint_mgr.create_checkpoint = lambda since: ("001", False)

        with caplog.at_level(logging.WARNING, logger="scripts.ci.build_system.orchestrator"):
            orch._maybe_checkpoint(completed=100, elapsed_minutes=99.0)

        assert orch._checkpoint_retries == 1
        assert orch.checkpoints_saved() is False
        # Counters advance even on failure — prevents infinite retry loops.
        assert orch._last_checkpoint_targets == 100
        # Required diagnostic is present in output.
        assert any(
            "WARNING: checkpoint could not be persisted" in r.getMessage()
            for r in caplog.records
        )
        # Compilation was NOT cancelled — no exception, state untouched.
        assert orch.current_state == "COMPILING"

    def test_exception_in_checkpoint_is_swallowed(self, orch):
        _always_trigger(orch)

        def boom(since):
            raise RuntimeError("gh command timed out")

        orch.checkpoint_mgr.create_checkpoint = boom
        orch._maybe_checkpoint(completed=200, elapsed_minutes=99.0)
        assert orch._checkpoint_retries == 1
        assert orch.checkpoints_saved() is False

    def test_success_resets_retry_counter(self, orch):
        _always_trigger(orch)
        results = iter([("001", False), ("002", True)])
        orch.checkpoint_mgr.create_checkpoint = lambda since: next(results)

        orch._maybe_checkpoint(100, 99.0)
        assert orch._checkpoint_retries == 1
        orch._maybe_checkpoint(200, 99.0)
        assert orch._checkpoint_retries == 0
        assert orch.checkpoints_saved() is True
        assert not orch._checkpoint_disabled

    def test_disables_after_max_consecutive_failures_but_keeps_building(
        self, orch
    ):
        _always_trigger(orch)
        orch.checkpoint_mgr.create_checkpoint = lambda since: ("00X", False)

        for i in range(MAX_CHECKPOINT_RETRIES):
            orch._maybe_checkpoint(100 * (i + 1), 99.0)

        assert orch._checkpoint_disabled is True
        assert orch.checkpoints_saved() is False

        # After disabling, no further checkpoint attempts happen at all…
        called = []
        orch.checkpoint_mgr.create_checkpoint = lambda since: called.append(since)
        orch._maybe_checkpoint(9999, 99.0)
        assert called == []

    def test_get_progress_reports_unhealthy_checkpoints(self, orch):
        _always_trigger(orch)
        orch.checkpoint_mgr.create_checkpoint = lambda since: ("001", False)
        orch._maybe_checkpoint(100, 99.0)
        progress = orch.get_progress()
        assert progress["checkpoint_healthy"] is False


class TestManifestCheckpointHealth:
    def test_checkpoint_healthy_round_trips_through_save_load(self, tmp_path):
        from scripts.ci.build_system.manifest import BuildManifest

        path = tmp_path / "build_state.json"
        m = BuildManifest("windows-x64", "out/Default_x64")
        m["workflow_state"] = "COMPILING"
        m["checkpoint_healthy"] = False
        m.save(path)

        loaded = BuildManifest.load(path)  # raises on checksum mismatch
        assert loaded["checkpoint_healthy"] is False
        assert loaded.checksum_valid()

    def test_get_returns_default_for_missing_and_unknown(self):
        from scripts.ci.build_system.manifest import BuildManifest

        m = BuildManifest("windows-x64", "out/Default_x64")
        assert m.get("checkpoint_counter", 0) == 0
        assert m.get("checkpoint_healthy") is True
        assert m.get("not_a_real_field", "dflt") == "dflt"
